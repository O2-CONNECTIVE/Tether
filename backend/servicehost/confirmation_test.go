package servicehost

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

type confirmationOwnerModel struct {
	service *Service
	resolve func(context.Context, PreviewConfirmationTicket) error
	closed  atomic.Int32
}

func (o *confirmationOwnerModel) Finalize(_ context.Context, proof InnerCleanupProof) error {
	if !proof.Confirms(o.service) {
		return ErrRuntimeUnconfirmed
	}
	o.closed.Add(1)
	return nil
}
func (o *confirmationOwnerModel) ResolvePreviewConfirmation(ctx context.Context, ticket PreviewConfirmationTicket) error {
	return o.resolve(ctx, ticket)
}

// The real public constructor and existing private auth/SCM seams are used.
// A model owner resolves display observations only; it supplies no native proof.
func confirmationFixture(t *testing.T, resolve func(context.Context, PreviewConfirmationTicket) error) (*Service, *serviceAuthAdapter, *confirmationOwnerModel) {
	t.Helper()
	c := previewConfigFixture(t)
	c.Budgets.Observe = time.Hour // model-only periodic query suppression
	return configuredConfirmationFixture(t, c, resolve)
}

func configuredConfirmationFixture(t *testing.T, c PreviewServiceConfig, resolve func(context.Context, PreviewConfirmationTicket) error) (*Service, *serviceAuthAdapter, *confirmationOwnerModel) {
	t.Helper()
	c.RequestLimit, c.LaunchLimit = 8, 9
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	o := &confirmationOwnerModel{service: s, resolve: resolve}
	if err := s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil && !fatalServiceThread(err) {
			t.Error(err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("confirmation SCM owner did not join")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for s.Snapshot().State != Running {
		select {
		case <-ctx.Done():
			t.Fatal("confirmation fixture did not become ready")
		case <-time.After(time.Millisecond):
		}
	}
	return s, s.auth, o
}

func confirmationFrame(hello servicecontrol.UntrustedResponse, intent *servicecontrol.UntrustedIntent, sequence uint64, id string) string {
	return fmt.Sprintf(`{"protocol":"rescue-host-service/1","type":"request","bootEpoch":%q,"channelNonce":%q,"sequence":%q,"requestId":%q,"command":"confirm-approval","body":{"intentId":%q,"previewDigest":%q}}`+"\n", hello.BootEpoch, hello.ChannelNonce, fmt.Sprint(sequence), id, intent.IntentID, intent.PreviewDigest)
}

func TestPreviewConfirmationResolvesOnceWithoutAuthorityOrApproval(t *testing.T) {
	var s *Service
	var saved PreviewConfirmationTicket
	var calls atomic.Int32
	s, a, _ := confirmationFixture(t, func(ctx context.Context, ticket PreviewConfirmationTicket) error {
		calls.Add(1)
		saved = ticket
		if _, err := ticket.PreviewFor(&Service{}); err == nil {
			t.Error("foreign Service observed the ticket")
		}
		s.mu.Lock()
		_, busyErr := ticket.PreviewFor(s)
		s.mu.Unlock()
		if busyErr == nil {
			t.Error("contended gate did not refuse ticket observation")
		}
		descriptor, err := ticket.PreviewFor(s)
		if err != nil || !authBudget(ctx) || descriptor.ConfigDigest != s.preview.descriptor.ConfigDigest {
			t.Error("same owner could not observe the current display candidate", err)
		}
		descriptor.AllowedKeyFingerprints[0] = "caller mutation"
		again, err := ticket.PreviewFor(s)
		if err != nil || again.AllowedKeyFingerprints[0] == "caller mutation" {
			t.Error("ticket leaked a borrowed descriptor slice", err)
		}
		return nil
	})
	hello := previewExchange(t, a, dispatchHello)
	prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	response := previewExchange(t, a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
	if response.Error == nil || response.Error.Code != "SERVICE_NOT_READY" || response.Approval != nil || calls.Load() != 1 {
		t.Fatal("confirmation observation became approval or skipped the owner", response)
	}
	if _, err := saved.PreviewFor(s); err == nil {
		t.Fatal("finished confirmation ticket remained usable")
	}
	replay := previewExchange(t, a, confirmationFrame(hello, prepared.Intent, 3, "44444444444444444444444444444444"))
	if replay.Error == nil || replay.Error.Code != "SERVICE_INTENT_CONSUMED" || calls.Load() != 1 {
		t.Fatal("same intent resolved more than once", replay)
	}
	status := previewExchange(t, a, dispatchRequest(hello, 4, "55555555555555555555555555555555", servicecontrol.Status))
	if status.Snapshot == nil || status.Snapshot.Generation != 0 || status.Snapshot.Binding != nil || status.Snapshot.ApprovalRequestID != nil || status.Snapshot.LaunchPhase != "none" {
		t.Fatal("confirmation created an approval phase or binding", status)
	}
	if snapshot := s.Snapshot(); snapshot.Generation != 0 || snapshot.HasAuthority || s.cfg.Provisioning != nil || s.cfg.Launcher != nil || s.ownerIntent != nil || s.reservation != nil {
		t.Fatal("confirmation reached authority or legacy activation", snapshot)
	}
	if _, err := (PreviewConfirmationTicket{}).PreviewFor(s); !errors.Is(err, ErrProvisioningRejected) {
		t.Fatal("zero ticket was accepted", err)
	}
}

func TestPreviewConfirmationRejectsMismatchedAndExpiredCandidatesBeforeResolving(t *testing.T) {
	for _, kind := range []string{"digest", "intent", "expired", "old-intent"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			s, a, _ := confirmationFixture(t, func(context.Context, PreviewConfirmationTicket) error { calls.Add(1); return nil })
			hello := previewExchange(t, a, dispatchHello)
			prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
			candidate := *prepared.Intent
			sequence := uint64(2)
			want := "SERVICE_STALE_CONTEXT"
			switch kind {
			case "digest":
				candidate.PreviewDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "intent":
				candidate.IntentID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "expired":
				// Existing display-only time seam makes expiry deterministic. The
				// authenticated request and resolver keep their actual clocks.
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				s.mu.Lock()
				intent, code := s.preparePreviewIntentLocked(ctx, s.dispatch, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil, time.Now().Add(-2*time.Second))
				s.mu.Unlock()
				if code != "" || intent == nil {
					t.Fatal("expired display fixture was not constructed", code)
				}
				candidate = *intent
				want = "SERVICE_INTENT_EXPIRED"
			case "old-intent":
				_ = previewExchange(t, a, dispatchRequest(hello, 2, "33333333333333333333333333333333", servicecontrol.PrepareIntent))
				sequence = 3
			}
			response := previewExchange(t, a, confirmationFrame(hello, &candidate, sequence, "44444444444444444444444444444444"))
			if response.Error == nil || response.Error.Code != want || calls.Load() != 0 || s.Snapshot().HasAuthority || s.Snapshot().Generation != 0 {
				t.Fatal("stale candidate reached the owner or authority", response, calls.Load())
			}
		})
	}
}
