//go:build darwin && cgo

package residentpipe

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const LoadBudget = 10 * time.Second

// CloseBudget must exceed the concrete owner's entire original shutdown path.
// Frozen Standby API confirms 75s for cooperative source15s+aggregate40s cleanup;
// hung synchronous native I/O is still unknown, never a successful timeout.
const CloseBudget = 75 * time.Second

// run is a package-private composition for a future sealed native launcher.
// It is not a CLI/path override or a normal startup entry. Loading the fixed
// protected policy alone never starts a connection.
func run(protectedPolicy string) error {
	if !filepath.IsAbs(protectedPolicy) {
		return ErrProtocol
	}
	pipes, e := capturePipes()
	if e != nil {
		return e
	}
	defer pipes.close()
	ctx, cancel := context.WithTimeout(context.Background(), LoadBudget)
	owner, e := residentmac.LoadStandby(ctx, protectedPolicy)
	cancel()
	if e != nil {
		if maclaunch.IsStartupPolicyRejected(e) {
			if writeErr := writeStartupRejected(pipes); writeErr != nil {
				return writeErr
			}
			return errStartupRejected
		}
		return e
	}
	e = safeServe(owner, pipes)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), CloseBudget)
	closeErr := closeOnce(owner, closeCtx)
	closeCancel() // exactly once
	terminalErr := writeClosed(pipes, closeErr)
	if closeErr != nil {
		// Unknown is retained in this process indefinitely: no replacement, retry,
		// success exit, kill, or fabricated OFF. Keep the original object reachable.
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runtime.KeepAlive(owner)
		}
	}
	runtime.KeepAlive(owner)
	if errors.Is(e, os.ErrClosed) {
		e = nil
	}
	return errors.Join(e, closeErr, terminalErr)
}
func serve(owner *residentmac.Standby, p *ownedPipes) error {
	var m mapper
	publish := func() error {
		now, e := continuousMS()
		if e != nil {
			return e
		}
		return publishObservation(&m, p, owner.Snapshot(), now)
	}
	if e := publish(); e != nil {
		return e
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(time.Second)
	defer heartbeat.Stop()
	var buffer []byte
	var partialAt *uint64
	for {
		select {
		case <-owner.Changes():
			if e := publish(); e != nil {
				return e
			}
		case <-heartbeat.C:
			if e := publish(); e != nil {
				return e
			}
		case <-poll.C:
			before, e := continuousMS()
			if e != nil {
				return e
			}
			if assemblyExpired(partialAt, before) {
				return ErrProtocol
			}
			intents, e := p.readAvailable(&buffer)
			if e != nil {
				return e
			}
			current, e := continuousMS()
			if e != nil {
				return e
			}
			if assemblyExpired(partialAt, current) {
				return ErrProtocol
			}
			if len(buffer) > 0 {
				if partialAt == nil {
					start := current
					partialAt = &start
				}
			} else {
				partialAt = nil
			}
			for _, r := range intents {
				now, e := continuousMS()
				if e != nil {
					return e
				}
				receipt := m.submit(owner, r, now)
				b, e := Encode(receiptEnvelope{1, "receipt", receipt})
				if e != nil {
					return e
				}
				if e = p.writeLine(b); e != nil {
					return e
				}
				if e = publish(); e != nil {
					return e
				}
			}
		}
	}
}

type closedEnvelope struct {
	ProtocolVersion    uint64 `json:"protocolVersion"`
	Type               string `json:"type"`
	SourceContinuousMS uint64 `json:"sourceContinuousMs"`
	Outcome            string `json:"outcome"`
}

func writeClosed(p *ownedPipes, closeErr error) error {
	now, e := continuousMS()
	if e != nil {
		return e
	}
	outcome := "confirmed"
	if closeErr != nil {
		outcome = "unknown"
	}
	b, e := Encode(closedEnvelope{1, "closed", now, outcome})
	if e != nil {
		return e
	}
	return p.writeLine(b)
}

// Recoverable panics are transport faults, never evidence of cleanup.
func safeServe(owner *residentmac.Standby, p *ownedPipes) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrProtocol
		}
	}()
	return serve(owner, p)
}
func closeOnce(owner *residentmac.Standby, ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = residentmac.ErrStandbyUnknown
		}
	}()
	return owner.Close(ctx)
}

func publishObservation(m *mapper, p *ownedPipes, o residentmac.StandbyObservation, now uint64) error {
	s, e := m.observe(o, now)
	if e != nil {
		return e
	}
	b, e := Encode(s)
	if e != nil {
		return e
	}
	if e = p.writeLine(b); e != nil {
		return e
	}
	if m.poisoned {
		return residentmac.ErrStandbyUnknown
	}
	return nil
}

func assemblyExpired(start *uint64, now uint64) bool {
	return start != nil && (now < *start || now-*start >= 1000)
}

func writeStartupRejected(p *ownedPipes) error {
	now, err := continuousMS()
	if err != nil {
		return err
	}
	b, err := Encode(startupRejectedEnvelope{1, "startupRejected", now, "policy-rejected"})
	if err != nil {
		return err
	}
	return p.writeLine(b) // one atomic attempt; failure never earns exit 2
}
