package guestaccount

import (
	"bytes"
	"context"
	"io"
	"testing"
)

type runtimeInputFixture struct {
	*sidRunInputFixture
	secret           []byte
	entered, release chan struct{}
}

func (w *runtimeInputFixture) WriteSecret(b []byte) (int, error) {
	w.secret = b // retain the caller-owned test buffer, never print its contents
	if w.entered != nil {
		close(w.entered)
		<-w.release
	}
	return len(b), nil
}
func runtimeCredentialFixtureOwner(t *testing.T, r *RuntimeGuest) (*runtimeCredentialExecutor, *runtimeInputFixture) {
	t.Helper()
	o, p, _ := sidRunFixture(t)
	w := &runtimeInputFixture{sidRunInputFixture: p.input}
	p.inputOverride = w
	b := r.plan.binding
	b.sid = b.machine + "-1002"
	p.output = io.NopCloser(bytes.NewReader(sidResult(runtimeCredentialRequest(b))))
	return &runtimeCredentialExecutor{owner: o, admission: func() bool { return !r.stop.Load() }}, w
}
func TestRuntimeGuestUsesDedicatedSecretSegmentAndErasesBinaryCopyAfterWriterReturn(t *testing.T) {
	r, _ := runtimeOwnerFixture(t)
	c, w := runtimeCredentialFixtureOwner(t, r)
	r.ops.credential = func() runtimeCredential { return c }
	if _, err := r.Prepare(testContext(t)); err != nil {
		t.Fatal("binary helper exchange", err)
	}
	if len(w.secret) != 72 || !bytes.HasPrefix(w.data, []byte("RHGC1\n")) {
		t.Fatal("bounded binary secret exchange missing")
	}
	for _, v := range w.secret {
		if v != 0 {
			t.Fatal("binary secret copy retained after writer return")
		}
	}
	if err := r.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeGuestCannotClearOrAcknowledgePendingSecretWriter(t *testing.T) {
	r, _ := runtimeOwnerFixture(t)
	c, w := runtimeCredentialFixtureOwner(t, r)
	w.entered, w.release = make(chan struct{}), make(chan struct{})
	r.ops.credential = func() runtimeCredential { return c }
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() { _, err := r.Prepare(ctx); done <- err }()
	<-w.entered
	cancel()
	if err := <-done; err == nil {
		t.Fatal("pending secret exchange succeeded")
	}
	if err := r.Close(testContext(t)); err == nil {
		t.Fatal("pending secret writer acknowledged closed")
	}
	if r.secret[0] == 0 || len(w.secret) != 72 || w.secret[0] == 0 {
		t.Fatal("pending writer lost owned secret memory")
	}
	close(w.release)
	sidRunConfirmBudget(c.owner)
	if err := r.Close(testContext(t)); err != nil {
		t.Fatal("actual writer return not reclaimed", err)
	}
	for _, v := range w.secret {
		if v != 0 {
			t.Fatal("temporary secret not erased after actual return")
		}
	}
}
