package agenttransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func residentProtocolFixture(t *testing.T) (residentBinding, ResidentFixedIntent, time.Time) {
	t.Helper()
	now := time.UnixMilli(1800000000000)
	b := residentBinding{endpoint: testEndpoint, host: hostLabel, principal: member1, generation: fmt.Sprintf("%064x", 1), clientNonce: bytes.Repeat([]byte{1}, 32), serverNonce: bytes.Repeat([]byte{2}, 32), hostKey: fixtureSigner(t, 0x11).PublicKey().Marshal(), principalKey: fixtureSigner(t, 0x21).PublicKey().Marshal()}
	return b, ResidentFixedIntent{generation: b.generation, sequence: 1, requestID: fmt.Sprintf("%032x", 1), deadline: now.Add(10 * time.Second)}, now
}
func TestResidentFixedIntentRejectsSignedOutOfScopeReplayAndMalformedInput(t *testing.T) {
	b, i, now := residentProtocolFixture(t)
	key := fixtureSigner(t, 0x21)
	msg, e := residentEncodeIntent(b, i, key)
	if e != nil {
		t.Fatal(e)
	}
	got, e := residentDecodeIntent(b, msg, key.PublicKey(), now, 1, map[string]bool{})
	if e != nil || got.RequestID() != i.requestID || got.Operation() != ResidentFixedOperation || !got.Deadline().Equal(i.deadline) {
		t.Fatal(got, e)
	}
	for _, tc := range []struct{ name, key, value string }{
		{"generation", "generation", fmt.Sprintf("%064x", 2)}, {"out-of-order", "sequence", "2"}, {"noncanonical-sequence", "sequence", "01"}, {"zero-sequence", "sequence", "0"}, {"sequence-cap", "sequence", "33"},
		{"request-shape", "request_id", "arbitrary"}, {"expired", "deadline_unix_ms", "1799999999999"}, {"deadline-extension", "deadline_unix_ms", "1800000015001"}, {"noncanonical-time", "deadline_unix_ms", "01800000010000"},
		{"arbitrary-operation", "operation", "run-shell"}, {"timeout-extension", "timeout_ms", "30000"}, {"noncanonical-timeout", "timeout_ms", "015000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]string
			_ = json.Unmarshal(msg, &m)
			m[tc.key] = tc.value
			v := []string{m["generation"], m["sequence"], m["request_id"], m["operation"], m["deadline_unix_ms"], m["timeout_ms"]}
			sig, e := sign(key, key.PublicKey(), residentTranscript(b, "gateway-fixed", v...))
			if e != nil {
				t.Fatal(e)
			}
			m["signature"] = b64.EncodeToString(sig)
			if _, e = residentDecodeIntent(b, residentJSON(m), key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
				t.Fatal("signed invalid scope accepted", e)
			}
		})
	}
	for _, tc := range []struct {
		name string
		msg  []byte
	}{
		{"unknown-approved", append(append([]byte{}, msg[:len(msg)-1]...), []byte(`,"approved":"true"}`)...)},
		{"unknown-command", append(append([]byte{}, msg[:len(msg)-1]...), []byte(`,"command":"arbitrary"}`)...)},
		{"duplicate-field", append(append([]byte{}, msg[:len(msg)-1]...), []byte(`,"timeout_ms":"15000"}`)...)},
		{"null", []byte(`{"type":"resident_fixed","generation":null}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := residentDecodeIntent(b, tc.msg, key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
				t.Fatal("malformed intent accepted", e)
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*residentBinding)
	}{
		{"endpoint", func(b *residentBinding) { b.endpoint = "wss://other.example.invalid/agent/register" }},
		{"host", func(b *residentBinding) { b.host = otherHost }}, {"principal", func(b *residentBinding) { b.principal = member2 }},
		{"old-nonce", func(b *residentBinding) { b.clientNonce = bytes.Repeat([]byte{3}, 32) }}, {"server-nonce", func(b *residentBinding) { b.serverNonce = bytes.Repeat([]byte{4}, 32) }},
		{"other-host-key", func(b *residentBinding) { b.hostKey = fixtureSigner(t, 0x31).PublicKey().Marshal() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := b
			tc.edit(&next)
			if _, e := residentDecodeIntent(next, msg, key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
				t.Fatal("cross-binding replay accepted", e)
			}
		})
	}
	for _, state := range []struct {
		next uint64
		seen map[string]bool
	}{{2, map[string]bool{}}, {1, map[string]bool{i.requestID: true}}} {
		if _, e := residentDecodeIntent(b, msg, key.PublicKey(), now, state.next, state.seen); !errors.Is(e, ErrRefused) {
			t.Fatal("replay accepted")
		}
	}
}
func TestResidentFixedRefusalRequiresExactHostSignatureAndCorrelation(t *testing.T) {
	b, i, _ := residentProtocolFixture(t)
	host := fixtureSigner(t, 0x11)
	msg, e := residentEncodeRefusal(b, i, host)
	if e != nil {
		t.Fatal(e)
	}
	if e = residentVerifyRefusal(b, i, msg, host.PublicKey()); !errors.Is(e, ErrWorkUnconfigured) {
		t.Fatal(e)
	}
	for _, field := range []string{"generation", "sequence", "request_id", "code", "signature"} {
		t.Run(field, func(t *testing.T) {
			var m map[string]string
			_ = json.Unmarshal(msg, &m)
			m[field] = "different"
			if e := residentVerifyRefusal(b, i, residentJSON(m), host.PublicKey()); !errors.Is(e, ErrRefused) {
				t.Fatal("uncorrelated refusal accepted", e)
			}
		})
	}
	if e = residentVerifyRefusal(b, i, msg, fixtureSigner(t, 0x12).PublicKey()); !errors.Is(e, ErrRefused) {
		t.Fatal("wrong host receipt accepted")
	}
}
func TestResidentCanceledBeforeRunNeverDialsAndSealsClient(t *testing.T) {
	r := newResidentRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := r.c.Run(ctx); !errors.Is(e, context.Canceled) || r.m.trips.Load() != 0 {
		t.Fatal("canceled run dialed", e)
	}
	if e := r.c.Run(context.Background()); !errors.Is(e, ErrUsed) {
		t.Fatal("canceled run reused", e)
	}
}
