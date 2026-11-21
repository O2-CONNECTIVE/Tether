// Package hostlifecycle is the host's one lifecycle coordinator (#456, #36
// DEC-01). It keeps the ledger of what the host acquired, starts in order,
// takes back what a failed start acquired, and decides whether everything
// stopped. A release it cannot confirm stays in the ledger as needing cleanup;
// it is never reported as stopped.
package hostlifecycle

import (
	"context"
	"errors"
	"fmt"
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
// partial acquisition; the ledger still takes it.
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
	ErrNeedsCleanup = errors.New("hostlifecycle: a release is unconfirmed")
)

type entryState uint8

const (
	entryAcquiring entryState = iota + 1
	entryActive
	entryClosing
	entryNeedsCleanup
)

type ledgerEntry struct {
	name            string
	resource        Resource
	state           entryState
	acquireDone     chan struct{}
	acquireErr      error
	acquirePanicked bool
}

type rollbackRun struct {
	entries []*ledgerEntry
	done    chan struct{}
}

type Coordinator struct {
	mu            sync.Mutex
	startMu       sync.Mutex
	budget        time.Duration
	ledger        []*ledgerEntry
	stopStarted   bool
	stopCh        chan struct{}
	stopDone      chan struct{}
	stopResult    Result
	acquiring     *ledgerEntry
	acquireCancel context.CancelFunc
	rollbackDone  chan struct{}
}

// New returns a coordinator that closes within budget, both when a start
// fails and when it stops.
func New(budget time.Duration) *Coordinator {
	if budget <= 0 {
		panic("hostlifecycle: non-positive budget")
	}
	return &Coordinator{
		budget:   budget,
		stopCh:   make(chan struct{}),
		stopDone: make(chan struct{}),
	}
}

// Start acquires steps in order. On the first error it closes what was
// acquired, in reverse, and returns that error.
func (c *Coordinator) Start(ctx context.Context, steps ...Step) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	if c.stopStarted {
		c.mu.Unlock()
		return ErrStopped
	}
	if c.hasStartBarrierLocked() {
		c.mu.Unlock()
		return ErrNeedsCleanup
	}
	c.mu.Unlock()

	owned := make([]*ledgerEntry, 0, len(steps))
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return c.failStart("", err, owned)
		}

		c.mu.Lock()
		if c.stopStarted {
			c.mu.Unlock()
			return ErrStopped
		}
		if c.hasStartBarrierLocked() {
			c.mu.Unlock()
			return ErrNeedsCleanup
		}
		entry := &ledgerEntry{
			name:        step.Name,
			state:       entryAcquiring,
			acquireDone: make(chan struct{}),
		}
		stepCtx, cancel := context.WithCancel(ctx)
		c.ledger = append(c.ledger, entry)
		c.acquiring = entry
		c.acquireCancel = cancel
		c.mu.Unlock()

		owned = append(owned, entry)
		go c.acquire(stepCtx, cancel, entry, step)

		select {
		case <-entry.acquireDone:
		case <-ctx.Done():
			select {
			case <-entry.acquireDone:
			default:
			}
		case <-c.stopCh:
		}

		c.mu.Lock()
		stopped := c.stopStarted
		completed := channelClosed(entry.acquireDone)
		acquireErr := entry.acquireErr
		panicked := entry.acquirePanicked
		c.mu.Unlock()

		if stopped {
			if panicked {
				acquireErr = fmt.Errorf("hostlifecycle: step %q acquisition panicked", step.Name)
			}
			if acquireErr != nil {
				return errors.Join(ErrStopped, fmt.Errorf("hostlifecycle: step %q: %w", step.Name, acquireErr))
			}
			return ErrStopped
		}
		if !completed {
			return c.failStart("", ctx.Err(), owned)
		}
		if err := ctx.Err(); err != nil {
			return c.failStart("", err, owned)
		}
		if panicked {
			acquireErr = fmt.Errorf("hostlifecycle: step %q acquisition panicked", step.Name)
		}
		if acquireErr != nil {
			return c.failStart(step.Name, acquireErr, owned)
		}
	}
	return nil
}

// Stop seals the coordinator, cancels an acquisition in progress, and closes
// the ledger in reverse acquisition order within the budget. It decides once.
func (c *Coordinator) Stop() Result {
	deadline := time.Now().Add(c.budget)
	c.mu.Lock()
	if c.stopStarted {
		done := c.stopDone
		c.mu.Unlock()
		<-done
		return c.copyStopResult()
	}
	c.stopStarted = true
	close(c.stopCh)
	if c.acquireCancel != nil {
		c.acquireCancel()
	}
	var pending []*ledgerEntry
	for _, entry := range c.ledger {
		if entry.state == entryAcquiring {
			pending = append(pending, entry)
		}
	}
	rollbackDone := c.rollbackDone
	c.mu.Unlock()

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for _, entry := range pending {
		if !waitForDone(ctx, entry.acquireDone) {
			c.markLedgerNeedsCleanup()
			return c.finishStop()
		}
	}
	if rollbackDone != nil && !waitForDone(ctx, rollbackDone) {
		c.markLedgerNeedsCleanup()
		return c.finishStop()
	}
	if ctx.Err() != nil {
		c.markLedgerNeedsCleanup()
		return c.finishStop()
	}

	c.mu.Lock()
	entries := append([]*ledgerEntry(nil), c.ledger...)
	c.mu.Unlock()
	for i := len(entries) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			c.markLedgerNeedsCleanup()
			break
		}
		entry := entries[i]
		c.mu.Lock()
		if entry.state == entryNeedsCleanup {
			c.mu.Unlock()
			continue
		}
		if entry.state != entryActive || entry.resource == nil {
			entry.state = entryNeedsCleanup
			c.mu.Unlock()
			continue
		}
		entry.state = entryClosing
		resource := entry.resource
		c.mu.Unlock()

		closeDone := make(chan error, 1)
		go func() { closeDone <- safeClose(resource, ctx) }()
		var closeErr error
		select {
		case closeErr = <-closeDone:
			if ctx.Err() != nil {
				c.markLedgerNeedsCleanup()
				break
			}
			c.finishClose(entry, closeErr)
		case <-ctx.Done():
			c.markLedgerNeedsCleanup()
		}
		if ctx.Err() != nil {
			break
		}
	}
	return c.finishStop()
}

func (c *Coordinator) acquire(ctx context.Context, cancel context.CancelFunc, entry *ledgerEntry, step Step) {
	resource, err, panicked := safeAcquire(ctx, step.Acquire)
	cancel()
	c.mu.Lock()
	entry.acquireErr = err
	entry.acquirePanicked = panicked
	if resource != nil {
		entry.resource = resource
	}
	if entry.state != entryNeedsCleanup {
		switch {
		case panicked:
			entry.state = entryNeedsCleanup
		case resource == nil:
			c.removeEntryLocked(entry)
		default:
			entry.state = entryActive
		}
	}
	if c.acquiring == entry {
		c.acquiring = nil
		c.acquireCancel = nil
	}
	close(entry.acquireDone)
	c.mu.Unlock()
}

func safeAcquire(ctx context.Context, acquire func(context.Context) (Resource, error)) (resource Resource, err error, panicked bool) {
	defer func() {
		if recover() != nil {
			resource = nil
			err = errors.New("acquisition panicked")
			panicked = true
		}
	}()
	resource, err = acquire(ctx)
	if isNilResource(resource) {
		resource = nil
	}
	return resource, err, false
}

func safeClose(resource Resource, ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("close panicked")
		}
	}()
	return resource.Close(ctx)
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

func (c *Coordinator) failStart(name string, err error, owned []*ledgerEntry) error {
	c.mu.Lock()
	if c.stopStarted {
		c.mu.Unlock()
		if err == nil {
			return ErrStopped
		}
		return errors.Join(ErrStopped, err)
	}
	run := c.beginRollbackLocked(owned)
	c.mu.Unlock()
	c.runRollback(run)
	if name != "" && err != nil {
		return fmt.Errorf("hostlifecycle: step %q: %w", name, err)
	}
	return err
}

func (c *Coordinator) beginRollbackLocked(owned []*ledgerEntry) *rollbackRun {
	entries := make([]*ledgerEntry, 0, len(owned))
	for _, entry := range owned {
		if entry.state == entryActive && entry.resource != nil {
			entry.state = entryClosing
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	done := make(chan struct{})
	c.rollbackDone = done
	return &rollbackRun{entries: entries, done: done}
}

func (c *Coordinator) runRollback(run *rollbackRun) {
	if run == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.budget)
	defer cancel()
	for i := len(run.entries) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			c.markNeedsCleanup(run.entries[:i+1])
			break
		}
		entry := run.entries[i]
		c.mu.Lock()
		resource := entry.resource
		c.mu.Unlock()
		closeDone := make(chan error, 1)
		go func() { closeDone <- safeClose(resource, ctx) }()
		select {
		case err := <-closeDone:
			if ctx.Err() != nil {
				c.markNeedsCleanup(run.entries[:i+1])
				break
			}
			c.finishClose(entry, err)
		case <-ctx.Done():
			c.markNeedsCleanup(run.entries[:i+1])
		}
		if ctx.Err() != nil {
			break
		}
	}
	c.mu.Lock()
	if c.rollbackDone == run.done {
		c.rollbackDone = nil
		close(run.done)
	}
	c.mu.Unlock()
}

func (c *Coordinator) finishClose(entry *ledgerEntry, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.state == entryNeedsCleanup {
		return
	}
	if err == nil {
		c.removeEntryLocked(entry)
		return
	}
	entry.state = entryNeedsCleanup
}

func (c *Coordinator) markNeedsCleanup(entries []*ledgerEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range entries {
		entry.state = entryNeedsCleanup
	}
}

func (c *Coordinator) markLedgerNeedsCleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.ledger {
		entry.state = entryNeedsCleanup
	}
}

func (c *Coordinator) hasStartBarrierLocked() bool {
	for _, entry := range c.ledger {
		if entry.state == entryAcquiring || entry.state == entryClosing || entry.state == entryNeedsCleanup {
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

func (c *Coordinator) finishStop() Result {
	c.mu.Lock()
	needs := make([]string, 0)
	for i := len(c.ledger) - 1; i >= 0; i-- {
		if c.ledger[i].state == entryNeedsCleanup {
			needs = append(needs, c.ledger[i].name)
		} else {
			c.ledger[i].state = entryNeedsCleanup
			needs = append(needs, c.ledger[i].name)
		}
	}
	c.stopResult = Result{Stopped: len(needs) == 0, NeedsCleanup: needs}
	result := cloneResult(c.stopResult)
	c.mu.Unlock()
	close(c.stopDone)
	return result
}

func (c *Coordinator) copyStopResult() Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneResult(c.stopResult)
}

func cloneResult(result Result) Result {
	result.NeedsCleanup = append([]string(nil), result.NeedsCleanup...)
	return result
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func waitForDone(ctx context.Context, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
