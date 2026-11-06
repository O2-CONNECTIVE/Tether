package workerowner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrBusy     = errors.New("standby: local attempt already owned")
	ErrRejected = errors.New("standby: fresh local attempt required")
	ErrUnknown  = errors.New("standby: owned cleanup unconfirmed")
)

// Session is the lifecycle seam, not authentication or approval proof.
// The caller must supply its own fresh, explicitly approved session.
type Session interface {
	Run(context.Context) error
	Close(context.Context) error
}

// Worker is single-use. Do not copy it or mutate Session after submission.
type Worker struct {
	Session Session
	claimed atomic.Bool
}

// Request asks the protected local owner for work admission; it is NOT approval.
// It does not mandate a user prompt: a resident admission owner may evaluate
// enrolled policy and authenticated standby state internally. This lifecycle
// port itself never decides that policy. Request is single-use and
// must not be copied or mutated after submission. Callers validate their own
// canonical session lifetime; this owner never extends the absolute deadline.
// Approve must retain any partially acquired resources in its returned worker;
// a nil worker means no resources, except ErrUnknown preserves uncertainty.
type Request struct {
	// SourceContext carries the original producer lifetime, never authentication.
	// Nil preserves existing foreground callers. Native source adapters must set
	// their original finite scope; a host context must not replace that scope.
	SourceContext context.Context
	Deadline      time.Time // orchestration limit only; never a grant's authority/deadline
	Approve       func(context.Context) (*Worker, error)
	Reply         chan error // optional nonblocking local admission observation
	used          atomic.Bool
}

type Snapshot struct {
	Owned   int
	Pending bool
	Unknown bool
	Closed  bool
}
type workerAttempt struct {
	request         *Request // exact immutable local request; never learned from a status view
	cleanupStarted  chan struct{}
	cleanupDeadline time.Time // published once by closing cleanupStarted
	cancel          context.CancelFunc
	worker          *Worker
	pending         bool
	complete        bool
	closeConfirmed  bool  // Close succeeded, or no worker was acquired; Run join is separate.
	closeErr        error // Original returned failure retained privately; never a retry permission.
	uncertain       bool
	result          error
	done            chan struct{}
	runDone         chan struct{}
}

// Owner retains every acquisition, Run and Close goroutine and reference.
// A timeout returns uncertainty, never releases the slot or transfers ownership.
// Callers retain this object after unknown; exiting an owning process requires
// a separate irreversible platform terminal-unknown disposition, never success.
// Standby supplies a nil request source; foreground callers own their sessions.
type Owner struct {
	mu     sync.Mutex
	held   *workerAttempt
	closed bool
	budget time.Duration
	wake   chan struct{}
}

func New(budget time.Duration) *Owner {
	return &Owner{budget: budget, wake: make(chan struct{}, 1)}
}
func (o *Owner) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Request admits one immutable, single-use local request. It is not approval.
func (o *Owner) Request(ctx context.Context, r *Request) error {
	_, err := o.admit(ctx, r)
	return err
}

func (o *Owner) admit(ctx context.Context, r *Request) (*workerAttempt, error) {
	if o == nil || ctx == nil || r == nil || r.Approve == nil || !o.ValidBudget() || !r.used.CompareAndSwap(false, true) {
		return nil, ErrRejected
	}
	now := time.Now()
	if ctx.Err() != nil || !r.Deadline.After(now) {
		return nil, ErrRejected
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refreshLocked()
	if o.closed {
		return nil, ErrRejected
	}
	if o.held != nil {
		return nil, ErrBusy
	}
	life, cancel := requestLifetime(ctx, r)
	if life.Err() != nil {
		cancel()
		return nil, ErrRejected
	}
	a := &workerAttempt{request: r, cancel: cancel, pending: true, done: make(chan struct{}), cleanupStarted: make(chan struct{})}
	o.held = a
	go o.acquire(life, r, a)
	return a, nil
}

func approvalResult(ctx context.Context, r *Request) (w *Worker, err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnknown
		}
	}()
	return r.Approve(ctx)
}
func callWorker(fn func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrUnknown
		}
	}()
	return fn()
}

func (o *Owner) acquire(ctx context.Context, r *Request, a *workerAttempt) {
	w, err := approvalResult(ctx, r)
	o.mu.Lock()
	a.pending = false
	a.result = err
	if w == nil || w.Session == nil || !w.claimed.CompareAndSwap(false, true) {
		if a.result == nil {
			a.result = ErrRejected
		}
		a.uncertain = errors.Is(err, ErrUnknown)
		a.closeConfirmed = !a.uncertain
		a.complete = true
		a.cancel()
		close(a.done)
		o.mu.Unlock()
		o.signal()
		return
	}
	a.worker = w
	a.runDone = make(chan struct{})
	run := err == nil && requestError(ctx, r) == nil
	if !run {
		close(a.runDone)
	}
	o.mu.Unlock()
	if run {
		go func() {
			if requestError(ctx, r) == nil {
				err := callWorker(func() error { return w.Session.Run(ctx) })
				o.mu.Lock()
				a.result = err
				o.mu.Unlock()
			}
			close(a.runDone)
			o.signal()
		}()
		select {
		case <-a.runDone:
		case <-ctx.Done():
		}
	}
	a.cancel()
	cleanup, stop := context.WithTimeout(context.Background(), o.budget)
	defer stop()
	a.cleanupDeadline, _ = cleanup.Deadline()
	close(a.cleanupStarted)
	o.cleanup(cleanup, a)
}

func (o *Owner) cleanup(ctx context.Context, a *workerAttempt) {
	err := callWorker(func() error { return a.worker.Session.Close(ctx) })
	joined := false
	if err == nil {
		select {
		case <-a.runDone:
			joined = true
		case <-ctx.Done():
		}
	}
	o.mu.Lock()
	a.closeConfirmed = err == nil
	if err != nil && a.closeErr == nil {
		a.closeErr = err
	}
	a.uncertain = err != nil || !joined
	a.complete = true
	close(a.done)
	o.mu.Unlock()
	o.signal()
}

// refresh observes completion only. It NEVER repeats Close after an error.
func (o *Owner) refreshLocked() {
	a := o.held
	if a == nil || !a.complete || !a.closeConfirmed {
		return
	}
	if a.runDone != nil {
		select {
		case <-a.runDone:
		default:
			return
		}
	}
	o.held = nil
}
func (o *Owner) Snapshot() Snapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refreshLocked()
	s := Snapshot{Closed: o.closed}
	if a := o.held; a != nil {
		s.Owned = 1
		s.Pending = a.pending
		s.Unknown = a.uncertain
	}
	return s
}

// Stop irreversibly closes admission, cancels the current attempt and observes
// its existing cleanup. Repeated Stop calls do NOT retry a failed Close.
func (o *Owner) Stop(ctx context.Context) error {
	return o.stop(ctx, nil, true)
}

// StopRequest cancels only the exact admitted local request and observes its
// existing cleanup. Confirmed completion permits a fresh, separately approved
// request; it never reuses a session or retries Close. A stale request cannot
// stop a replacement. This is a lifecycle seam, not caller authentication or
// permission to revoke another owner's grant. Native adapters must retain and
// authenticate the backend-owned request mapping, not accept a UI-made Request.
func (o *Owner) StopRequest(ctx context.Context, r *Request) error {
	if r == nil {
		return ErrRejected
	}
	return o.stop(ctx, r, false)
}

func (o *Owner) stop(ctx context.Context, r *Request, seal bool) error {
	if o == nil || ctx == nil {
		return ErrUnknown
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrUnknown
	}
	o.mu.Lock()
	o.refreshLocked()
	a := o.held
	if !seal && (a == nil || a.request != r) {
		o.mu.Unlock()
		return ErrRejected
	}
	if seal {
		o.closed = true
	}
	var done <-chan struct{}
	if a != nil {
		a.cancel()
		done = a.done
	}
	o.mu.Unlock()
	if a == nil {
		return nil
	}
	select {
	case <-done:
	case <-ctx.Done():
		o.mu.Lock()
		o.refreshLocked()
		if o.held == a {
			a.uncertain = true
		}
		o.mu.Unlock()
		o.signal()
		return ErrUnknown
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refreshLocked()
	// StopRequest must observe its own attempt, even if a fresh worker was
	// admitted after confirmed completion and before this waiter reacquired mu.
	if o.held == a {
		return ErrUnknown
	}
	return nil
}

// Wait handles local admission until cancellation. A caller with requests must
// retain this owner after an unknown result; standby supplies nil requests.
func (o *Owner) Wait(ctx context.Context, requests <-chan *Request, observe func(string)) error {
	for {
		if ctx.Err() != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), o.budget)
			defer cancel()
			return o.Stop(cleanup)
		}
		select {
		case <-ctx.Done():
			continue
		case r, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			err := o.Request(ctx, r)
			if r != nil && r.Reply != nil {
				select {
				case r.Reply <- err:
				default:
				}
			}
			if err == nil && observe != nil {
				observe("worker_owned")
			}
		case <-o.wake:
			if ctx.Err() != nil {
				continue
			}
			s := o.Snapshot()
			if observe != nil {
				if s.Owned == 0 {
					observe("standby")
				} else if s.Unknown {
					observe("cleanup_unknown")
				}
			}
		}
	}
}

// ValidBudget reports whether cleanup has a finite supported bound.
func (o *Owner) ValidBudget() bool { return o != nil && o.budget > 0 && o.budget <= time.Minute }

// requestLifetime joins the host/caller scope with the original source scope.
// No identity or approval is inferred. The explicit request deadline remains
// an upper bound; the original source may only shorten it or cancel it.
func requestLifetime(ctx context.Context, r *Request) (context.Context, context.CancelFunc) {
	deadline := r.Deadline
	source := r.SourceContext
	valid := true
	if source != nil {
		original, finite := source.Deadline()
		valid = finite && source.Err() == nil
		if finite && original.Before(deadline) {
			deadline = original
		}
	}
	life, cancel := context.WithDeadline(ctx, deadline)
	if source == nil {
		return life, cancel
	}
	if !valid {
		cancel()
		return life, cancel
	}
	stop := context.AfterFunc(source, cancel)
	if source.Err() != nil {
		cancel()
	}
	return life, func() { stop(); cancel() }
}

// Cancellation callbacks may be scheduled later than the source's Err change.
// Check the original scope as well; this is still an observation boundary, not
// an atomic authorization/revocation barrier for the native Session.
func requestError(ctx context.Context, r *Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.SourceContext != nil {
		return r.SourceContext.Err()
	}
	return nil
}
