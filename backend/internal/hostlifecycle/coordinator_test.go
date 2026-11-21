package hostlifecycle

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder keeps the order in which resources were closed.
type recorder struct {
	mu     sync.Mutex
	closed []string
}

func (r *recorder) order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.closed)
}

func (r *recorder) res(name string) *fakeResource {
	return &fakeResource{name: name, rec: r, entered: make(chan struct{}), returned: make(chan struct{})}
}

type fakeResource struct {
	name     string
	rec      *recorder
	closes   atomic.Int32
	err      error
	panics   bool
	exits    bool          // Close ends its goroutine with runtime.Goexit
	hang     chan struct{} // when set, Close ignores ctx until this is closed
	entered  chan struct{} // closed when the first Close begins
	returned chan struct{} // closed when the first Close returns
}

func (f *fakeResource) Close(context.Context) error {
	if f.closes.Add(1) == 1 {
		close(f.entered)
		defer close(f.returned)
	}
	f.rec.mu.Lock()
	f.rec.closed = append(f.rec.closed, f.name)
	f.rec.mu.Unlock()
	if f.panics {
		panic("close panicked")
	}
	if f.exits {
		runtime.Goexit()
	}
	if f.hang != nil {
		<-f.hang
	}
	return f.err
}

func returns(r Resource, err error) func(context.Context) (Resource, error) {
	return func(context.Context) (Resource, error) { return r, err }
}

func startCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for", what)
	}
}

func started(t *testing.T, c *Coordinator, resources ...*fakeResource) *Coordinator {
	t.Helper()
	steps := make([]Step, 0, len(resources))
	for _, r := range resources {
		steps = append(steps, Step{Name: r.name, Acquire: returns(r, nil)})
	}
	if err := c.Start(startCtx(t), steps...); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStopClosesInReverseAcquisitionOrder(t *testing.T) {
	rec := &recorder{}
	a, b, c := rec.res("a"), rec.res("b"), rec.res("c")
	r := started(t, New(time.Second), a, b, c).Stop()
	if !r.Stopped || len(r.NeedsCleanup) != 0 {
		t.Fatal(r)
	}
	if got := rec.order(); !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Fatal("close order", got)
	}
}

// Acceptance 1 (#456): a start that fails midway leaves nothing without an
// owner. What was acquired, even together with the error, is closed by the
// ledger in reverse, and nothing after the failure is acquired.
func TestStartFailureTakesBackPartialAcquisitionInReverse(t *testing.T) {
	rec := &recorder{}
	a, b := rec.res("a"), rec.res("b")
	boom := errors.New("b failed after acquiring")
	thirdAcquired := false
	coord := New(time.Second)
	err := coord.Start(startCtx(t),
		Step{Name: "a", Acquire: returns(a, nil)},
		Step{Name: "b", Acquire: returns(b, boom)},
		Step{Name: "c", Acquire: func(context.Context) (Resource, error) { thirdAcquired = true; return rec.res("c"), nil }})
	if !errors.Is(err, boom) || thirdAcquired {
		t.Fatal("start did not stop at the failure", err, thirdAcquired)
	}
	if got := rec.order(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatal("rollback order", got)
	}
	if r := coord.Stop(); !r.Stopped || len(r.NeedsCleanup) != 0 {
		t.Fatal("confirmed rollback left the ledger dirty", r)
	}
	if a.closes.Load() != 1 || b.closes.Load() != 1 {
		t.Fatal("a resource was closed twice", a.closes.Load(), b.closes.Load())
	}
}

// Acceptance 2 (#456): a release the coordinator cannot confirm stays in the
// ledger and the stop is not a success. The other resources are still closed.
func TestStopKeepsUnconfirmedReleaseAndClosesTheRest(t *testing.T) {
	rec := &recorder{}
	a, b, c := rec.res("a"), rec.res("b"), rec.res("c")
	b.err = errors.New("release unconfirmed")
	r := started(t, New(time.Second), a, b, c).Stop()
	if r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"b"}) {
		t.Fatal(r)
	}
	if got := rec.order(); !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Fatal("close order", got)
	}
}

// New resources are not stacked on top of a release the coordinator could not
// confirm.
func TestStartIsRefusedWhileAReleaseIsUnconfirmed(t *testing.T) {
	rec := &recorder{}
	a := rec.res("a")
	a.err = errors.New("release unconfirmed")
	coord := New(time.Second)
	if err := coord.Start(startCtx(t), Step{Name: "a", Acquire: returns(a, nil)}, Step{Name: "b", Acquire: returns(nil, errors.New("b failed"))}); err == nil {
		t.Fatal("failed start succeeded")
	}
	acquired := false
	err := coord.Start(startCtx(t), Step{Name: "x", Acquire: func(context.Context) (Resource, error) { acquired = true; return nil, nil }})
	if !errors.Is(err, ErrNeedsCleanup) || acquired {
		t.Fatal("start over an unconfirmed release", err, acquired)
	}
	if r := coord.Stop(); r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"a"}) {
		t.Fatal(r)
	}
}

// A Close that ignores its deadline cannot hold the stop open. What was
// acquired before it, which it may depend on, is not closed under it and stays
// as needing cleanup.
func TestHungCloseEndsStopAtBudgetAndKeepsWhatItMayDependOn(t *testing.T) {
	rec := &recorder{}
	a, b := rec.res("a"), rec.res("b")
	b.hang = make(chan struct{})
	defer close(b.hang)
	coord := started(t, New(50*time.Millisecond), a, b)
	begin := time.Now()
	r := coord.Stop()
	if time.Since(begin) > time.Second {
		t.Fatal("stop waited past its budget")
	}
	if r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"b", "a"}) || a.closes.Load() != 0 {
		t.Fatal(r, a.closes.Load())
	}
}

func TestPanickingCloseIsUnconfirmed(t *testing.T) {
	rec := &recorder{}
	a, b := rec.res("a"), rec.res("b")
	b.panics = true
	r := started(t, New(time.Second), a, b).Stop()
	if r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"b"}) || a.closes.Load() != 1 {
		t.Fatal(r, a.closes.Load())
	}
}

// Stop decides once. A repeated or concurrent Stop shares the first result, no
// resource is closed twice, and a release confirmed after the decision does not
// turn it into a success.
func TestStopDecidesOnce(t *testing.T) {
	rec := &recorder{}
	a := rec.res("a")
	a.hang = make(chan struct{})
	coord := started(t, New(50*time.Millisecond), a)
	results := make(chan Result, 2)
	go func() { results <- coord.Stop() }()
	go func() { results <- coord.Stop() }()
	first, second := <-results, <-results
	for _, r := range []Result{first, second} {
		if r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"a"}) {
			t.Fatal(r)
		}
	}
	close(a.hang)
	waitFor(t, a.returned, "the hung close to return")
	if r := coord.Stop(); r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"a"}) {
		t.Fatal("a late confirmation changed the decision", r)
	}
	if a.closes.Load() != 1 {
		t.Fatal("closed more than once", a.closes.Load())
	}
}

// Stop cancels an acquisition in progress and still takes what it returns:
// nothing acquired around the stop is left without an owner.
func TestStopTakesAnAcquisitionInProgress(t *testing.T) {
	rec := &recorder{}
	a := rec.res("a")
	entered := make(chan struct{})
	coord := New(time.Second)
	startErr := make(chan error, 1)
	go func() {
		startErr <- coord.Start(context.Background(), Step{Name: "a", Acquire: func(ctx context.Context) (Resource, error) {
			close(entered)
			<-ctx.Done()
			return a, ctx.Err()
		}})
	}()
	waitFor(t, entered, "the acquisition to start")
	r := coord.Stop()
	if !r.Stopped || a.closes.Load() != 1 {
		t.Fatal("late acquisition was not taken and closed once", r, a.closes.Load())
	}
	if err := <-startErr; err == nil {
		t.Fatal("start succeeded across a stop")
	}
}

// An acquisition that ignores cancellation past the budget is unconfirmed.
func TestStopWithAHungAcquisitionIsUnconfirmed(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	coord := New(50 * time.Millisecond)
	go func() {
		_ = coord.Start(context.Background(), Step{Name: "a", Acquire: func(context.Context) (Resource, error) {
			close(entered)
			<-release
			return nil, nil
		}})
	}()
	waitFor(t, entered, "the acquisition to start")
	if r := coord.Stop(); r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"a"}) {
		t.Fatal(r)
	}
}

// A panic while acquiring may have acquired something: unconfirmed.
func TestPanickingAcquisitionIsUnconfirmed(t *testing.T) {
	coord := New(time.Second)
	err := coord.Start(startCtx(t), Step{Name: "a", Acquire: func(context.Context) (Resource, error) { panic("acquire panicked") }})
	if err == nil {
		t.Fatal("panicking acquisition succeeded")
	}
	if r := coord.Stop(); r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"a"}) {
		t.Fatal(r)
	}
}

func TestStartAfterStopIsRefused(t *testing.T) {
	coord := New(time.Second)
	if r := coord.Stop(); !r.Stopped {
		t.Fatal("empty ledger did not stop", r)
	}
	acquired := false
	err := coord.Start(startCtx(t), Step{Name: "a", Acquire: func(context.Context) (Resource, error) { acquired = true; return nil, nil }})
	if !errors.Is(err, ErrStopped) || acquired {
		t.Fatal(err, acquired)
	}
}

type rejected struct{}

func (rejected) Error() string { return "policy rejected" }

// Start returns the step's own error, not a wrapper. Callers decide by its
// concrete type: the sidecar writes its startup-rejection receipt only for
// maclaunch's rejected-policy type.
func TestStartReturnsTheStepErrorItself(t *testing.T) {
	err := New(time.Second).Start(startCtx(t), Step{Name: "a", Acquire: returns(nil, rejected{})})
	if _, ok := err.(rejected); !ok {
		t.Fatal("step error was wrapped", err)
	}
}

// A start that gives up on a slow acquisition still owns what it returns: the
// rollback waits for it within the budget and closes it before what was
// acquired earlier, which it may depend on.
func TestFailedStartWaitsForItsLateAcquisitionAndClosesInReverse(t *testing.T) {
	rec := &recorder{}
	a, b := rec.res("a"), rec.res("b")
	coord := New(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := coord.Start(ctx,
		Step{Name: "a", Acquire: returns(a, nil)},
		Step{Name: "b", Acquire: func(ctx context.Context) (Resource, error) {
			<-ctx.Done()
			time.Sleep(100 * time.Millisecond) // returns after the start stopped waiting
			return b, nil
		}})
	if err == nil {
		t.Fatal("start succeeded past its context")
	}
	if got := rec.order(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatal("rollback order", got)
	}
	if r := coord.Stop(); !r.Stopped {
		t.Fatal(r)
	}
}

// When the slow acquisition outlasts the budget, the rollback cannot know what
// it holds, so nothing acquired before it is closed under it.
func TestFailedStartPastTheBudgetLeavesEarlierResourcesUnclosed(t *testing.T) {
	rec := &recorder{}
	a := rec.res("a")
	release := make(chan struct{})
	defer close(release)
	coord := New(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- coord.Start(ctx,
			Step{Name: "a", Acquire: returns(a, nil)},
			Step{Name: "b", Acquire: func(context.Context) (Resource, error) { <-release; return nil, nil }})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("start succeeded past its context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("start waited past its context and budget")
	}
	if a.closes.Load() != 0 {
		t.Fatal("closed under an unfinished acquisition")
	}
	if err := coord.Start(startCtx(t), Step{Name: "x", Acquire: returns(nil, nil)}); !errors.Is(err, ErrNeedsCleanup) {
		t.Fatal("start over an unfinished rollback", err)
	}
	if r := coord.Stop(); r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"b", "a"}) {
		t.Fatal(r)
	}
}

// A second start does not queue behind an acquisition in progress, where it
// would ignore its own context: it is refused at once and acquires nothing.
func TestStartIsRefusedWhileAnotherStartIsAcquiring(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	coord := New(time.Second)
	go func() {
		_ = coord.Start(context.Background(), Step{Name: "a", Acquire: func(context.Context) (Resource, error) {
			close(entered)
			<-release
			return nil, nil
		}})
	}()
	waitFor(t, entered, "the first acquisition to start")
	var acquired atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- coord.Start(context.Background(), Step{Name: "b", Acquire: func(context.Context) (Resource, error) { acquired.Store(true); return nil, nil }})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNeedsCleanup) || acquired.Load() {
			t.Fatal(err, acquired.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second start queued behind the first")
	}
}

// A failed start rolls back only what it acquired. What an earlier start
// acquired stays owned until Stop.
func TestFailedStartRollsBackOnlyItsOwnAcquisitions(t *testing.T) {
	rec := &recorder{}
	x, a := rec.res("x"), rec.res("a")
	coord := started(t, New(time.Second), x)
	if err := coord.Start(startCtx(t), Step{Name: "a", Acquire: returns(a, nil)}, Step{Name: "b", Acquire: returns(nil, errors.New("b failed"))}); err == nil {
		t.Fatal("failed start succeeded")
	}
	if got := rec.order(); !slices.Equal(got, []string{"a"}) {
		t.Fatal("rollback reached an earlier start", got)
	}
	if r := coord.Stop(); !r.Stopped || !slices.Equal(rec.order(), []string{"a", "x"}) {
		t.Fatal(r, rec.order())
	}
}

// An acquisition that ends its goroutine without returning (runtime.Goexit)
// reported nothing: unconfirmed, and nothing waits on it forever.
func TestExitingAcquisitionIsUnconfirmed(t *testing.T) {
	coord := New(time.Second)
	done := make(chan error, 1)
	go func() {
		done <- coord.Start(context.Background(), Step{Name: "a", Acquire: func(context.Context) (Resource, error) {
			runtime.Goexit()
			return nil, nil
		}})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("exited acquisition succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("start waited on an exited acquisition")
	}
	if r := coord.Stop(); r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"a"}) {
		t.Fatal(r)
	}
}

// A close that ends its goroutine without returning is unconfirmed at once. It
// is not a hung close, so the stop goes on to close what came before it.
func TestExitingCloseIsUnconfirmed(t *testing.T) {
	rec := &recorder{}
	a, b := rec.res("a"), rec.res("b")
	b.exits = true
	r := started(t, New(time.Second), a, b).Stop()
	if r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"b"}) || a.closes.Load() != 1 {
		t.Fatal(r, a.closes.Load())
	}
}

// A Stop that begins while a failed start is still rolling back decides once,
// within its budget, and nothing is closed after that decision.
func TestStopDuringRollbackDecidesOnceAndClosesNothingAfter(t *testing.T) {
	rec := &recorder{}
	a, b := rec.res("a"), rec.res("b")
	b.hang = make(chan struct{})
	coord := New(100 * time.Millisecond)
	startErr := make(chan error, 1)
	go func() {
		startErr <- coord.Start(context.Background(),
			Step{Name: "a", Acquire: returns(a, nil)},
			Step{Name: "b", Acquire: returns(b, nil)},
			Step{Name: "c", Acquire: returns(nil, errors.New("c failed"))})
	}()
	waitFor(t, b.entered, "the rollback to close b")
	begin := time.Now()
	r := coord.Stop()
	if time.Since(begin) > time.Second {
		t.Fatal("stop waited past its budget")
	}
	if r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"b", "a"}) || a.closes.Load() != 0 {
		t.Fatal(r, a.closes.Load())
	}
	select {
	case <-startErr:
	case <-time.After(2 * time.Second):
		t.Fatal("start did not return after the decision")
	}
	close(b.hang)
	waitFor(t, b.returned, "the hung close to return")
	time.Sleep(50 * time.Millisecond) // room for a wrongly resumed rollback
	if a.closes.Load() != 0 {
		t.Fatal("closed after the decision")
	}
	if r := coord.Stop(); r.Stopped || !slices.Equal(r.NeedsCleanup, []string{"b", "a"}) {
		t.Fatal("decision changed", r)
	}
}
