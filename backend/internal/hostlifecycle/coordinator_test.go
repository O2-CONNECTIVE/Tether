package hostlifecycle

import (
	"context"
	"errors"
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
	return &fakeResource{name: name, rec: r, returned: make(chan struct{})}
}

type fakeResource struct {
	name     string
	rec      *recorder
	closes   atomic.Int32
	err      error
	panics   bool
	hang     chan struct{} // when set, Close ignores ctx until this is closed
	returned chan struct{} // closed when the first Close returns
}

func (f *fakeResource) Close(context.Context) error {
	if f.closes.Add(1) == 1 {
		defer close(f.returned)
	}
	f.rec.mu.Lock()
	f.rec.closed = append(f.rec.closed, f.name)
	f.rec.mu.Unlock()
	if f.panics {
		panic("close panicked")
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
