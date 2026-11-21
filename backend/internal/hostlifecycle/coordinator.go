// Package hostlifecycle is the host's one lifecycle coordinator (#456, #36
// DEC-01). It keeps the ledger of what the host acquired, starts in order,
// takes back what a failed start acquired, and decides whether everything
// stopped. A release it cannot confirm stays in the ledger as needing cleanup;
// it is never reported as stopped.
package hostlifecycle

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

// Resource is one thing the host acquired and must give back. Close returns
// nil only when the release is confirmed.
type Resource interface {
	Close(ctx context.Context) error
}

// Step acquires one resource. A non-nil Resource returned with an error is a
// partial acquisition; the ledger still takes it. Each step must return a
// distinct resource; returning the same resource from two steps closes it twice.
type Step struct {
	Name    string
	Acquire func(ctx context.Context) (Resource, error)
}

// Result is the coordinator's one decision about a stop.
type Result struct {
	Stopped      bool     // every release in the ledger is confirmed
	NeedsCleanup []string // names kept as needing cleanup, in close order
}

var (
	// ErrStopped refuses a start once a stop has begun.
	ErrStopped = errors.New("hostlifecycle: stopped")
	// ErrNeedsCleanup refuses a start while a release is unconfirmed.
	ErrNeedsCleanup   = errors.New("hostlifecycle: a release is unconfirmed")
	errAcquireAborted = errors.New("hostlifecycle: acquisition ended without returning")
)

type entryState uint8

const (
	entryAcquiring entryState = iota + 1
	entryHeld
	entryClosing
	entryNeedsCleanup
)

type ledgerEntry struct {
	name           string
	resource       Resource
	state          entryState
	acquireDone    chan struct{}
	acquireErr     error
	acquireAborted bool
	pastDeadline   bool
	cancel         context.CancelFunc
}

type callResult struct {
	resource Resource
	err      error
	abnormal bool
}

type Coordinator struct {
	mu           sync.Mutex
	budget       time.Duration
	ledger       []*ledgerEntry
	starting     bool
	startDone    chan struct{}
	stopping     bool
	stopDeadline time.Time
	stopSignal   chan struct{}
	decided      bool
	stopDone     chan struct{}
	stopResult   Result
}

// New returns a coordinator that closes within budget, both when a start
// fails and when it stops.
func New(budget time.Duration) *Coordinator {
	if budget <= 0 {
		panic("hostlifecycle: non-positive budget")
	}
	return &Coordinator{
		budget:     budget,
		stopSignal: make(chan struct{}),
		stopDone:   make(chan struct{}),
	}
}

// Start acquires steps in order. On the first error it closes what was
// acquired, in reverse, and returns that error.
func (c *Coordinator) Start(ctx context.Context, steps ...Step) error {
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return ErrStopped
	}
	if c.starting || c.hasStartBarrierLocked() {
		c.mu.Unlock()
		return ErrNeedsCleanup
	}
	c.starting = true
	done := make(chan struct{})
	c.startDone = done
	c.mu.Unlock()

	err := c.startSteps(ctx, steps)
	c.finishStart(done)
	return err
}

func (c *Coordinator) startSteps(ctx context.Context, steps []Step) error {
	owned := make([]*ledgerEntry, 0, len(steps))
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return c.failStart(err, owned, false)
		}

		c.mu.Lock()
		if c.stopping {
			c.mu.Unlock()
			return ErrStopped
		}
		entry := &ledgerEntry{
			name:        step.Name,
			state:       entryAcquiring,
			acquireDone: make(chan struct{}),
		}
		stepCtx, cancel := context.WithCancel(ctx)
		entry.cancel = cancel
		c.ledger = append(c.ledger, entry)
		owned = append(owned, entry)
		c.mu.Unlock()

		result := callAsync(func() (Resource, error) {
			return step.Acquire(stepCtx)
		})
		go c.recordAcquisition(entry, result)

		select {
		case <-entry.acquireDone:
		case <-ctx.Done():
		case <-c.stopSignal:
		}

		c.mu.Lock()
		stopping := c.stopping
		completed := channelClosed(entry.acquireDone)
		acquireErr := entry.acquireErr
		acquireAborted := entry.acquireAborted
		c.mu.Unlock()

		stepErr := acquireErr
		if stepErr == nil && acquireAborted {
			stepErr = errAcquireAborted
		}
		if stopping {
			if stepErr != nil {
				return stepErr
			}
			return ErrStopped
		}
		if completed {
			if stepErr != nil {
				return c.failStart(stepErr, owned, true)
			}
			if err := ctx.Err(); err != nil {
				return c.failStart(err, owned, false)
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return c.failStart(err, owned, false)
		}
		return ErrStopped
	}
	return nil
}

// Stop seals the coordinator, cancels acquisitions in progress, and closes
// the ledger in reverse acquisition order within the budget. It decides once.
func (c *Coordinator) Stop() Result {
	c.mu.Lock()
	if c.stopping {
		done := c.stopDone
		c.mu.Unlock()
		<-done
		return c.copyStopResult()
	}
	c.stopping = true
	c.stopDeadline = time.Now().Add(c.budget)
	close(c.stopSignal)
	deadline := c.stopDeadline
	startDone := c.startDone
	var cancels []context.CancelFunc
	for _, entry := range c.ledger {
		if entry.state == entryAcquiring && entry.cancel != nil {
			cancels = append(cancels, entry.cancel)
			entry.cancel = nil
		}
	}
	c.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	if startDone != nil {
		waitUntil(deadline, startDone)
	}
	for {
		c.mu.Lock()
		var pending *ledgerEntry
		for _, entry := range c.ledger {
			if entry.state == entryAcquiring {
				pending = entry
				break
			}
		}
		c.mu.Unlock()
		if pending == nil {
			break
		}
		if !waitUntil(deadline, pending.acquireDone) && !time.Now().Before(deadline) {
			break
		}
	}

	c.mu.Lock()
	entries := reverseEntries(c.ledger)
	c.mu.Unlock()
	c.closeEntries(entries, deadline)
	return c.finishStop()
}

func (c *Coordinator) recordAcquisition(entry *ledgerEntry, result <-chan callResult) {
	outcome := <-result
	c.mu.Lock()
	entry.resource = outcome.resource
	entry.acquireErr = outcome.err
	entry.acquireAborted = outcome.abnormal
	if entry.state == entryAcquiring {
		switch {
		case c.stopping && !time.Now().Before(c.stopDeadline):
			entry.state = entryNeedsCleanup
			entry.pastDeadline = true
		case outcome.resource != nil:
			entry.state = entryHeld
		case outcome.abnormal:
			entry.state = entryNeedsCleanup
		default:
			c.removeEntryLocked(entry)
		}
	}
	cancel := entry.cancel
	entry.cancel = nil
	close(entry.acquireDone)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Coordinator) failStart(err error, owned []*ledgerEntry, acquisitionFailure bool) error {
	c.mu.Lock()
	stopping := c.stopping
	deadline := time.Now().Add(c.budget)
	c.mu.Unlock()
	if stopping {
		if acquisitionFailure {
			return err
		}
		return ErrStopped
	}

	if !c.waitForAcquisitions(owned, deadline) {
		return err
	}
	c.closeEntries(c.trackedReverse(owned), deadline)
	return err
}

func (c *Coordinator) waitForAcquisitions(entries []*ledgerEntry, deadline time.Time) bool {
	for {
		c.mu.Lock()
		if c.decided {
			c.mu.Unlock()
			return false
		}
		if !time.Now().Before(deadline) {
			c.markNeedsCleanupLocked(entries)
			c.mu.Unlock()
			return false
		}
		var pending *ledgerEntry
		for _, entry := range entries {
			if entry.state == entryAcquiring && c.containsEntryLocked(entry) {
				pending = entry
				break
			}
		}
		c.mu.Unlock()
		if pending == nil {
			return true
		}
		waitUntil(deadline, pending.acquireDone)
	}
}

// closeEntries receives entries in reverse acquisition order.
func (c *Coordinator) closeEntries(entries []*ledgerEntry, deadline time.Time) {
	for i, entry := range entries {
		c.mu.Lock()
		if c.decided {
			c.mu.Unlock()
			return
		}
		if !time.Now().Before(deadline) {
			c.markNeedsCleanupLocked(entries[i:])
			c.mu.Unlock()
			return
		}
		if entry.state == entryNeedsCleanup && entry.pastDeadline {
			c.markNeedsCleanupLocked(entries[i:])
			c.mu.Unlock()
			return
		}
		if entry.state != entryHeld {
			c.mu.Unlock()
			continue
		}
		entry.state = entryClosing
		resource := entry.resource
		c.mu.Unlock()

		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		result := callAsync(func() (Resource, error) {
			return nil, resource.Close(ctx)
		})
		var outcome callResult
		completed := false
		select {
		case outcome = <-result:
			completed = true
		case <-ctx.Done():
			select {
			case outcome = <-result:
				completed = true
			default:
			}
		}
		cancel()

		c.mu.Lock()
		if c.decided || entry.state != entryClosing {
			c.mu.Unlock()
			return
		}
		if !completed {
			c.markNeedsCleanupLocked(entries[i:])
			c.mu.Unlock()
			return
		}
		if outcome.abnormal || outcome.err != nil {
			entry.state = entryNeedsCleanup
		} else {
			c.removeEntryLocked(entry)
		}
		if !time.Now().Before(deadline) {
			c.markNeedsCleanupLocked(entries[i+1:])
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

func callAsync(call func() (Resource, error)) <-chan callResult {
	result := make(chan callResult, 1)
	go func() {
		var outcome callResult
		returned := false
		defer func() {
			_ = recover()
			if !returned {
				outcome.abnormal = true
			}
			if isNilResource(outcome.resource) {
				outcome.resource = nil
			}
			result <- outcome
		}()
		outcome.resource, outcome.err = call()
		returned = true
	}()
	return result
}

func (c *Coordinator) finishStart(done chan struct{}) {
	c.mu.Lock()
	if c.startDone == done {
		c.starting = false
		c.startDone = nil
		close(done)
	}
	c.mu.Unlock()
}

func (c *Coordinator) finishStop() Result {
	c.mu.Lock()
	c.decided = true
	needs := make([]string, 0, len(c.ledger))
	for i := len(c.ledger) - 1; i >= 0; i-- {
		entry := c.ledger[i]
		entry.state = entryNeedsCleanup
		needs = append(needs, entry.name)
	}
	c.stopResult = Result{Stopped: len(c.ledger) == 0, NeedsCleanup: needs}
	result := cloneResult(c.stopResult)
	close(c.stopDone)
	c.mu.Unlock()
	return result
}

func (c *Coordinator) copyStopResult() Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneResult(c.stopResult)
}

func (c *Coordinator) hasStartBarrierLocked() bool {
	for _, entry := range c.ledger {
		if entry.state == entryAcquiring || entry.state == entryNeedsCleanup {
			return true
		}
	}
	return false
}

func (c *Coordinator) trackedReverse(entries []*ledgerEntry) []*ledgerEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	var reversed []*ledgerEntry
	for i := len(entries) - 1; i >= 0; i-- {
		if c.containsEntryLocked(entries[i]) {
			reversed = append(reversed, entries[i])
		}
	}
	return reversed
}

func reverseEntries(entries []*ledgerEntry) []*ledgerEntry {
	reversed := make([]*ledgerEntry, len(entries))
	for i := range entries {
		reversed[len(entries)-1-i] = entries[i]
	}
	return reversed
}

func (c *Coordinator) markNeedsCleanupLocked(entries []*ledgerEntry) {
	for _, entry := range entries {
		if c.containsEntryLocked(entry) {
			if entry.state == entryAcquiring || entry.state == entryClosing {
				entry.pastDeadline = true
			}
			entry.state = entryNeedsCleanup
		}
	}
}

func (c *Coordinator) containsEntryLocked(entry *ledgerEntry) bool {
	for _, candidate := range c.ledger {
		if candidate == entry {
			return true
		}
	}
	return false
}

func (c *Coordinator) removeEntryLocked(entry *ledgerEntry) {
	for i, candidate := range c.ledger {
		if candidate == entry {
			copy(c.ledger[i:], c.ledger[i+1:])
			c.ledger[len(c.ledger)-1] = nil
			c.ledger = c.ledger[:len(c.ledger)-1]
			return
		}
	}
}

func isNilResource(resource Resource) bool {
	if resource == nil {
		return true
	}
	v := reflect.ValueOf(resource)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func waitUntil(deadline time.Time, done <-chan struct{}) bool {
	if !time.Now().Before(deadline) {
		return false
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return time.Now().Before(deadline)
	case <-timer.C:
		return false
	}
}

func cloneResult(result Result) Result {
	result.NeedsCleanup = append([]string(nil), result.NeedsCleanup...)
	return result
}
