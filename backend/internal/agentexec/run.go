package agentexec

import (
	"context"
	"errors"
	"io"
)

var errRuntimePanic = errors.New("agentexec: guest runtime panicked")

func guard(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errRuntimePanic
		}
	}()
	return f()
}

// run is the single worker of one command. It keeps a partially or late
// returned process, stops it after any stop request, and always terminates the
// whole guest workload before Release so descendants cannot outlive the shell.
func (s *Session) run(c *command) {
	defer close(c.done)
	if s.beforeStart != nil {
		s.beforeStart()
	}
	// Recheck the canonical grant and this command's stop under the Session lock
	// immediately before the runtime call. This only orders local admission: it
	// cannot make an OS start atomic with a later revoke, which instead cancels
	// ctx and leaves any late result owned below. Native code must revalidate.
	s.mu.Lock()
	live, reason := s.liveLocked()
	if !live && c.stop == StopNone {
		c.stop = reason
	}
	if c.stop != StopNone || c.ctx.Err() != nil {
		if c.stop == StopNone {
			c.stop = StopRevoked
		}
		c.phase, c.cleanup, c.stdin = PhaseCanceled, CleanupNotRequired, nil
		s.finishLocked(c)
		s.mu.Unlock()
		if !live {
			s.revoke(reason)
		}
		return
	}
	stdin := c.stdin
	s.mu.Unlock()

	var proc GuestProcess
	err := guard(func() (err error) {
		proc, err = s.runtime.StartShell(c.ctx, s.binding, stdin)
		return err
	})
	s.mu.Lock()
	c.stdin = nil
	if proc == nil {
		if err != nil && errors.Is(err, ErrNotStarted) {
			c.phase, c.cleanup = PhaseFailed, CleanupNotRequired
		} else {
			// No handle and no proof of absence: never retried or relabeled.
			c.phase, c.cleanup = PhaseStartUnknown, CleanupUnknown
		}
		if c.stop == StopNone {
			c.stop = StopRuntimeFault
		}
		s.finishLocked(c)
		s.mu.Unlock()
		return
	}
	c.proc = proc
	switch {
	case err != nil:
		c.phase = PhaseFailed
		if c.stop == StopNone {
			c.stop = StopRuntimeFault
		}
	case c.stop == StopNone && c.ctx.Err() == nil:
		c.phase = PhaseRunning
	}
	running := c.phase == PhaseRunning
	s.mu.Unlock()

	natural := false
	if running && s.pump(c, proc) {
		if st, werr := waitExit(c.ctx, proc); werr == nil {
			s.mu.Lock()
			// An exit observed after a stop request may be the forced one.
			if c.stop == StopNone {
				c.exit, c.exited, natural = st, true, true
			}
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	c.releasing = true
	s.mu.Unlock()
	c.terms.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), s.cleanupTimeout)
	defer cancel()
	terr := guard(func() error { return proc.Terminate(ctx) })
	rerr := terr
	if terr == nil {
		rerr = guard(func() error { return proc.Release(ctx) })
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	c.terminated = terr == nil
	switch {
	case c.phase == PhaseFailed:
	case natural:
		c.phase = PhaseExited
	case c.stop == StopNone || c.stop == StopRuntimeFault:
		c.phase, c.stop = PhaseFailed, StopRuntimeFault
	default:
		c.phase = PhaseCanceled
	}
	if rerr == nil {
		c.cleanup, c.proc = CleanupConfirmed, nil
	} else {
		c.cleanup = CleanupUnknown
	}
	s.finishLocked(c)
}

func waitExit(ctx context.Context, proc GuestProcess) (st ExitStatus, err error) {
	err = guard(func() (err error) {
		st, err = proc.Wait(ctx)
		return err
	})
	if err != nil {
		return ExitStatus{}, err
	}
	st.Known = true
	return st, nil
}

func (s *Session) finishLocked(c *command) {
	c.terminal = true
	c.timerStop()
	c.cancel()
	c.out.closeLocked()
}

// pump returns true only when both streams reached EOF without a stop. While the
// retained window is full it stops reading, which backpressures the guest pipes,
// but it waits only until the command context ends at cancel, timeout, revoke or
// the hard deadline.
func (s *Session) pump(c *command, proc GuestProcess) bool {
	for {
		var stream Stream
		var data []byte
		err := guard(func() (err error) {
			stream, data, err = proc.Next(c.ctx)
			return err
		})
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil || (stream != Stdout && stream != Stderr) {
			s.mu.Lock()
			if c.stop == StopNone && c.ctx.Err() == nil {
				c.stop = StopRuntimeFault
			}
			s.mu.Unlock()
			return false
		}
		if !s.appendOutput(c, stream, data) {
			return false
		}
	}
}

func (s *Session) appendOutput(c *command, stream Stream, data []byte) bool {
	for {
		s.mu.Lock()
		if c.ctx.Err() != nil {
			s.mu.Unlock()
			return false
		}
		data = c.out.appendLocked(s.limits, stream, data)
		if len(data) == 0 {
			s.mu.Unlock()
			return true
		}
		space := c.out.space
		s.mu.Unlock()
		select {
		case <-space:
		case <-c.ctx.Done():
			return false
		}
	}
}
