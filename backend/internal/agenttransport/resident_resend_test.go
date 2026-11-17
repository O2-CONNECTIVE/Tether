package agenttransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

type collectingRegistrar struct {
	*recordingRegistrar
	proofs chan agentgateway.ResidentCollectionProof
}

func (c collectingRegistrar) ResidentCollected(p agentgateway.ResidentCollectionProof) error {
	c.proofs <- p
	return nil
}

// cutBeforeProof accepts one command run on the first connection and cuts that
// connection before the Mac could send its cleanup proof. It returns the proof
// the Mac kept, the request and connection it belongs to, and the source of
// the next authenticated connection.
func cutBeforeProof(t *testing.T, result error) (r *residentRig, proofs chan agentgateway.ResidentCollectionProof, unsent []byte, id, generation string, next *ResidentSourceLease) {
	t.Helper()
	r = connectorRig(t)
	proofs = make(chan agentgateway.ResidentCollectionProof, 1)
	r.h.registrar = collectingRegistrar{r.rec, proofs}
	_ = residentRun(r.c)
	s := takeSource(t, r)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	id = fmt.Sprintf("%032x", 1)
	proposed := make(chan error, 1)
	go func() {
		proposed <- r.h.ProposeResidentRun(ctx, hostLabel, member1, id, residentRunWireDeadline(), residentRunWireHash, "src/app", 60000)
	}()
	i, e := s.Next()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Pending(i.Correlation()); e != nil {
		t.Fatal(e)
	}
	_ = await(t, proposed, 3*time.Second)
	generation = residentGeneration(r.h)
	// Closing the receiver while its reader waits for the next intent closes
	// the connection itself, as standby OFF and a cut link both do.
	reading := make(chan struct{})
	go func() { defer close(reading); _, _ = s.Next() }()
	time.Sleep(50 * time.Millisecond)
	s.Receiver().Close()
	<-reading
	if e = s.Collected(i.WorkBinding(), result); e == nil {
		t.Fatal("rig did not cut the connection before the proof")
	}
	if unsent = s.Unsent(); unsent == nil {
		t.Fatal("the proof that could not be written was not kept")
	}
	_ = finishSource(t, s, nil)
	next = takeSource(t, r)
	if residentGeneration(r.h) == generation {
		t.Fatal("rig did not reconnect")
	}
	return
}

// The proof is lost on the wire, not on the Mac. Sent over the next
// authenticated connection it must reach the gateway still bound to the work
// and connection it was signed for, or the gateway keeps refusing this member
// until it restarts.
func TestResidentUnsentCollectionProofReachesGatewayOverNextConnection(t *testing.T) {
	_, proofs, unsent, id, generation, next := cutBeforeProof(t, nil)
	if e := next.Resend(unsent); e != nil {
		t.Fatal("kept proof could not be sent", e)
	}
	select {
	case p := <-proofs:
		if p.Binding.RequestID != id || p.Binding.Generation != generation || p.Cleanup != "confirmed" {
			t.Fatal("gateway received a proof for other work", p.Binding.RequestID, p.Cleanup)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateway never received the kept proof")
	}
}

// The next connection carries no authority of its own over old work: only the
// exact proof the Mac signed for that work is passed on. An edited one would
// otherwise free a slot whose process may still be running.
func TestResidentEditedUnsentCollectionProofIsRefused(t *testing.T) {
	for _, edit := range []struct{ name, old, new string }{
		{"cleanup upgraded", `"cleanup":"unknown"`, `"cleanup":"confirmed"`},
		{"other request", fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2)},
	} {
		t.Run(edit.name, func(t *testing.T) {
			_, proofs, unsent, _, _, next := cutBeforeProof(t, context.Canceled)
			edited := bytes.Replace(unsent, []byte(edit.old), []byte(edit.new), 1)
			if bytes.Equal(edited, unsent) {
				t.Fatal("edit did not apply")
			}
			_ = next.Resend(edited)
			select {
			case p := <-proofs:
				t.Fatal("gateway received an edited proof", p.Binding.RequestID, p.Cleanup)
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
}

// Collected takes a plain binding, so the Mac's host key must sign only the
// binding this source received. A proof for another scope, sequence or
// deadline would claim cleanup of work the Mac was never asked to run.
func TestResidentCollectedSignsOnlyTheReceivedBinding(t *testing.T) {
	r := connectorRig(t)
	proofs := make(chan agentgateway.ResidentCollectionProof, 1)
	r.h.registrar = collectingRegistrar{r.rec, proofs}
	done := residentRun(r.c)
	s := takeSource(t, r)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	id := fmt.Sprintf("%032x", 1)
	proposed := make(chan error, 1)
	go func() {
		proposed <- r.h.ProposeResidentRun(ctx, hostLabel, member1, id, residentRunWireDeadline(), residentRunWireHash, "src/app", 60000)
	}()
	i, e := s.Next()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Pending(i.Correlation()); e != nil {
		t.Fatal(e)
	}
	_ = await(t, proposed, 3*time.Second)
	received := i.WorkBinding()
	for name, edit := range map[string]func(*agentgateway.ResidentWorkBinding){
		"other cwd":      func(b *agentgateway.ResidentWorkBinding) { b.Run.Cwd = "elsewhere" },
		"fixed scope":    func(b *agentgateway.ResidentWorkBinding) { b.Run = agentgateway.ResidentRunScope{} },
		"other sequence": func(b *agentgateway.ResidentWorkBinding) { b.Sequence++ },
		"other deadline": func(b *agentgateway.ResidentWorkBinding) { b.DeadlineMillis++ },
	} {
		edited := received
		edit(&edited)
		if e = s.Collected(edited, nil); !errors.Is(e, ErrRefused) {
			t.Fatal("signed a binding it did not receive:", name, e)
		}
	}
	// A proof the gateway rejects closes the control, so this also shows that
	// none of the refused bindings was sent.
	if e = s.Collected(received, nil); e != nil {
		t.Fatal("received binding was not collected", e)
	}
	select {
	case p := <-proofs:
		if p.Binding != received || p.Cleanup != "confirmed" {
			t.Fatal("gateway received a proof for other work", p.Binding, p.Cleanup)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateway never received the proof")
	}
	r.c.Stop()
	if e = finishSource(t, s, nil); e != nil {
		t.Fatal(e)
	}
	if e = await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
