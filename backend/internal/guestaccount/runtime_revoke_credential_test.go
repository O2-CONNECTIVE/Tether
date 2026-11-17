package guestaccount

import (
	"context"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

func TestRuntimeGuestRevokeBlocksCredentialHelperAfterPipeReturn(t *testing.T) {
	r, _ := runtimeOwnerFixture(t)
	credential, _ := runtimeCredentialFixtureOwner(t, r)
	entered, release := make(chan struct{}), make(chan struct{})
	open, start := credential.owner.ops.open, credential.owner.ops.start
	started := false
	credential.owner.ops.open = func(ctx context.Context, s childstdio.Setup) (sidRunPipes, error) {
		p, err := open(ctx, s)
		close(entered)
		<-release
		return p, err
	}
	credential.owner.ops.start = func(ctx context.Context, s childjob.Spec) (sidRunChild, error) { started = true; return start(ctx, s) }
	r.ops.credential = func() runtimeCredential { return credential }
	done := make(chan error, 1)
	go func() { _, err := r.Prepare(testContext(t)); done <- err }()
	defer releaseRuntimePhase(release)
	awaitRuntimePhase(t, entered)
	_ = r.Revoke()
	close(release)
	if err := awaitRuntimePhase(t, done); err == nil {
		t.Fatal("revoked helper accepted")
	}
	if started || r.created.sid != "" || r.secret != nil {
		t.Fatal("credential helper or account acquired after Revoke")
	}
	sidRunConfirmBudget(credential.owner)
	if err := r.Close(testContext(t)); err != nil {
		t.Fatal("Revoke disabled pipe cleanup", err)
	}
}
