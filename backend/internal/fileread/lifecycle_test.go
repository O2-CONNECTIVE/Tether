package fileread

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestReadOwnedObservationFailures(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		for _, tc := range []struct {
			name   string
			mutate func(*fakeHandle)
			want   error
		}{
			{"identity", func(h *fakeHandle) { h.state.identity[0]++ }, errChanged},
			{"version", func(h *fakeHandle) { h.state.version[0]++ }, errChanged},
			{"size", func(h *fakeHandle) { h.state.size++ }, errChanged},
			{"unknown-kind", func(h *fakeHandle) { h.state.kind = 0 }, errUnsafe},
			{"non-regular", func(h *fakeHandle) { h.state.kind = 2 }, errUnsafe},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				h, req := fixture([]byte("abc"))
				h.inspectHook = func() error {
					if phase == "before" || h.inspections == 2 {
						tc.mutate(h)
					}
					return nil
				}
				out, err := readOwned(deadline(t), h, allow, req)
				assertRefused(t, h, out, err, tc.want)
				if phase == "before" && h.reads != 0 {
					t.Fatal("unsafe target read")
				}
			})
		}
		t.Run(phase+"/inspection-error", func(t *testing.T) {
			h, req := fixture([]byte("abc"))
			h.inspectHook = func() error {
				if phase == "before" || h.inspections == 2 {
					return errors.New("private detail")
				}
				return nil
			}
			out, err := readOwned(deadline(t), h, allow, req)
			assertRefused(t, h, out, err, errInspect)
		})
	}
}

func TestReadOwnedRevocationAndCancellation(t *testing.T) {
	for _, phase := range []string{"before", "inspect", "partial-read", "final-read", "close"} {
		for _, cancelContext := range []bool{false, true} {
			t.Run(phase+map[bool]string{true: "/cancel", false: "/revoke"}[cancelContext], func(t *testing.T) {
				h, req := fixture([]byte("abc"))
				ctx, cancel := context.WithCancel(deadline(t))
				defer cancel()
				revoked := false
				stop := func() {
					if cancelContext {
						cancel()
					} else {
						revoked = true
					}
				}
				auth := func(context.Context, request) error {
					if revoked {
						return errors.New("private detail")
					}
					return nil
				}
				switch phase {
				case "before":
					stop()
				case "inspect":
					h.inspectHook = func() error { stop(); return nil }
				case "partial-read", "final-read":
					h.readHook = func(p []byte, _ int64) (int, error) {
						stop()
						if phase == "partial-read" {
							p = p[:1]
						}
						return copy(p, []byte("abc")), nil
					}
				case "close":
					h.closeHook = func() error { stop(); return nil }
				}
				out, err := readOwned(ctx, h, auth, req)
				want := errDenied
				if cancelContext {
					want = context.Canceled
				}
				assertRefused(t, h, out, err, want)
				if (phase == "before" || phase == "inspect") && h.reads != 0 {
					t.Fatal("read after observed stop")
				}
				if phase == "partial-read" && h.reads != 1 {
					t.Fatal("continued reading after observed stop")
				}
			})
		}
	}
}

func TestReadOwnedAuthorizationFailureAtEveryCheckpoint(t *testing.T) {
	for failAt := 1; failAt <= 5; failAt++ {
		h, req := fixture([]byte("abc"))
		calls := 0
		auth := func(context.Context, request) error {
			calls++
			if calls == failAt {
				return errors.New("private detail")
			}
			return nil
		}
		out, err := readOwned(deadline(t), h, auth, req)
		assertRefused(t, h, out, err, errDenied)
		if failAt <= 2 && h.reads != 0 {
			t.Fatal("read before authorization")
		}
	}
}

func TestReadOwnedCleanupFailureWithholdsPayloadAndPreservesFault(t *testing.T) {
	for _, deny := range []bool{false, true} {
		h, req := fixture([]byte("abc"))
		h.closeHook = func() error { return errors.New("private close detail") }
		auth := allow
		if deny {
			auth = func(context.Context, request) error { return errors.New("private auth detail") }
		}
		out, err := readOwned(deadline(t), h, auth, req)
		assertRefused(t, h, out, err, errCleanup)
		if deny && !errors.Is(err, errDenied) {
			t.Fatal("cleanup uncertainty hid authorization failure")
		}
	}
}

func TestReadOwnedDetachedPayloadAndSingleLifetime(t *testing.T) {
	h, req := fixture([]byte("abc"))
	closed := false
	h.closeHook = func() error {
		closed = true
		clear(h.data)
		for _, p := range h.buffers {
			clear(p)
		}
		return nil
	}
	auth := func(context.Context, request) error { return nil }
	out, err := readOwned(deadline(t), h, auth, req)
	if err != nil || !closed || h.closes != 1 || !bytes.Equal(out.data, []byte("abc")) {
		t.Fatalf("result=%+v err=%v closes=%d", out, err, h.closes)
	}
	out.data[0] = 9
	if !bytes.Equal(h.data, []byte{0, 0, 0}) {
		t.Fatal("returned bytes alias handle data")
	}
	for _, p := range h.buffers {
		if !bytes.Equal(p, make([]byte, len(p))) {
			t.Fatal("returned bytes alias scratch")
		}
	}
}

func TestReadOwnedInvalidDependenciesStillReleaseOwnership(t *testing.T) {
	for _, which := range []string{"nil-context", "no-deadline", "nil-authorizer"} {
		t.Run(which, func(t *testing.T) {
			h, req := fixture([]byte("abc"))
			ctx := deadline(t)
			auth := allow
			switch which {
			case "nil-context":
				ctx = nil
			case "no-deadline":
				ctx = context.Background()
			case "nil-authorizer":
				auth = nil
			}
			out, err := readOwned(ctx, h, auth, req)
			assertRefused(t, h, out, err, errInvalid)
			if h.reads != 0 || h.inspections != 0 {
				t.Fatal("invalid configuration touched target")
			}
		})
	}
	_, req := fixture(nil)
	out, err := readOwned(deadline(t), nil, allow, req)
	if err != errInvalid || out.data != nil {
		t.Fatal("nil handle accepted")
	}
}

func TestReadOwnedExpiredDeadlineReleasesWithoutRead(t *testing.T) {
	h, req := fixture([]byte("abc"))
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	out, err := readOwned(ctx, h, allow, req)
	assertRefused(t, h, out, err, context.DeadlineExceeded)
	if h.reads != 0 {
		t.Fatal("expired operation read target")
	}
}

func TestReadOwnedDoesNotClaimAtomicSnapshot(t *testing.T) {
	h, req := fixture([]byte("abc"))
	h.readHook = func(p []byte, _ int64) (int, error) {
		// A mutation restored before the next inspection is unobservable to
		// this seam. Passing fake stamps are NOT proof of atomic read bytes.
		copy(p, []byte("xyz"))
		return len(p), nil
	}
	out, err := readOwned(deadline(t), h, allow, req)
	if err != nil || !bytes.Equal(out.data, []byte("xyz")) {
		t.Fatalf("result=%+v err=%v", out, err)
	}
}

func TestReadOwnedCancellationWaitsForCooperativeAdapter(t *testing.T) {
	h, req := fixture([]byte("abc"))
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(deadline(t))
	defer cancel()
	h.readHook = func(p []byte, _ int64) (int, error) {
		close(entered)
		<-release
		return copy(p, []byte("abc")), nil
	}
	type completion struct {
		out result
		err error
	}
	done := make(chan completion, 1)
	go func() { out, err := readOwned(ctx, h, allow, req); done <- completion{out, err} }()
	<-entered
	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("returned before owned read finished")
	default:
	}
	close(release)
	select {
	case got := <-done:
		assertRefused(t, h, got.out, got.err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cooperating read failed to return")
	}
}
