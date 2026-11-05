package maclaunch

import (
	"context"
	"errors"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// ResidentWorker retains the actual issuer/Approved/Client and their partial
// acquisition. It is created only by the protected signed-scope factory.
// No field or public constructor can inject a Runtime, Authority or callback.
type ResidentWorker struct {
	operation                                   context.Context
	clientEntered                               bool
	uncertain                                   bool
	cancel                                      context.CancelFunc
	stopHooks                                   func()
	mu                                          sync.Mutex
	issuer                                      *ephemeral.Issuer
	approved                                    *Approved
	client                                      ResidentClient
	prepared, revoked, started, closing, closed bool
	runDone                                     chan struct{}
	closeErr                                    error
}

func newResidentWorker() *ResidentWorker { return &ResidentWorker{runDone: make(chan struct{})} }

// ResidentWorkerObservation reports the SAME retained capsule's lifecycle.
// RunEntered is transport lifecycle; Starting/Running/Stopping are the actual
// retained Session summary and are valid only when SummaryObserved is true.
type ResidentWorkerObservation struct {
	Prepared, Revoked, RunEntered, RunFinished, CleanupObserved, CleanupUnknown bool
	SummaryObserved                                                             bool
	Starting, Running, Stopping                                                 int
}

func (w *ResidentWorker) Observation() ResidentWorkerObservation {
	if w == nil {
		return ResidentWorkerObservation{CleanupUnknown: true}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	joined := false
	if w.started {
		select {
		case <-w.runDone:
			joined = true
		default:
		}
	}
	v := ResidentWorkerObservation{Prepared: w.prepared, Revoked: w.revoked, RunEntered: w.clientEntered, RunFinished: joined, CleanupObserved: w.closed, CleanupUnknown: w.uncertain || (w.closed && w.closeErr != nil)}
	if w.approved != nil && w.approved.session != nil {
		summary, ok := w.approved.session.LocalSummary()
		v.SummaryObserved, v.Starting, v.Running, v.Stopping = ok, summary.Starting, summary.Running, summary.Stopping
	}
	return v
}
func (w *ResidentWorker) attachIssuer(i *ephemeral.Issuer) {
	w.mu.Lock()
	w.issuer = i
	revoked := w.revoked
	w.mu.Unlock()
	if revoked {
		_ = i.Close()
	}
}
func (w *ResidentWorker) attachApproved(a *Approved) {
	w.mu.Lock()
	w.approved = a
	revoked := w.revoked
	w.mu.Unlock()
	if revoked && a != nil && a.session != nil {
		a.session.Revoke()
	}
}
func (w *ResidentWorker) attachClient(c ResidentClient) { w.mu.Lock(); w.client = c; w.mu.Unlock() }
func (w *ResidentWorker) attachLifetime(operation context.Context, cancel context.CancelFunc, stop func()) {
	w.mu.Lock()
	w.operation, w.cancel, w.stopHooks = operation, cancel, stop
	revoked := w.revoked
	w.mu.Unlock()
	if revoked {
		stop()
		cancel()
	}
}
func (w *ResidentWorker) ready() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.revoked {
		return false
	}
	w.prepared = true
	return true
}
func (w *ResidentWorker) markUncertain(err error) {
	if errors.Is(err, ErrResidentUnknown) {
		w.mu.Lock()
		w.uncertain = true
		w.mu.Unlock()
	}
}
func (w *ResidentWorker) isRevoked() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.revoked }
func (w *ResidentWorker) revoke() {
	w.mu.Lock()
	w.revoked = true
	i, a, cancel, stop := w.issuer, w.approved, w.cancel, w.stopHooks
	w.mu.Unlock()
	if i != nil {
		_ = i.Close()
	}
	if a != nil && a.session != nil {
		a.session.Revoke()
	}
	if stop != nil {
		stop()
	}
	if cancel != nil {
		cancel()
	}
}
func (w *ResidentWorker) Dispatch() agentchannel.Dispatch {
	if w == nil {
		return agentchannel.Dispatch{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.prepared || w.revoked || w.closing || w.closed || w.approved == nil {
		return agentchannel.Dispatch{}
	}
	return w.approved.Dispatch()
}
func (w *ResidentWorker) Run(ctx context.Context) (result error) {
	if w == nil || ctx == nil || ctx.Err() != nil {
		return ErrResident
	}
	w.mu.Lock()
	if !w.prepared || w.revoked || w.closing || w.started || w.client == nil || w.operation == nil || w.operation.Err() != nil {
		w.mu.Unlock()
		return ErrResident
	}
	w.started = true
	c, original := w.client, w.operation
	w.mu.Unlock()
	run, cancel := context.WithCancel(original)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	defer func() {
		if recover() != nil {
			w.markUncertain(ErrResidentUnknown)
			result = ErrResidentUnknown
		}
		close(w.runDone)
	}()
	if ctx.Err() != nil || run.Err() != nil {
		return ErrResident
	}
	w.mu.Lock()
	if w.closing || w.revoked || run.Err() != nil {
		w.mu.Unlock()
		return ErrResident
	}
	w.clientEntered = true
	w.mu.Unlock()
	return c.Run(run)
}
func (w *ResidentWorker) Close(ctx context.Context) error {
	if w == nil {
		return workerowner.ErrUnknown
	}
	w.revoke()
	if !finiteResident(ctx) {
		return workerowner.ErrUnknown
	}
	w.mu.Lock()
	if w.closing {
		err, done := w.closeErr, w.closed
		w.mu.Unlock()
		if !done {
			return workerowner.ErrUnknown
		}
		return err
	}
	w.closing = true
	ran, c, a, i := w.started, w.client, w.approved, w.issuer
	w.mu.Unlock()
	w.revoke()
	var err error
	switch {
	case ran:
		select {
		case <-w.runDone:
		case <-ctx.Done():
			err = workerowner.ErrUnknown
		}
		if err == nil {
			w.mu.Lock()
			entered := w.clientEntered
			w.mu.Unlock()
			if !entered {
				err = c.Close(ctx)
			} else {
				var observed bool
				err, observed = c.CleanupResult()
				if !observed {
					err = workerowner.ErrUnknown
				}
			}
		}
	case c != nil:
		err = c.Close(ctx)
	case a != nil:
		if seen, observed := a.CleanupResult(); observed {
			err = seen
		} else {
			err = a.Close(ctx)
		}
	case i != nil:
		err = i.Close()
	}
	w.mu.Lock()
	if w.uncertain {
		err = errors.Join(err, workerowner.ErrUnknown)
	}
	w.closed = true
	w.closeErr = err
	w.mu.Unlock()
	return err
}
