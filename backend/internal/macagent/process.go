package macagent

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

type signal uint8

const (
	sigTerm signal = iota + 1
	sigKill
)

type exitInfo struct{ code int32 }

// osProcess is the owned leader of a fresh session and process group (pgid ==
// pid). process calls every method under its own mutex, so a group signal never
// races the single reap, and none of the methods blocks.
type osProcess interface {
	// signalGroup signals the leader's process group. process calls it only
	// while the leader is unreaped and its ownership is not lost, which keeps
	// the PID and group ID reserved. There is no PID-only or name-based signal.
	signalGroup(signal) error
	// pollExit reports whether the leader has exited, without reaping it. known
	// says whether info carries the leader's wait status. An error means exit
	// cannot be observed; ownership is unaffected and the owner still sweeps.
	pollExit() (exited bool, info exitInfo, known bool, err error)
	// tryReap reaps the leader at most once. After an error the reap state is
	// lost and no further signal is sent.
	tryReap() (exited bool, info exitInfo, err error)
	// releaseObserver frees the exit observer after the reap. It is attempted
	// once; an error is permanent and the descriptor is never closed again.
	releaseObserver() error
}

var (
	errGroupObservation = errors.New("macagent: owned group snapshot is unconfirmed")
	errStreamRead       = errors.New("macagent: owned output did not end at EOF")
	errStreamWrite      = errors.New("macagent: owned command input delivery is unconfirmed")
	errUnconfirmed      = errors.New("macagent: owned shell exit is unconfirmed")
	errIOUnconfirmed    = errors.New("macagent: owned stdio is still held; a descendant may have left the process group")
	errSignal           = errors.New("macagent: the owned process group could not be signalled")
	errNotTerminated    = errors.New("macagent: release requires a confirmed terminate")
	errObserver         = errors.New("macagent: leader exit cannot be observed without reaping")
	errForcedExit       = errors.New("macagent: leader exit followed a stop signal and is not a natural result")
	errCloseRetry       = errors.New("macagent: close was not attempted; the descriptor is still owned")
	errCloseUncertain   = errors.New("macagent: close result is uncertain; the descriptor is never closed again")
)

type timing struct {
	termGrace, killWait, ioWait, poll time.Duration
}

type event struct {
	stream agentexec.Stream
	data   []byte
}

const (
	chunkSize  = 32 << 10
	eventSlots = 4 // a reader starts read n only after n-1 chunks were queued
)

// ownedCloser records close attempts on one owned descriptor. A closer that
// returns errCloseRetry certifies it did not release the descriptor, so the
// same owner may try again. Any other error leaves the result permanently
// uncertain and the descriptor is never closed again, so a reused descriptor
// number can never be closed by this owner.
type ownedCloser struct {
	mu   sync.Mutex
	c    io.Closer
	done bool
	err  error
}

func newOwnedCloser(c io.Closer) *ownedCloser { return &ownedCloser{c: c} }

func (o *ownedCloser) close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done {
		return o.err
	}
	err := o.c.Close()
	if err != nil && errors.Is(err, errCloseRetry) {
		return err
	}
	o.done = true
	if err != nil {
		o.err = errors.Join(errCloseUncertain, err)
	}
	return o.err
}

// process owns one shell leader, its process group and its three pipes. It
// implements agentexec.GuestProcess for the Mac current-user profile.
type ioCompletion uint8

const (
	ioPending ioCompletion = iota
	ioNormal
	ioExpectedLocalClose
	ioAbnormal
)

type ownedGroupObserver interface {
	// Called under process.mu and sweep, only for a natural known exit before
	// any signal. The native owner supplies its PID; no remote identifier enters.
	observeOwnedGroup() groupObservation
}

type process struct {
	terminalState atomic.Uint32 // 0 active, 1 terminal unknown, 2 confirmed released
	disposition   *TerminalDisposition
	partialPipes  [3]*ownedCloser // original streams from incomplete plumbing; never discarded
	diagnostics   *CleanupDiagnostics
	os            osProcess
	timing        timing

	stdin, stdout, stderr       *ownedCloser
	events                      chan event
	discard                     chan struct{} // closed by Terminate: readers keep draining to EOF
	discardOnce                 sync.Once
	ioDone                      chan struct{} // both readers reached EOF and the stdin writer returned
	readersLeft                 atomic.Int32
	stdinCloseRequested         atomic.Bool
	ioWrite, ioStdout, ioStderr ioCompletion // protected by mu
	ioErr                       error        // sticky fixed error, never raw OS text
	groupAttempted              bool         // protected by mu; never retry an observation
	groupState                  groupObservation
	groupErr                    error // sticky, including after a later cleanup request

	mu            sync.Mutex // orders signals, observation, the reap and observer release
	exitSeen      bool       // leader exit observed (by the observer or by the reap)
	natural       bool       // the exit was observed before any stop signal
	natInfo       exitInfo
	natKnown      bool
	stopStarted   bool // first stop sweep only; never reset
	termAttempted bool
	killAttempted bool
	signaled      bool
	reaped        bool
	reapInfo      exitInfo
	lost          error // reap state unknown: no more signals
	obsErr        error // sticky: exit observation failed; the sweep still runs
	sigErr        error // sticky: a stop signal failed, so the group sweep is unproven

	sweep      sync.Mutex // serializes stopAndReap from Wait and Terminate
	term       sync.Mutex // serializes Terminate and Release
	terminated bool
	released   bool
}

func newProcess(sp spawned, t timing, stdin []byte) *process {
	p := &process{
		os: sp.proc, timing: t,
		stdin: newOwnedCloser(sp.stdin), stdout: newOwnedCloser(sp.stdout), stderr: newOwnedCloser(sp.stderr),
		events: make(chan event, eventSlots), discard: make(chan struct{}), ioDone: make(chan struct{}),
	}
	p.readersLeft.Store(2)
	var wg sync.WaitGroup
	wg.Add(3)
	go p.write(&wg, sp.stdin, append([]byte(nil), stdin...))
	go p.read(&wg, agentexec.Stdout, sp.stdout)
	go p.read(&wg, agentexec.Stderr, sp.stderr)
	go func() {
		wg.Wait()
		close(p.ioDone)
	}()
	return p
}

// write delivers the command bytes to the shell's stdin, then closes it. A
// blocked write is released when Terminate closes stdin.
func (p *process) write(wg *sync.WaitGroup, w io.Writer, data []byte) {
	defer wg.Done()
	var err error
	if len(data) > 0 {
		var n int
		n, err = w.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	state := ioNormal
	if err != nil {
		state = ioAbnormal
		if p.stdinCloseRequested.Load() && (errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)) {
			state = ioExpectedLocalClose
		}
	}
	p.mu.Lock()
	p.ioWrite = state
	if state == ioAbnormal {
		p.ioErr = errors.Join(p.ioErr, errStreamWrite)
	}
	p.mu.Unlock()
	_ = p.stdin.close()
}

// read forwards bounded chunks in observation order. After Terminate it
// discards data but keeps reading, because only EOF proves no holder remains.
func (p *process) read(wg *sync.WaitGroup, s agentexec.Stream, r io.Reader) {
	defer wg.Done()
	defer func() {
		if p.readersLeft.Add(-1) == 0 {
			close(p.events)
		}
	}()
	for {
		buf := make([]byte, chunkSize)
		n, err := r.Read(buf)
		if n > 0 {
			select {
			case p.events <- event{stream: s, data: buf[:n]}:
			case <-p.discard:
			}
		}
		if err != nil {
			p.mu.Lock()
			state := ioNormal
			if err != io.EOF {
				state = ioAbnormal
				p.ioErr = errors.Join(p.ioErr, errStreamRead)
			}
			if s == agentexec.Stdout {
				p.ioStdout = state
			} else {
				p.ioStderr = state
			}
			p.mu.Unlock()
			return
		}
	}
}

func (p *process) Next(ctx context.Context) (agentexec.Stream, []byte, error) {
	if p.terminalState.Load() == 1 {
		return 0, nil, ErrTerminalCleanupUnknown
	}
	select {
	case ev, ok := <-p.events:
		if !ok {
			p.mu.Lock()
			err := p.ioErr
			p.mu.Unlock()
			if err != nil {
				return 0, nil, err
			}
			return 0, nil, io.EOF
		}
		return ev.stream, ev.data, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// observeLocked polls the non-reaping observer once.
func (p *process) observeLocked() (bool, error) {
	if p.terminalState.Load() == 1 {
		return false, ErrTerminalCleanupUnknown
	}
	switch {
	case p.exitSeen || p.reaped:
		return true, nil
	case p.lost != nil:
		return false, p.lost
	case p.obsErr != nil:
		return false, p.obsErr
	}
	exited, info, known, err := p.os.pollExit()
	if err != nil {
		p.obsErr = errors.Join(errObserver, err)
		return false, p.obsErr
	}
	if exited {
		p.exitSeen, p.natural, p.natInfo, p.natKnown = true, !p.signaled, info, known
	}
	return exited, nil
}

// Wait reports the leader's natural exit status without reaping it first: the
// leader stays reserved so stopAndReap still targets the same owned group.
// When a valid exit event omits its status, the status comes from the reap, which runs
// only after that sweep. An exit seen after a stop signal is not reported as a
// natural result.
func (p *process) Wait(ctx context.Context) (agentexec.ExitStatus, error) {
	ticker := time.NewTicker(p.timing.poll)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		exited, err := p.observeLocked()
		natural, known, info := p.natural, p.natKnown, p.natInfo
		p.mu.Unlock()
		if err != nil {
			return agentexec.ExitStatus{}, err
		}
		if exited {
			if !natural {
				return agentexec.ExitStatus{}, errForcedExit
			}
			if !known {
				if err := p.stopAndReap(ctx); err != nil {
					return agentexec.ExitStatus{}, err
				}
				p.mu.Lock()
				info = p.reapInfo
				p.mu.Unlock()
			}
			return agentexec.ExitStatus{Code: info.code}, nil
		}
		select {
		case <-ctx.Done():
			return agentexec.ExitStatus{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// signal sends a group signal only while the leader is provably unreaped and
// owned. A successful group signal means delivery to the group, not proof that
// every member exited.
func (p *process) signal(s signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminalState.Load() == 1 || p.reaped || p.lost != nil {
		return
	}
	p.signaled = true
	if s == sigTerm {
		p.termAttempted = true
	} else if s == sigKill {
		p.killAttempted = true
	}
	if err := p.os.signalGroup(s); err != nil {
		kind := cleanupSignalTerm
		if s == sigKill {
			kind = cleanupSignalKill
		}
		p.diagnostics.record(kind, err)
		if p.sigErr == nil {
			p.sigErr = errors.Join(errSignal, err)
		}
	}
}

// stopAndReap is the only path that reaps. A fresh known natural exit may use
// an anchor-only group snapshot before any signal. Otherwise it sends TERM, waits
// for the configured grace period unless the caller context ends, then sends
// KILL to the same group, all before the reap. A fresh successful TERM may also use the
// independent pre-KILL anchor-only observation. A natural exit without an eligible snapshot
// adapter follows this original sweep. A failed attempted snapshot is sticky
// unknown and retains the anchor; it is not retried through another API.
// Successful signal delivery does not prove every member has exited.
// Stdio EOF does not shorten the TERM grace or prove that group members exited.
func (p *process) stopAndReap(ctx context.Context) error {
	p.sweep.Lock()
	defer p.sweep.Unlock()
	p.mu.Lock()
	if p.terminalState.Load() == 1 {
		p.mu.Unlock()
		return ErrTerminalCleanupUnknown
	}
	// A failed observation is never retried or replaced by a signal-based
	// success. Retain the unreaped anchor for this still-unknown owner.
	if p.groupErr != nil {
		err := p.groupErr
		p.mu.Unlock()
		return err
	}
	if !p.reaped && p.lost == nil && p.exitSeen && p.natural && p.natKnown &&
		!p.signaled && p.sigErr == nil {
		if observer, ok := p.os.(ownedGroupObserver); ok {
			if !p.groupAttempted {
				p.groupAttempted = true
				p.groupState = observer.observeOwnedGroup()
			}
			if p.groupState != groupAnchorOnly {
				p.groupErr = errGroupObservation
				p.mu.Unlock()
				return errGroupObservation
			}
			// The same owner mutex stays held from observation through the only reap.
			// This is a point-in-time group snapshot, not future quiescence.
			reaped, err := p.reapLocked()
			if err != nil {
				p.groupErr = errors.Join(errGroupObservation, err)
			}
			if err == nil && !reaped {
				p.groupErr = errGroupObservation
			}
			result := p.groupErr
			p.mu.Unlock()
			return result
		}
	}
	reaped, lost := p.reaped, p.lost
	firstStop := !p.stopStarted && !p.signaled && !p.termAttempted && !p.killAttempted
	p.stopStarted = true
	p.mu.Unlock()
	if reaped {
		return nil
	}
	if lost != nil {
		return lost
	}
	p.signal(sigTerm)
	waitTermGrace(ctx, p.timing.termGrace)
	if handled, err := p.preKillSnapshot(firstStop); handled {
		return err
	}
	p.signal(sigKill)
	reaped, err := p.awaitReap(ctx, p.timing.killWait)
	if err != nil {
		return err
	}
	if !reaped {
		p.diagnostics.record(cleanupReapTimeout, nil)
		return errUnconfirmed
	}
	return nil
}

// waitTermGrace preserves the original caller budget without letting early
// stdio EOF advance the first group decision. A caller cancellation/deadline
// still ends this wait and proceeds to the existing nonblocking stop decision.
// Neither timer expiry nor context completion is cleanup evidence.
func waitTermGrace(ctx context.Context, limit time.Duration) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// preKillSnapshot is only for a fresh TERM-success stop, before any KILL.
// TERM delivery is not cleanup proof: the independent anchor-only snapshot is.
// A negative attempted observation is sticky unknown, never rescued by KILL.
// A normally observed live leader is ineligible and follows the existing stop.
func (p *process) preKillSnapshot(firstStop bool) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminalState.Load() == 1 {
		return true, ErrTerminalCleanupUnknown
	}
	if p.groupErr != nil {
		return true, p.groupErr
	}
	if p.lost != nil {
		return true, p.lost
	}
	if !firstStop || !p.termAttempted || p.killAttempted || p.sigErr != nil || p.reaped {
		return false, nil
	}
	observer, ok := p.os.(ownedGroupObserver)
	if !ok {
		return false, nil
	}
	exited, err := p.observeLocked()
	if err != nil {
		p.groupErr = errors.Join(errGroupObservation, err)
		return true, p.groupErr
	}
	if !exited {
		return false, nil
	} // live, not an error/unknown conversion
	if !p.natKnown || p.groupAttempted {
		p.groupErr = errGroupObservation
		return true, p.groupErr
	}
	p.groupAttempted = true
	p.groupState = observer.observeOwnedGroup()
	if p.groupState != groupAnchorOnly {
		p.groupErr = errGroupObservation
		return true, p.groupErr
	}
	// sweep and mu remain held through this same-generation sole-waiter reap.
	reaped, err := p.reapLocked()
	if err != nil {
		p.groupErr = errors.Join(errGroupObservation, err)
	} else if !reaped {
		p.groupErr = errGroupObservation
	}
	return true, p.groupErr
}

func (p *process) awaitReap(ctx context.Context, limit time.Duration) (bool, error) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	ticker := time.NewTicker(p.timing.poll)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		reaped, err := p.reapLocked()
		p.mu.Unlock()
		if err != nil || reaped {
			return reaped, err
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-timer.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func (p *process) reapLocked() (bool, error) {
	if p.terminalState.Load() == 1 {
		return false, ErrTerminalCleanupUnknown
	}
	switch {
	case p.reaped:
		return true, nil
	case p.lost != nil:
		return false, p.lost
	}
	exited, info, err := p.os.tryReap()
	if err != nil {
		p.diagnostics.record(cleanupReapLost, err)
		p.lost = err
		return false, err
	}
	if exited {
		p.reaped, p.reapInfo, p.exitSeen = true, info, true
	}
	return exited, nil
}

// Terminate closes stdin, sweeps and reaps through stopAndReap, then requires
// EOF on both output pipes and no failed stop signal. Descendants that left the
// session/group, and members whose signal delivery cannot be checked
// individually, are outside what this proves.
func (p *process) Terminate(ctx context.Context) error {
	p.term.Lock()
	defer p.term.Unlock()
	if p.terminalState.Load() == 1 {
		return ErrTerminalCleanupUnknown
	}
	if p.terminated {
		return nil
	}
	p.stdinCloseRequested.Store(true)
	if err := p.stdin.close(); err != nil {
		p.diagnostics.record(cleanupStdinClose, err)
	} // Release still returns the original close error
	p.discardOnce.Do(func() { close(p.discard) })
	if err := p.stopAndReap(ctx); err != nil {
		return err
	}
	if !waitClosed(ctx, p.ioDone, p.timing.ioWait) {
		p.diagnostics.record(cleanupIOJoin, nil)
		return errIOUnconfirmed
	}
	p.mu.Lock()
	sigErr, ioErr, groupErr := p.sigErr, p.ioErr, p.groupErr
	ioReady := p.ioStdout == ioNormal && p.ioStderr == ioNormal &&
		(p.ioWrite == ioNormal || p.ioWrite == ioExpectedLocalClose)
	p.mu.Unlock()
	if ioErr != nil || !ioReady {
		p.diagnostics.record(cleanupIOJoin, nil)
		return errors.Join(errIOUnconfirmed, ioErr)
	}
	if sigErr != nil || groupErr != nil {
		return errors.Join(sigErr, groupErr)
	}
	if p.terminalState.Load() == 1 {
		return ErrTerminalCleanupUnknown
	}
	p.terminated = true
	return nil
}

func waitClosed(ctx context.Context, done <-chan struct{}, limit time.Duration) bool {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-ctx.Done():
	case <-timer.C:
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// Release closes this owner's descriptors after a confirmed Terminate. Each is
// closed through ownedCloser; a retryable failure is retried by a later
// Release, an uncertain one keeps cleanup unknown permanently.
func (p *process) Release(context.Context) error {
	p.term.Lock()
	defer p.term.Unlock()
	if p.released {
		return nil
	}
	if p.terminalState.Load() == 1 {
		return ErrTerminalCleanupUnknown
	}
	if !p.terminated {
		p.diagnostics.record(cleanupNotTerminated, nil)
		return errNotTerminated
	}
	inErr, outErr, errErr := p.stdin.close(), p.stdout.close(), p.stderr.close()
	if inErr != nil {
		p.diagnostics.record(cleanupStdinClose, inErr)
	}
	if outErr != nil {
		p.diagnostics.record(cleanupStdoutClose, outErr)
	}
	if errErr != nil {
		p.diagnostics.record(cleanupStderrClose, errErr)
	}
	err := errors.Join(inErr, outErr, errErr)
	for i, c := range p.partialPipes {
		if c == nil {
			continue
		}
		if closeErr := c.close(); closeErr != nil {
			kind := [...]cleanupFailure{cleanupStdinClose, cleanupStdoutClose, cleanupStderrClose}[i]
			p.diagnostics.record(kind, closeErr)
			err = errors.Join(err, closeErr)
		}
	}
	p.mu.Lock()
	observerErr := p.os.releaseObserver()
	if observerErr != nil {
		p.diagnostics.record(cleanupObserverClose, observerErr)
	}
	err = errors.Join(err, observerErr)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if p.terminalState.Load() == 1 {
		return ErrTerminalCleanupUnknown
	}
	if !p.terminalState.CompareAndSwap(0, 2) {
		return ErrTerminalCleanupUnknown
	}
	p.released = true
	p.disposition.forget(p)
	return nil
}
