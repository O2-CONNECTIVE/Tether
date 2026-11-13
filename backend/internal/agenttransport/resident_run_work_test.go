package agenttransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func transcriptDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The Mac proves two things about a piece of work with its host key: that it
// registered the worker, and that it collected it. For a command run both
// proofs must say which command, directory and timeout the Mac acted on, so a
// proof made for one scope can never stand for another, and a fixed-work proof
// can never stand for a command run. Fixed work keeps its exact bytes.
func TestResidentRunWorkProofsBindTheSignedScope(t *testing.T) {
	key := fixtureSigner(t, 7)
	deadline := time.UnixMilli(1900000000000)
	d := fixtureDispatch(key, "launch-1", deadline)
	fixed := agentgateway.ResidentWorkBinding{Host: hostLabel, Principal: member1, Generation: strings.Repeat("a", 64), RequestID: strings.Repeat("1", 32), Sequence: 1, DeadlineMillis: deadline.UnixMilli()}
	nonce := make([]byte, 32)
	nonce[0] = 9
	work := func(b agentgateway.ResidentWorkBinding) string {
		return transcriptDigest(workTranscript(testEndpoint, hostLabel, member1, d, nonce, b))
	}
	collect := func(b agentgateway.ResidentWorkBinding) string {
		return transcriptDigest(agentgateway.ResidentCollectionTranscript(agentgateway.ResidentCollectionProof{Binding: b, Endpoint: testEndpoint, ClientNonce: nonce, ServerNonce: nonce, HostKey: []byte("host"), PrincipalKey: []byte("principal"), Cleanup: "confirmed"}))
	}
	// Recorded from the implementation before command runs existed.
	if got := work(fixed); got != "b5ea7f826598b59d7dc526061566188fb001c097f8759835c9e43b2cf9b32b2d" {
		t.Fatal("fixed work registration proof changed", got)
	}
	if got := collect(fixed); got != "c752af54213eb8b46536c9141a308a69000d28062535716446e17eee3019e7e8" {
		t.Fatal("fixed work collection proof changed", got)
	}
	run := fixed
	run.Run = agentgateway.ResidentRunScope{CommandSHA256: residentRunWireHash, Cwd: "src/app", TimeoutMillis: 60000}
	seen := map[string]string{work(fixed): "fixed work", work(run): "run"}
	seenCollect := map[string]string{collect(fixed): "fixed work", collect(run): "run"}
	if len(seen) != 2 || len(seenCollect) != 2 {
		t.Fatal("a fixed-work proof stands for a command run")
	}
	for name, edit := range map[string]func(*agentgateway.ResidentRunScope){
		"another command":   func(s *agentgateway.ResidentRunScope) { s.CommandSHA256 = strings.Repeat("0", 64) },
		"another directory": func(s *agentgateway.ResidentRunScope) { s.Cwd = "src" },
		"another timeout":   func(s *agentgateway.ResidentRunScope) { s.TimeoutMillis = 60001 },
		// Length prefixes keep a boundary shift from producing the same bytes.
		"shifted boundary": func(s *agentgateway.ResidentRunScope) {
			s.CommandSHA256, s.Cwd = s.CommandSHA256+"s", "rc/app"
		},
	} {
		changed := run
		edit(&changed.Run)
		if other, dup := seen[work(changed)]; dup {
			t.Fatalf("registration proof for %s equals the one for %s", name, other)
		}
		if other, dup := seenCollect[collect(changed)]; dup {
			t.Fatalf("collection proof for %s equals the one for %s", name, other)
		}
		seen[work(changed)], seenCollect[collect(changed)] = name, name
	}
	// The message keeps the fixed-work fields: the gateway takes the scope from
	// the intent it signed, never from what the Mac sends back.
	sig, e := sign(key, key.PublicKey(), workTranscript(testEndpoint, hostLabel, member1, d, nonce, run))
	if e != nil {
		t.Fatal(e)
	}
	r, e := decodeWorkRegister(encodeWorkRegister(hostLabel, member1, d, sig, run))
	if e != nil || r.resident == nil || r.resident.Run != (agentgateway.ResidentRunScope{}) {
		t.Fatal("registration message carried a scope of its own", e)
	}
	if verify(key.PublicKey(), workTranscript(testEndpoint, r.host, r.principal, r.dispatch, nonce, *r.resident), r.signature) == nil {
		t.Fatal("run proof verified without the signed scope")
	}
	r.resident.Run = run.Run
	if verify(key.PublicKey(), workTranscript(testEndpoint, r.host, r.principal, r.dispatch, nonce, *r.resident), r.signature) != nil {
		t.Fatal("run proof did not verify against the signed scope")
	}
}

// When a worker registers, the gateway must check the Mac's proof against the
// scope the gateway itself signed for that request, looked up by request id.
func TestGatewayTakesRunWorkScopeFromItsOwnIntent(t *testing.T) {
	r := connectorRig(t)
	done := residentRun(r.c)
	s := takeSource(t, r)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	result := make(chan error, 2)
	runID, fixedID := fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2)
	go func() {
		result <- r.h.ProposeResidentRun(ctx, hostLabel, member1, runID, residentRunWireDeadline(), residentRunWireHash, "src/app", 60000)
	}()
	i, e := s.Next()
	if e != nil {
		t.Fatal(e)
	}
	claimed := i.workBinding()
	if claimed.Run != (agentgateway.ResidentRunScope{CommandSHA256: residentRunWireHash, Cwd: "src/app", TimeoutMillis: 60000}) {
		t.Fatal("the Mac's work binding lost the signed scope", claimed.Run)
	}
	claimed.Run = agentgateway.ResidentRunScope{} // what a registration message carries
	current, ok := r.h.currentWork(claimed)
	if !ok || current != i.workBinding() {
		t.Fatal("gateway did not restore the scope it signed", ok, current.Run)
	}
	claimed.Run = agentgateway.ResidentRunScope{CommandSHA256: strings.Repeat("0", 64), Cwd: "elsewhere", TimeoutMillis: 1}
	if current, ok = r.h.currentWork(claimed); !ok || current != i.workBinding() {
		t.Fatal("gateway trusted a scope supplied by the caller", ok, current.Run)
	}
	wrong := i.workBinding()
	wrong.Sequence++
	if _, ok = r.h.currentWork(wrong); ok {
		t.Fatal("stale sequence accepted as current work")
	}
	if e = s.Refuse(i); e != nil {
		t.Fatal(e)
	}
	_ = await(t, result, 3*time.Second)
	go func() {
		result <- r.h.ProposeResidentFixed(ctx, hostLabel, member1, fixedID, residentRunWireDeadline())
	}()
	if i, e = s.Next(); e != nil {
		t.Fatal(e)
	}
	if current, ok = r.h.currentWork(i.workBinding()); !ok || current.Run != (agentgateway.ResidentRunScope{}) {
		t.Fatal("fixed work gained a run scope", ok, current.Run)
	}
	if e = s.Refuse(i); e != nil {
		t.Fatal(e)
	}
	_ = await(t, result, 3*time.Second)
	r.c.Stop()
	if e = finishSource(t, s, nil); e != nil {
		t.Fatal(e)
	}
	if e = await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
