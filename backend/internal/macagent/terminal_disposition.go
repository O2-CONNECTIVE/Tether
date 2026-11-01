package macagent

import (
	"errors"
	"sync"
	"sync/atomic"
)

// ErrTerminalCleanupUnknown is an irreversible local disposition, never cleanup
// confirmation. It cannot create a grant or authorize process/group operations.
var ErrTerminalCleanupUnknown = errors.New("macagent: terminal cleanup unknown; owner relinquished")

// TerminalDisposition belongs to the foreground CLI, not to a remote request.
// It retains original references/evidence until the CLI exits. Confirmed released
// processes are forgotten; unknown ones are terminalized without claiming safety.
type TerminalDisposition struct {
	terminal atomic.Bool
	mu       sync.Mutex
	owned    map[*process]struct{}
}

func (d *TerminalDisposition) IsTerminal() bool { return d != nil && d.terminal.Load() }
func (d *TerminalDisposition) retain(p *process) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.owned == nil {
		d.owned = make(map[*process]struct{})
	}
	d.owned[p] = struct{}{}
	closed := d.terminal.Load()
	d.mu.Unlock()
	if closed {
		p.relinquishUnknown(false)
	} // late acquisition, never inspect/signal
}
func (d *TerminalDisposition) forget(p *process) {
	if d == nil {
		return
	}
	d.mu.Lock()
	delete(d.owned, p)
	d.mu.Unlock()
}

// RelinquishUnknown is called only after local authority revocation and transport
// shutdown attempts. allowReap is true only when both were independently sealed.
// It makes no query/signal/close retries and never waits for a live child's exit.
func (d *TerminalDisposition) RelinquishUnknown(allowReap bool) error {
	if d == nil {
		return ErrTerminalCleanupUnknown
	}
	if d.terminal.Swap(true) {
		return ErrTerminalCleanupUnknown
	}
	d.mu.Lock()
	owned := make([]*process, 0, len(d.owned))
	for p := range d.owned {
		owned = append(owned, p)
	}
	d.mu.Unlock()
	for _, p := range owned {
		p.relinquishUnknown(allowReap)
	}
	return ErrTerminalCleanupUnknown
}

type terminalReaper interface {
	// Exactly one nonblocking wait on the original known-exited child; no EINTR
	// loop, observer query, group signal or fallback.
	reapExitedOnce() (bool, exitInfo, error)
}

func (p *process) relinquishUnknown(allowReap bool) {
	if !p.terminalState.CompareAndSwap(0, 1) {
		return
	}
	// Never wait on an operation already in flight. The flag makes its later
	// operations refuse; an already admitted OS call is not falsely "undone".
	if !p.term.TryLock() {
		return
	}
	defer p.term.Unlock()
	if !p.sweep.TryLock() {
		return
	}
	defer p.sweep.Unlock()
	if !p.mu.TryLock() {
		return
	}
	defer p.mu.Unlock()
	if p.released || p.reaped || p.lost != nil || !allowReap || !p.exitSeen || !p.natKnown {
		return
	}
	r, ok := p.os.(terminalReaper)
	if !ok {
		return
	}
	done, info, err := r.reapExitedOnce()
	if err != nil {
		p.lost = err
		p.diagnostics.record(cleanupReapLost, err)
	} else if done {
		p.reaped, p.reapInfo = true, info
	}
	// Original pipes, close errors, signal errors and group observation remain.
	// No Release or successful Terminate is reported by this disposal.
}
