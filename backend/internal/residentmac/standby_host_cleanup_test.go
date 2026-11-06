package residentmac

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

// No receiver/authentication, key, native Session, socket or process is forged.
// Hold Accept so the outer Stop cannot mask a missed seal in the SAME cycle's
// actual waitSource entry. Its eventual error models consume's collected result.
type cleanupConnector struct {
	accept chan error
	closes atomic.Int32
	joined chan struct{}
}

func (c *cleanupConnector) Run(ctx context.Context) error {
	defer close(c.joined)
	<-ctx.Done()
	return ctx.Err()
}
func (c *cleanupConnector) Accept(context.Context) (*agenttransport.ResidentSourceLease, error) {
	return nil, <-c.accept
}
func (c *cleanupConnector) Stop()                       {}
func (c *cleanupConnector) Close(context.Context) error { c.closes.Add(1); return nil }
func (c *cleanupConnector) Snapshot() agenttransport.ResidentControlSnapshot {
	return agenttransport.ResidentControlSnapshot{}
}

type cleanupWorker struct {
	started, runJoined, entered, release chan struct{}
	closes                               atomic.Int32
}

func (w *cleanupWorker) Run(ctx context.Context) error {
	close(w.started)
	defer close(w.runJoined)
	<-ctx.Done()
	return ctx.Err()
}
func (w *cleanupWorker) Close(context.Context) error {
	w.closes.Add(1)
	close(w.entered)
	<-w.release
	return nil
}

func TestStandbyHostStopsDuringSourceCleanup(t *testing.T) {
	for _, end := range []string{"disconnect", "eof"} {
		for _, outcome := range []string{"confirmed", "unknown"} {
			t.Run(end+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := newStandby()
					connector := &cleanupConnector{accept: make(chan error, 1), joined: make(chan struct{})}
					var constructions atomic.Int32
					s.makeConnector = func(context.Context) (residentConnector, error) { constructions.Add(1); return connector, nil }
					if r := s.RequestOn(1, s.Snapshot().Revision); r.Disposition != "accepted" {
						t.Fatal(r)
					}
					synctest.Wait()
					original := s.cycle
					owner := original.owner
					source, disconnect := context.WithCancel(context.Background())
					defer disconnect()
					finite, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					w := &cleanupWorker{started: make(chan struct{}), runJoined: make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{})}
					requests := make(chan *workerowner.Request, 1)
					waited := make(chan error, 1)
					go func() { waited <- original.waitSource(source, requests) }()
					request := &workerowner.Request{SourceContext: finite, Deadline: time.Now().Add(time.Minute), Reply: make(chan error, 1), Approve: func(context.Context) (*workerowner.Worker, error) { return &workerowner.Worker{Session: w}, nil }}
					requests <- request
					if e := <-request.Reply; e != nil {
						t.Fatal(e)
					}
					<-w.started
					if end == "disconnect" {
						disconnect()
					} else {
						close(requests)
					}
					<-w.entered
					synctest.Wait()
					closing := make(chan error, 1)
					ctx, stop := context.WithTimeout(context.Background(), standbyCleanup)
					defer stop()
					go func() { closing <- s.Close(ctx) }()
					synctest.Wait()
					if o := s.Snapshot(); !o.AdmissionSealed || o.CanRequestOn || o.Standby != "stopping" {
						t.Error("host stop did not seal UI admission", o)
					}
					if r := s.RequestOn(2, s.Snapshot().Revision); r.Disposition != "rejected" {
						t.Error("replacement accepted while cleanup held", r)
					}
					before := time.Now()
					if outcome == "confirmed" {
						close(w.release)
					}
					err := <-waited
					if outcome == "unknown" {
						if !errors.Is(err, workerowner.ErrUnknown) || time.Since(before) != 10*time.Second {
							t.Error("unknown/budget lost", err, time.Since(before))
						}
						if o := owner.Snapshot(); o.Owned != 1 || !o.Unknown {
							t.Error("original unknown slot lost", o)
						}
					} else if err != nil {
						t.Error(err)
					}
					// Outer Stop is still blocked in Accept, so this cannot be its seal.
					if o := owner.Snapshot(); !o.Closed || w.closes.Load() != 1 {
						t.Error("concrete cycle returned source cleanup with admission open", o, w.closes.Load())
					}
					var approvals atomic.Int32
					next := &workerowner.Request{Deadline: time.Now().Add(time.Minute), Approve: func(context.Context) (*workerowner.Worker, error) { approvals.Add(1); return nil, nil }}
					if e := owner.Request(context.Background(), next); !errors.Is(e, workerowner.ErrRejected) || approvals.Load() != 0 {
						t.Error("stopped same owner admitted fresh work", e, approvals.Load())
					}
					if outcome == "unknown" {
						close(w.release)
					}
					synctest.Wait() // Late physical completion does not reclassify collected unknown.
					if err == nil {
						connector.accept <- context.Canceled
					} else {
						connector.accept <- err
					}
					result := <-closing
					<-original.done
					<-w.runJoined
					<-connector.joined
					o := s.Snapshot()
					if outcome == "unknown" {
						if !errors.Is(result, ErrStandbyUnknown) || o.Standby != "unknown" || o.Cleanup != "unknown" || o.CanRequestOn {
							t.Error("Standby erased collected unknown", result, o)
						}
					} else if result != nil || o.Standby != "off" || o.Cleanup != "confirmed" || o.Owned != 0 {
						t.Error("confirmed original joins absent", result, o)
					}
					if s.cycle != original || original.owner != owner || constructions.Load() != 1 || connector.closes.Load() != 1 || w.closes.Load() != 1 {
						t.Error("original owner replaced or Close retried")
					}
					if held := owner.Snapshot(); !held.Closed || held.Owned != 0 {
						t.Error("same original worker not physically joined", held)
					}
				})
			})
		}
	}
}
