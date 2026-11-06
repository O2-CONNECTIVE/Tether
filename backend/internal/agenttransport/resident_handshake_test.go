package agenttransport

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func residentRawBegin(t *testing.T, h *Handler) (*rawWS, residentBinding) {
	t.Helper()
	r, response := upgrade(t, h, func(q *http.Request) { q.Header.Set("Sec-WebSocket-Protocol", ResidentSubprotocol) })
	if response.StatusCode != 101 {
		t.Fatal(response.StatusCode)
	}
	nonce := bytes.Repeat([]byte{7}, 32)
	r.text(residentJSON(map[string]string{"type": "resident_hello", "host": hostLabel, "principal": member1, "client_nonce": b64.EncodeToString(nonce)}))
	op, msg, e := r.next(3 * time.Second)
	if e != nil || op != opText {
		t.Fatal("no signed challenge", e)
	}
	m, ok := residentFields(msg, "resident_challenge", "generation", "server_nonce", "signature")
	if !ok {
		t.Fatal("bad challenge")
	}
	server, ok := decodeB64(m["server_nonce"])
	if !ok {
		t.Fatal("bad nonce")
	}
	b := residentBinding{endpoint: testEndpoint, host: hostLabel, principal: member1, generation: m["generation"], clientNonce: nonce, serverNonce: server, hostKey: fixtureSigner(t, 0x11).PublicKey().Marshal(), principalKey: fixtureSigner(t, 0x21).PublicKey().Marshal()}
	sig, ok := residentSignature(m)
	if !ok || verify(fixtureSigner(t, 0x21).PublicKey(), residentTranscript(b, "gateway-challenge"), sig) != nil {
		t.Fatal("unpinned challenge")
	}
	return r, b
}
func residentRawAccept(t *testing.T, r *rawWS, b residentBinding) {
	t.Helper()
	host := fixtureSigner(t, 0x11)
	sig, e := sign(host, host.PublicKey(), residentTranscript(b, "host-proof"))
	if e != nil {
		t.Fatal(e)
	}
	r.text(residentJSON(map[string]string{"type": "resident_proof", "signature": b64.EncodeToString(sig)}))
	op, msg, e := r.next(3 * time.Second)
	m, ok := residentFields(msg, "resident_accepted", "signature")
	got, okSig := residentSignature(m)
	if e != nil || op != opText || !ok || !okSig || verify(fixtureSigner(t, 0x21).PublicKey(), residentTranscript(b, "gateway-accepted"), got) != nil {
		t.Fatal("not mutually accepted", e)
	}
}
func residentRawRefused(t *testing.T, r *rawWS) {
	t.Helper()
	for {
		op, msg, e := r.next(3 * time.Second)
		if e != nil {
			break
		}
		if m, ok := residentFields(msg, "resident_accepted", "signature"); op == opText && ok && m != nil {
			t.Fatal("resident proof accepted")
		}
	}
	select {
	case <-r.served:
	case <-time.After(3 * time.Second):
		t.Fatal("refusal did not return/clean original owner")
	}
}
func TestResidentActualHandshakeRejectsOldGenerationAndWrongRole(t *testing.T) {
	rig := newResidentRig(t, nil)
	first, b1 := residentRawBegin(t, rig.h)
	residentRawAccept(t, first, b1)
	host := fixtureSigner(t, 0x11)
	old, e := sign(host, host.PublicKey(), residentTranscript(b1, "host-proof"))
	if e != nil {
		t.Fatal(e)
	}
	_ = first.conn.Close()
	select {
	case <-first.served:
	case <-time.After(3 * time.Second):
		t.Fatal("old control not cleaned")
	}
	for _, role := range []string{"old-generation", "gateway-challenge"} {
		t.Run(role, func(t *testing.T) {
			r, b2 := residentRawBegin(t, rig.h)
			if b2.generation == b1.generation || bytes.Equal(b2.serverNonce, b1.serverNonce) {
				t.Fatal("connection proof context reused")
			}
			sig := old
			if role != "old-generation" {
				sig, e = sign(host, host.PublicKey(), residentTranscript(b2, role))
				if e != nil {
					t.Fatal(e)
				}
			}
			r.text(residentJSON(map[string]string{"type": "resident_proof", "signature": b64.EncodeToString(sig)}))
			residentRawRefused(t, r)
		})
	}
	if rig.rec.count.Load() != 0 {
		t.Fatal("proof replay reached grant route")
	}
}
func TestResidentSignedWrongIntentEndsWithoutReconnect(t *testing.T) {
	r := newResidentRig(t, nil)
	done := residentRun(r.c)
	residentReady(t, r)
	r.h.mu.Lock()
	p := r.h.residents[residentMapKey(hostLabel, member1)]
	r.h.mu.Unlock()
	i := ResidentFixedIntent{generation: p.binding.generation, sequence: 1, requestID: "00000000000000000000000000000001", deadline: time.Now().Add(10 * time.Second).Truncate(time.Millisecond)}
	values := i.values()
	values[3] = "arbitrary-shell"
	sig, e := sign(p.signer, p.signer.PublicKey(), residentTranscript(p.binding, "gateway-fixed", values...))
	if e != nil {
		t.Fatal(e)
	}
	msg := residentJSON(map[string]string{"type": "resident_fixed", "generation": values[0], "sequence": values[1], "request_id": values[2], "operation": values[3], "deadline_unix_ms": values[4], "timeout_ms": values[5], "signature": b64.EncodeToString(sig)})
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e = residentWrite(ctx, p.ws, msg); e != nil {
		t.Fatal(e)
	}
	e = await(t, done, 3*time.Second)
	if !errors.Is(e, ErrRefused) || r.m.trips.Load() != 1 || r.rec.count.Load() != 0 {
		t.Fatal("signed out-of-scope intent reconnected or granted", e)
	}
}
func TestResidentSnapshotIsRevisionedTransportObservation(t *testing.T) {
	r := newResidentRig(t, nil)
	if s := r.c.Snapshot(); s.Revision != 0 || s.Connected || s.CleanupObserved {
		t.Fatal(s)
	}
	done := residentRun(r.c)
	residentReady(t, r)
	before := r.c.Snapshot()
	if before.Revision == 0 || !residentHex(before.Generation, 32) || !before.Connected || before.RunFinished || before.CleanupObserved {
		t.Fatal(before)
	}
	select {
	case <-r.c.Changes():
	default:
		t.Fatal("no coalesced observation")
	}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e := r.c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	_ = await(t, done, 3*time.Second)
	after := r.c.Snapshot()
	if after.Revision <= before.Revision || after.Generation != before.Generation || after.Connected || !after.RunFinished || !after.CleanupObserved || after.CleanupUnknown {
		t.Fatal(after)
	}
}
func TestResidentActualHandshakeRefusesBinaryOrUnknownHello(t *testing.T) {
	rig := newResidentRig(t, nil)
	for _, binary := range []bool{false, true} {
		r, response := upgrade(t, rig.h, func(q *http.Request) { q.Header.Set("Sec-WebSocket-Protocol", ResidentSubprotocol) })
		if response.StatusCode != 101 {
			t.Fatal(response.StatusCode)
		}
		msg := residentJSON(map[string]string{"type": "resident_hello", "host": hostLabel, "principal": member1, "client_nonce": b64.EncodeToString(bytes.Repeat([]byte{1}, 32)), "approved": "true"})
		if binary {
			if e := r.send(true, opBinary, msg); e != nil {
				t.Fatal(e)
			}
		} else {
			r.text(msg)
		}
		residentRawRefused(t, r)
	}
	if rig.rec.count.Load() != 0 {
		t.Fatal("untrusted hello reached grant route")
	}
}
