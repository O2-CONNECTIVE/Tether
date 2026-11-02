package childjob

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitExitObservesCodeWithoutJobCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{exitCode: 37, emptyFail: true}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	code, err := child.WaitExit(ctx)
	if err != nil || code != 37 {
		t.Fatalf("leader outcome: code=%d error=%v", code, err)
	}
	if !reflect.DeepEqual(api.calls, []string{"wait-process"}) {
		t.Fatalf("observation cleaned or killed: %v", api.calls)
	}
	code, err = child.WaitExit(ctx)
	if err != nil || code != 37 || len(api.calls) != 1 {
		t.Fatalf("cached observation was not shared: code=%d error=%v calls=%v", code, err, api.calls)
	}
	if err := child.Wait(ctx); err != ErrUnconfirmed {
		t.Fatalf("leader exit became cleanup: %v", err)
	}
	api.emptyFail = false
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type exitObservationKernel struct {
	*memoryKernel
	wait func(context.Context) (uint32, error)
}

func (k *exitObservationKernel) waitProcess(ctx context.Context, _ uintptr) (uint32, error) {
	if err := k.record("wait-process"); err != nil {
		return 0, err
	}
	return k.wait(ctx)
}

func TestWaitExitPreservesFailureAndDoesNotInventZeroExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	denied := errors.New("synthetic access denied")
	api := &exitObservationKernel{memoryKernel: &memoryKernel{}, wait: func(context.Context) (uint32, error) { return 99, denied }}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	code, err := child.WaitExit(ctx)
	if code != 0 || !errors.Is(err, denied) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("lost error or invented exit: %d %v", code, err)
	}
	if !reflect.DeepEqual(api.calls, []string{"wait-process"}) {
		t.Fatal(api.calls)
	}
	// A separate explicit observation is not an automatic retry. The error did
	// not cache its accompanying numeric value as an observed exit.
	api.wait = func(context.Context) (uint32, error) { return 41, nil }
	code, err = child.WaitExit(ctx)
	if err != nil || code != 41 {
		t.Fatalf("failed result cached: %d %v", code, err)
	}
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWaitExitCancellationRetainsSuccessfulLateObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &exitObservationKernel{memoryKernel: &memoryKernel{}, wait: func(context.Context) (uint32, error) { cancel(); return 23, nil }}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	code, err := child.WaitExit(ctx)
	if code != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call reported success: %d %v", code, err)
	}
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	code, err = child.WaitExit(fresh)
	if err != nil || code != 23 || !reflect.DeepEqual(api.calls, []string{"wait-process"}) {
		t.Fatalf("late observation lost/requeried: %d %v %v", code, err, api.calls)
	}
	if err := child.Close(fresh); err != nil {
		t.Fatal(err)
	}
}

func TestWaitExitRejectsCanceledContextBeforeCachedObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &memoryKernel{exitCode: 0}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	expired, stop := context.WithCancel(ctx)
	stop()
	if _, err := child.WaitExit(expired); !errors.Is(err, context.Canceled) {
		t.Fatalf("expired observation accepted: %v", err)
	}
	if _, err := child.WaitExit(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal("unbounded observation accepted")
	}
	if _, err := child.WaitExit(nil); !errors.Is(err, ErrConfig) {
		t.Fatal("nil context accepted")
	}
	code, err := child.WaitExit(ctx)
	if err != nil || code != 0 || len(api.calls) != 0 {
		t.Fatalf("closed cached fact reopened handles: %d %v %v", code, err, api.calls)
	}
}

func TestWaitExitConcurrentWaitAndObservationShareOneOSWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		entered, release := make(chan struct{}), make(chan struct{})
		api := &exitObservationKernel{memoryKernel: &memoryKernel{}, wait: func(context.Context) (uint32, error) { close(entered); <-release; return 57, nil }}
		child, err := start(ctx, approvedSpec(), api)
		if err != nil {
			t.Fatal(err)
		}
		api.calls = nil
		results := make(chan error, 3)
		observe := func() {
			code, err := child.WaitExit(ctx)
			if err == nil && code != 57 {
				err = errors.New("wrong code")
			}
			results <- err
		}
		go observe()
		<-entered
		go observe()
		go func() { results <- child.Wait(ctx) }()
		synctest.Wait()
		if !reflect.DeepEqual(api.calls, []string{"wait-process"}) {
			t.Fatalf("concurrent OS operation: %v", api.calls)
		}
		close(release)
		for range 3 {
			if err := <-results; err != nil && !errors.Is(err, ErrExit) {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		counts := map[string]int{}
		for _, call := range api.calls {
			counts[call]++
		}
		if counts["wait-process"] != 1 || counts["wait-empty"] != 1 || counts["close-process"] != 1 || counts["close-job"] != 1 {
			t.Fatal(counts)
		}
		code, err := child.WaitExit(ctx)
		if err != nil || code != 57 {
			t.Fatalf("post-Wait cache: %d %v", code, err)
		}
	})
}

func TestWaitExitQueuedCancellationDoesNotInterfereWithOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		entered, release := make(chan struct{}), make(chan struct{})
		api := &exitObservationKernel{memoryKernel: &memoryKernel{}, wait: func(context.Context) (uint32, error) { close(entered); <-release; return 0, nil }}
		child, err := start(ctx, approvedSpec(), api)
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { _, err := child.WaitExit(ctx); result <- err }()
		<-entered
		queued, expire := context.WithTimeout(ctx, time.Second)
		_, err = child.WaitExit(queued)
		expire()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued wait: %v", err)
		}
		close(release)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if err := child.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPreparedWaitExitAfterRevokeRemainsObservationOnly(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			api := &inspectionMemory{memoryKernel: &memoryKernel{exitCode: 29}}
			prepared, err := prepareWithKernel(ctx, approvedSpec(), api)
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if err := prepared.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepared.Revoke(); err != nil {
				t.Fatal(err)
			}
			api.calls = nil
			code, err := prepared.WaitExit(ctx)
			if err != nil || code != 29 || !reflect.DeepEqual(api.calls, []string{"wait-process"}) {
				t.Fatalf("revoked observation: %d %v %v", code, err, api.calls)
			}
			if err := prepared.Commit(ctx); !errors.Is(err, ErrStart) {
				t.Fatalf("observation restored admission: %v", err)
			}
			if err := prepared.Wait(ctx); !errors.Is(err, ErrConfig) {
				t.Fatalf("legacy revoked Wait changed: %v", err)
			}
			if err := prepared.Close(ctx); err != nil {
				t.Fatal(err)
			}
			api.calls = nil
			code, err = prepared.WaitExit(ctx)
			if err != nil || code != 29 || len(api.calls) != 0 {
				t.Fatalf("closed observation touched OS: %d %v %v", code, err, api.calls)
			}
		})
	}
}

func TestWaitExitWithoutAProcessCannotReportSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var absent *Child
	if _, err := absent.WaitExit(ctx); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	if _, err := (&Child{}).WaitExit(ctx); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	var unprepared *Prepared
	if _, err := unprepared.WaitExit(ctx); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	// No-process ownership after pre-creation failure has no exit observation.
	api := &memoryKernel{}
	child := &Child{api: api, gate: make(chan struct{}, 1)}
	child.gate <- struct{}{}
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	code, err := child.WaitExit(ctx)
	if code != 0 || !errors.Is(err, ErrUnconfirmed) || len(api.calls) != 0 {
		t.Fatalf("absent process became code0: %d %v %v", code, err, api.calls)
	}
}

func TestWaitExitCancellationAndOSErrorRemainSeparate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	denied := errors.New("synthetic identity lost")
	api := &exitObservationKernel{memoryKernel: &memoryKernel{}, wait: func(context.Context) (uint32, error) { cancel(); return 88, denied }}
	child, err := start(ctx, approvedSpec(), api)
	if err != nil {
		t.Fatal(err)
	}
	code, err := child.WaitExit(ctx)
	if code != 0 || !errors.Is(err, context.Canceled) || !errors.Is(err, denied) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("lost cancellation/OS error: %d %v", code, err)
	}
	api.wait = func(context.Context) (uint32, error) { return 88, nil }
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := child.Close(fresh); err != nil {
		t.Fatal(err)
	}
}

func TestWaitExitConcurrentCloseSharesObservationWithoutReopening(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		entered, release := make(chan struct{}), make(chan struct{})
		api := &exitObservationKernel{memoryKernel: &memoryKernel{}, wait: func(context.Context) (uint32, error) { close(entered); <-release; return 9, nil }}
		child, err := start(ctx, approvedSpec(), api)
		if err != nil {
			t.Fatal(err)
		}
		api.calls = nil
		observed, closed := make(chan error, 1), make(chan error, 1)
		go func() { _, err := child.WaitExit(ctx); observed <- err }()
		<-entered
		go func() { closed <- child.Close(ctx) }()
		synctest.Wait()
		if !reflect.DeepEqual(api.calls, []string{"wait-process"}) {
			t.Fatal(api.calls)
		}
		close(release)
		if err := <-observed; err != nil {
			t.Fatal(err)
		}
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, call := range api.calls {
			counts[call]++
		}
		if counts["wait-process"] != 1 || counts["terminate-job"] != 1 || counts["close-process"] != 1 || counts["close-job"] != 1 {
			t.Fatal(counts)
		}
		before := len(api.calls)
		code, err := child.WaitExit(ctx)
		if err != nil || code != 9 || len(api.calls) != before {
			t.Fatalf("closed cache: %d %v", code, err)
		}
	})
}
