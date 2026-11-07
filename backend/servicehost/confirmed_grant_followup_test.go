package servicehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
)

func TestConfirmedGrantFollowupFailureClosesGuestBeforeWorkloadAndAuthJoin(t *testing.T) {
	for _, kind := range []string{"status-write", "revoke-write", "start", "collect", "consume", "status-write-auth-held"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { testConfirmedGrantFollowupNaturalStop(t, kind) })
		})
	}
}

func testConfirmedGrantFollowupNaturalStop(t *testing.T, kind string) {
	var s *Service
	// Keep the configured observation/cleanup retry cadence. The generic
	// confirmation fixture suppresses periodic queries for an hour, which
	// also prevents recovery from a transient busy issuer observation.
	s, a, outer := configuredConfirmationFixture(t, previewConfigFixture(t), func(_ context.Context, ticket PreviewConfirmationTicket) error {
		_, err := ticket.PreviewFor(s)
		return err
	})
	source := grantRuntimeFixture(t, s)
	guestClosed := make(chan struct{})
	authHeld, releaseAuth := make(chan struct{}), make(chan struct{})
	var authOnce, releaseOnce sync.Once
	unblockAuth := func() { releaseOnce.Do(func() { close(releaseAuth) }) }
	var planeCloses atomic.Int32
	var guestOnce sync.Once
	source.kernel = &runtimeCleanupModel{confirmationRuntimeKernel: source.kernel, close: func(_ context.Context, owner *confirmationEvidenceOwner) error {
		s.mu.Lock()
		confirmed, _ := s.authWriter.WorkloadLocked()
		reservationFinished := s.reservation.finished
		s.mu.Unlock()
		if owner != source.owner || !s.issuer.Snapshot().Closed || !reservationFinished || confirmed {
			t.Error("guest cleanup lost reservation/revocation ordering")
		}
		guestOnce.Do(func() { close(guestClosed) })
		return nil
	}}
	hello := previewExchange(t, a, dispatchHello)
	prepared := previewExchange(t, a, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	approved := previewExchange(t, a, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
	if approved.Approval == nil {
		t.Fatal("grant fixture did not issue")
	}
	s.control.witness.close = func(context.Context) error {
		planeCloses.Add(1)
		select {
		case <-guestClosed:
		default:
			t.Error("Plane closed before guest")
		}
		s.mu.Lock()
		active := s.auth.active
		s.mu.Unlock()
		select {
		case <-active.ownerDone:
		default:
			t.Error("Plane closed before auth worker join")
		}
		return nil
	}
	command := servicecontrol.Status
	if kind == "revoke-write" {
		command = servicecontrol.RevokeAllAndQuiesce
	}
	batch := fixtureAuthBatch(a, dispatchRequest(hello, 3, "44444444444444444444444444444444", command))
	failure := errors.New("private followup model failure")
	switch kind {
	case "start":
		batch.start = func(context.Context) error { return failure }
	case "collect":
		batch.collect = func(context.Context) (*operatorauth.Read, error) { return nil, failure }
	case "consume":
		batch.consume = func(context.Context, *operatorauth.Read) ([]byte, error) { return nil, failure }
	}
	var abort atomic.Bool
	var closes atomic.Int32
	batch.close = func(ctx context.Context) error {
		closes.Add(1)
		// On a RED assertion only, retire the memory worker with a known
		// fatal instead of inventing a workload ack to break the cycle.
		if abort.Load() {
			return operatorauth.ErrFatal
		}
		if err := s.authOwner.AwaitWorkload(ctx); err != nil {
			return err
		}
		if kind == "status-write-auth-held" {
			authOnce.Do(func() { close(authHeld) })
			select {
			case <-releaseAuth:
			case <-ctx.Done():
				return ErrRuntimeUnconfirmed
			}
		}
		return nil
	}
	joined := make(chan struct{})
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		defer close(joined)
		result <- a.exchangeOps(ctx, batch, func(context.Context, []byte) error { return failure })
	}()
	t.Cleanup(func() {
		unblockAuth()
		abort.Store(true)
		cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		_ = s.Close(cleanup)
		select {
		case <-joined:
		case <-cleanup.Done():
			t.Error("followup memory worker did not join")
		}
	})
	select {
	case <-guestClosed:
	case <-ctx.Done():
		t.Fatal("guest cleanup waited on auth worker that awaits whole-workload ack")
	}
	if kind == "status-write-auth-held" {
		select {
		case <-authHeld:
		case <-ctx.Done():
			t.Fatal("auth cleanup did not pass the real workload ack")
		}
		short, end := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := s.Close(short)
		end()
		s.mu.Lock()
		workloadConfirmed, _ := s.authWriter.WorkloadLocked()
		s.mu.Unlock()
		if !errors.Is(err, ErrRuntimeUnconfirmed) || !workloadConfirmed || s.Snapshot().CleanupConfirmed || planeCloses.Load() != 0 || outer.closed.Load() != 0 {
			t.Fatal("workload ack bypassed held auth join", err)
		}
		unblockAuth()
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("failed followup succeeded")
		}
	case <-ctx.Done():
		t.Fatal("whole-workload ack did not release the original auth owner")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if closes.Load() == 0 || !s.Snapshot().CleanupConfirmed || outer.closed.Load() != 1 || s.Snapshot().HasAuthority || !s.issuer.Snapshot().Closed {
		t.Fatal("followup lost actual cleanup ordering")
	}
}
