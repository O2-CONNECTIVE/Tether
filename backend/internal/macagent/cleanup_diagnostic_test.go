package macagent

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
)

type diagnosticCloser struct{ err error }

func (c diagnosticCloser) Close() error { return c.err }

func TestCleanupDiagnosticKeepsAllCloseCausesAndUnknown(t *testing.T) {
	d := &CleanupDiagnostics{}
	f := &fakeOS{observerCloseErr: syscall.EBADF}
	p := &process{diagnostics: d, terminated: true, os: f,
		stdin:  newOwnedCloser(diagnosticCloser{os.ErrClosed}),
		stdout: newOwnedCloser(diagnosticCloser{syscall.EPERM}),
		stderr: newOwnedCloser(diagnosticCloser{syscall.EINTR})}
	err := p.Release(context.Background())
	if err == nil || p.released || !errors.Is(err, os.ErrClosed) || !errors.Is(err, syscall.EPERM) || !errors.Is(err, syscall.EBADF) {
		t.Fatal("close errors or unknown lost")
	}
	want := []string{"stdin_close", "stdin_close_already_closed", "stdout_close", "stdout_close_permission", "stderr_close", "stderr_close_interrupted", "observer_close", "observer_close_bad_descriptor"}
	if !slices.Equal(d.Failures(), want) {
		t.Fatalf("classes=%v", d.Failures())
	}
	if p.Release(context.Background()) == nil || p.released {
		t.Fatal("uncertain close retry became confirmed")
	}
}

func TestCleanupDiagnosticsAccumulateConcurrentFixedClasses(t *testing.T) {
	d := &CleanupDiagnostics{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.record(cleanupSignalTerm, syscall.EPERM)
			d.record(cleanupSignalKill, syscall.EBADF)
			d.record(cleanupReapLost, syscall.ECHILD)
			_ = d.Failures()
		}()
	}
	wg.Wait()
	for _, s := range []string{"signal_term_permission", "signal_kill_bad_descriptor", "reap_lost_no_child"} {
		if !slices.Contains(d.Failures(), s) {
			t.Fatal("cause lost")
		}
	}
	d.record(cleanupObserverClose, errors.Join(os.ErrClosed, syscall.EBADF, errors.New("private path token")))
	if !slices.Contains(d.Failures(), "observer_close_already_closed") || !slices.Contains(d.Failures(), "observer_close_bad_descriptor") {
		t.Fatal("joined classes lost")
	}
	before := slices.Clone(d.Failures())
	d.record(cleanupFailure(255), errors.New("secret"))
	if !slices.Equal(before, d.Failures()) {
		t.Fatal("unknown class accepted")
	}
	var nilRecord *CleanupDiagnostics
	nilRecord.record(cleanupIOJoin, nil)
	if len(nilRecord.Failures()) != 0 {
		t.Fatal("nil recorder")
	}
}

func TestCleanupDiagnosticDoesNotMakeReleaseBeforeTerminateSafe(t *testing.T) {
	d := &CleanupDiagnostics{}
	p := &process{diagnostics: d}
	if !errors.Is(p.Release(context.Background()), errNotTerminated) || p.released {
		t.Fatal("release bypassed terminate")
	}
	if !slices.Equal(d.Failures(), []string{"not_terminated"}) {
		t.Fatal("missing refusal")
	}
}
