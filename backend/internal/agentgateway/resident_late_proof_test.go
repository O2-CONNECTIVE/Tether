package agentgateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/crypto/ssh"
)

// lateProofRig leaves one fixed work unknown the way a cut connection does:
// the gateway's own cleanup is confirmed, but the Mac's proof never arrived.
func lateProofRig(t *testing.T) (*Gateway, *residentSession, ssh.Signer, string, *residentControlFixture) {
	t.Helper()
	g, s, host, control := residentEvidenceRig(t)
	t.Cleanup(func() {
		close(control.begin)
		ctx, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		_ = g.Close(ctx)
	})
	id := strings.Repeat("1", 32)
	s.FixedStart(context.Background(), id)
	time.Sleep(40 * time.Second) // operation deadline (15s) + proof wait (20s)
	synctest.Wait()
	if v := s.FixedStatus(context.Background(), id); v["phase"] != "unknown" || v["owner_cleanup"] != "unknown" || v["gateway_cleanup"] != "confirmed" {
		t.Fatal("rig did not leave the work unknown for want of the proof", v)
	}
	if next := s.FixedStart(context.Background(), strings.Repeat("2", 32)); next["error"] != "busy_or_refused" {
		t.Fatal("unknown work admitted a replacement", next)
	}
	select {
	case id := <-control.rotated:
		t.Fatal("uncollected generation reported collected", id)
	default:
	}
	return g, s, host, id, control
}

// A cut connection loses a cleanup proof the Mac already holds. Once the same
// signed proof arrives, nothing is unknown any more, so the work must finish and
// the next request must be admitted; otherwise only a gateway restart, which
// drops every member's work, lets this member run again.
func TestResidentLateConfirmedProofFinishesUnknownWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, s, host, id, control := lateProofRig(t)
		proof := signedCollectionFixture(t, g, s, host, id)
		if e := g.ResidentCollected(proof); e != nil {
			t.Fatal("late proof refused", e)
		}
		if v := s.FixedStatus(context.Background(), id); v["phase"] != "finished" || v["owner_cleanup"] != "confirmed" || v["gateway_cleanup"] != "confirmed" {
			t.Fatal("late proof did not finish the work", v)
		}
		if next := s.FixedStart(context.Background(), strings.Repeat("3", 32)); next["error"] == "busy_or_refused" {
			t.Fatal("collected work still blocks the next request", next)
		}
		// Collection frees more than the slot: a control that spent its 32
		// intents rotates only once its last work is reported collected. A
		// control that outlived the wait would otherwise answer control_ended
		// until something else cut it.
		synctest.Wait()
		select {
		case rotated := <-control.rotated:
			if rotated != id {
				t.Fatal("another request reported collected", rotated)
			}
		default:
			t.Fatal("late proof did not report its generation collected")
		}
		if e := g.ResidentCollected(proof); !errors.Is(e, ErrRefused) {
			t.Fatal("late proof replay accepted", e)
		}
	})
}

// A late proof that says the Mac could not confirm its cleanup changes nothing:
// a process may still be running, so the next request stays refused.
func TestResidentLateUnknownProofKeepsWorkUnknown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, s, host, id, _ := lateProofRig(t)
		proof := signedCollectionFixture(t, g, s, host, id)
		proof.Cleanup = "unknown"
		sig, e := host.Sign(nil, ResidentCollectionTranscript(proof))
		if e != nil {
			t.Fatal(e)
		}
		proof.Signature = ssh.Marshal(sig)
		if e := g.ResidentCollected(proof); e != nil {
			t.Fatal("late unknown proof refused", e)
		}
		if v := s.FixedStatus(context.Background(), id); v["phase"] != "unknown" || v["owner_cleanup"] != "unknown" {
			t.Fatal("unconfirmed cleanup finished the work", v)
		}
		if next := s.FixedStart(context.Background(), strings.Repeat("3", 32)); next["error"] != "busy_or_refused" {
			t.Fatal("unconfirmed cleanup admitted a replacement", next)
		}
	})
}
