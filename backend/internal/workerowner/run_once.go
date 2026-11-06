package workerowner

import (
	"context"
	"time"
)

// RunOnce waits for this attempt, including cleanup and Run join. After its
// absolute deadline, cancellation, or cleanup start, it waits at most the
// cleanup budget, without renewing an already-started cleanup deadline.
// ErrUnknown never releases the retained acquisition, worker or Close goroutine;
// callers retain the Owner unless a separate platform terminal-unknown
// disposition irreversibly seals it before process exit. This API cannot do that.
func (o *Owner) RunOnce(ctx context.Context, r *Request) error {
	if ctx == nil || r == nil {
		return ErrRejected
	}
	life, cancel := requestLifetime(ctx, r)
	defer cancel()
	a, err := o.admit(life, r)
	if err != nil {
		return err
	}

	// Both events have their own original bound. Observing cleanup later must
	// not restart its budget; cancellation while acquisition is pending still
	// bounds the wait even if cleanup has not started.
	cleanupStarted, canceled := a.cleanupStarted, life.Done()
	var sourceCanceled <-chan struct{}
	if r.SourceContext != nil {
		sourceCanceled = r.SourceContext.Done()
	}
	var deadline time.Time
	var timer *time.Timer
	var timeout <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		var bound time.Time
		select {
		case <-a.done:
			return o.attemptResult(a, requestError(life, r))
		case <-canceled:
			canceled = nil
			bound = time.Now().Add(o.budget)
		case <-sourceCanceled:
			// The original scope ends even if an AfterFunc callback has not run.
			sourceCanceled = nil
			bound = time.Now().Add(o.budget)
		case <-cleanupStarted:
			cleanupStarted = nil
			bound = a.cleanupDeadline
		case <-timeout:
			return o.attemptResult(a, requestError(life, r))
		}
		if deadline.IsZero() || bound.Before(deadline) {
			deadline = bound
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
	}

}

func (o *Owner) attemptResult(a *workerAttempt, canceled error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refreshLocked()
	if !a.complete || !a.closeConfirmed {
		return o.publishUnknownLocked(a)
	}
	if a.runDone != nil {
		select {
		case <-a.runDone:
		default:
			return o.publishUnknownLocked(a)
		}
	}
	if a.result != nil {
		return a.result
	}
	return canceled
}

// Caller holds mu. Observing an unknown result never transfers ownership or
// marks a replacement: only this same retained attempt may publish uncertainty.
func (o *Owner) publishUnknownLocked(a *workerAttempt) error {
	if a != nil && o.held == a {
		a.uncertain = true
		o.signal()
	}
	return ErrUnknown
}
