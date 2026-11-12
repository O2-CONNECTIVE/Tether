package agenttransport

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var residentRunFields = []string{"generation", "sequence", "request_id", "operation", "deadline_unix_ms", "timeout_ms", "command_sha256", "cwd"}

func residentRunFixture(t *testing.T) (residentBinding, ResidentFixedIntent, time.Time) {
	t.Helper()
	b, fixed, now := residentProtocolFixture(t)
	return b, ResidentFixedIntent{generation: b.generation, sequence: 1, requestID: fixed.requestID, deadline: now.Add(10 * time.Second), run: residentRunScope{timeoutMillis: 60000, commandSHA256: fmt.Sprintf("%064x", 0xabc), cwd: "src/app"}}, now
}

// residentRunResigned edits one field and signs the result with the gateway role,
// so a refusal proves the scope check and not merely the signature check.
func residentRunResigned(t *testing.T, b residentBinding, msg []byte, role, field, value string) []byte {
	t.Helper()
	key := fixtureSigner(t, 0x21)
	var m map[string]string
	if e := json.Unmarshal(msg, &m); e != nil {
		t.Fatal(e)
	}
	m[field] = value
	var v []string
	for _, k := range residentRunFields {
		v = append(v, m[k])
	}
	sig, e := sign(key, key.PublicKey(), residentTranscript(b, role, v...))
	if e != nil {
		t.Fatal(e)
	}
	m["signature"] = b64.EncodeToString(sig)
	return residentJSON(m)
}

// The Mac must be able to trust that the command hash, working directory and
// timeout it enforces are the ones the gateway signed: a relay that alters any
// of them after signing must not obtain a valid intent.
func TestResidentRunIntentBindsCommandScopeToGatewaySignature(t *testing.T) {
	b, i, now := residentRunFixture(t)
	key := fixtureSigner(t, 0x21)
	msg, e := residentEncodeRun(b, i, key)
	if e != nil {
		t.Fatal(e)
	}
	got, e := residentDecodeRun(b, msg, key.PublicKey(), now, 1, map[string]bool{})
	if e != nil {
		t.Fatal(e)
	}
	if got.RequestID() != i.requestID || got.Generation() != b.generation || got.Operation() != ResidentRunOperation || !got.Deadline().Equal(i.deadline) || got.TimeoutMillis() != 60000 || got.CommandSHA256() != i.run.commandSHA256 || got.Cwd() != "src/app" {
		t.Fatalf("decoded intent differs from the signed one: %+v", got)
	}
	// The Mac matches these against its own enrollment before admitting work.
	if got.Endpoint() != b.endpoint || got.HostLabel() != b.host || got.PrincipalLabel() != b.principal {
		t.Fatal("decoded intent lost the connection it was received on")
	}
	for _, tc := range []struct{ field, value string }{
		{"command_sha256", fmt.Sprintf("%064x", 0xdef)}, {"cwd", "src/other"}, {"timeout_ms", "240000"},
		{"request_id", fmt.Sprintf("%032x", 2)}, {"deadline_unix_ms", "1800000020000"},
	} {
		t.Run("altered-after-signing/"+tc.field, func(t *testing.T) {
			var m map[string]string
			_ = json.Unmarshal(msg, &m)
			m[tc.field] = tc.value
			if _, e := residentDecodeRun(b, residentJSON(m), key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
				t.Fatal("altered intent accepted", e)
			}
		})
	}
	// A changed sequence that is the one the receiver expects next: only the
	// signature can refuse it, so this fails if sequence leaves the transcript.
	var reordered map[string]string
	_ = json.Unmarshal(msg, &reordered)
	reordered["sequence"] = "2"
	if _, e := residentDecodeRun(b, residentJSON(reordered), key.PublicKey(), now, 2, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("intent moved to another sequence accepted", e)
	}
	other := fixtureSigner(t, 0x31)
	forged, e := residentEncodeRun(b, i, other)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := residentDecodeRun(b, forged, key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("intent signed by another key accepted", e)
	}
}

// A compromised or buggy gateway holds a valid signing key, so the receiver
// bounds every signed value itself instead of trusting the signature alone.
func TestResidentRunIntentRefusesSignedValuesOutsideTheRunScope(t *testing.T) {
	b, i, now := residentRunFixture(t)
	key := fixtureSigner(t, 0x21)
	msg, e := residentEncodeRun(b, i, key)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ name, field, value string }{
		{"generation", "generation", fmt.Sprintf("%064x", 2)}, {"out-of-order", "sequence", "2"}, {"noncanonical-sequence", "sequence", "01"}, {"sequence-cap", "sequence", "33"},
		{"request-shape", "request_id", "arbitrary"}, {"expired", "deadline_unix_ms", "1799999999999"}, {"noncanonical-time", "deadline_unix_ms", "01800000010000"},
		{"deadline-beyond-timeout", "deadline_unix_ms", "1800000060001"},
		{"fixed-operation", "operation", ResidentFixedOperation}, {"arbitrary-operation", "operation", "run-shell"},
		{"zero-timeout", "timeout_ms", "0"}, {"timeout-above-cap", "timeout_ms", "240001"}, {"timeout-overflowing-uint32", "timeout_ms", "4294967297"}, {"noncanonical-timeout", "timeout_ms", "060000"},
		{"timeout-shorter-than-deadline", "timeout_ms", "9999"},
		{"short-hash", "command_sha256", "abc"}, {"uppercase-hash", "command_sha256", strings.Repeat("AB", 32)}, {"empty-hash", "command_sha256", ""},
		{"empty-cwd", "cwd", ""}, {"absolute-cwd", "cwd", "/etc"}, {"parent-cwd", "cwd", ".."}, {"escaping-cwd", "cwd", "../outside"},
		{"unclean-escape", "cwd", "src/../../outside"}, {"unclean-dot", "cwd", "./src"}, {"trailing-slash", "cwd", "src/"}, {"double-slash", "cwd", "src//app"},
		{"control-character", "cwd", "src\napp"}, {"bidi-override", "cwd", "src/\u202etxt.exe"}, {"nul", "cwd", "src\x00app"}, {"oversized-cwd", "cwd", strings.Repeat("a", 1025)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := residentDecodeRun(b, residentRunResigned(t, b, msg, "gateway-run", tc.field, tc.value), key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
				t.Fatal("signed out-of-scope intent accepted", e)
			}
		})
	}
	for _, tc := range []struct{ name, cwd string }{{"work-root", "."}, {"nested", "a/b/c"}, {"dot-prefixed-name", "..hidden/x"}, {"non-ascii", "資料/設計"}} {
		t.Run("accepts/"+tc.name, func(t *testing.T) {
			got, e := residentDecodeRun(b, residentRunResigned(t, b, msg, "gateway-run", "cwd", tc.cwd), key.PublicKey(), now, 1, map[string]bool{})
			if e != nil || got.Cwd() != tc.cwd {
				t.Fatal("canonical relative cwd refused", got.Cwd(), e)
			}
		})
	}
	for _, tc := range []struct {
		name string
		msg  []byte
	}{
		{"unknown-approved", append(append([]byte{}, msg[:len(msg)-1]...), []byte(`,"approved":"true"}`)...)},
		{"unknown-command", append(append([]byte{}, msg[:len(msg)-1]...), []byte(`,"command":"arbitrary"}`)...)},
		{"duplicate-field", append(append([]byte{}, msg[:len(msg)-1]...), []byte(`,"cwd":"src/app"}`)...)},
		{"null", []byte(`{"type":"resident_run","generation":null}`)},
		{"missing-cwd", func() []byte {
			var m map[string]string
			_ = json.Unmarshal(msg, &m)
			delete(m, "cwd")
			return residentJSON(m)
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := residentDecodeRun(b, tc.msg, key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
				t.Fatal("malformed intent accepted", e)
			}
		})
	}
	for _, state := range []struct {
		next uint64
		seen map[string]bool
	}{{2, map[string]bool{}}, {1, map[string]bool{i.requestID: true}}} {
		if _, e := residentDecodeRun(b, msg, key.PublicKey(), now, state.next, state.seen); !errors.Is(e, ErrRefused) {
			t.Fatal("replay accepted")
		}
	}
	// Sequence 33 is in order here, so only the per-connection intent cap refuses it.
	if _, e := residentDecodeRun(b, residentRunResigned(t, b, msg, "gateway-run", "sequence", "33"), key.PublicKey(), now, 33, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("intent beyond the per-connection cap accepted", e)
	}
	// A wire timeout that only equals the signed one after uint32 truncation.
	var wrapped map[string]string
	_ = json.Unmarshal(msg, &wrapped)
	wrapped["timeout_ms"] = "4295027296"
	if _, e := residentDecodeRun(b, residentJSON(wrapped), key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("timeout that wraps to the signed value accepted", e)
	}
	other := b
	other.host = otherHost
	if _, e := residentDecodeRun(other, msg, key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("cross-binding replay accepted", e)
	}
}

// The fixed operation guarantees that the Mac runs one known command. A run
// intent must never satisfy the fixed decoder, and a fixed signature must never
// authorize a command scope.
func TestResidentRunAndFixedIntentsAreNotInterchangeable(t *testing.T) {
	b, i, now := residentRunFixture(t)
	_, fixed, _ := residentProtocolFixture(t)
	key := fixtureSigner(t, 0x21)
	run, e := residentEncodeRun(b, i, key)
	if e != nil {
		t.Fatal(e)
	}
	fixedMsg, e := residentEncodeIntent(b, fixed, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := residentDecodeIntent(b, run, key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("fixed decoder accepted a run intent", e)
	}
	if _, e := residentDecodeRun(b, fixedMsg, key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("run decoder accepted a fixed intent", e)
	}
	// One type correlates both kinds of work, so the fixed-operation admission on
	// the Mac tells them apart by these two values. A run intent must never
	// report the fixed ones, and the gateway must never sign it as fixed.
	got, e := residentDecodeRun(b, run, key.PublicKey(), now, 1, map[string]bool{})
	if e != nil || got.Operation() != ResidentRunOperation || got.TimeoutMillis() != 60000 {
		t.Fatal("run intent reports fixed-operation values", got.Operation(), got.TimeoutMillis(), e)
	}
	if msg, e := residentEncodeIntent(b, got, key); !errors.Is(e, ErrRefused) || msg != nil {
		t.Fatal("run intent signed as the fixed operation", e)
	}
	plain, e := residentDecodeIntent(b, fixedMsg, key.PublicKey(), now, 1, map[string]bool{})
	if e != nil || plain.Operation() != ResidentFixedOperation || plain.TimeoutMillis() != 15000 || plain.CommandSHA256() != "" || plain.Cwd() != "" {
		t.Fatal("fixed intent carries a command scope", e)
	}
	// Same values, but signed under the fixed-operation role.
	if _, e := residentDecodeRun(b, residentRunResigned(t, b, run, "gateway-fixed", "cwd", "src/app"), key.PublicKey(), now, 1, map[string]bool{}); !errors.Is(e, ErrRefused) {
		t.Fatal("run decoder accepted a signature made for the fixed role", e)
	}
}

// The gateway must not put its signature on a scope the receiver would refuse.
func TestResidentRunEncodeRefusesToSignOutOfScopeIntent(t *testing.T) {
	b, i, _ := residentRunFixture(t)
	key := fixtureSigner(t, 0x21)
	for name, edit := range map[string]func(*ResidentFixedIntent){
		"zero-value":        func(i *ResidentFixedIntent) { *i = ResidentFixedIntent{} },
		"zero-timeout":      func(i *ResidentFixedIntent) { i.run.timeoutMillis = 0 },
		"timeout-above-cap": func(i *ResidentFixedIntent) { i.run.timeoutMillis = 240001 },
		"escaping-cwd":      func(i *ResidentFixedIntent) { i.run.cwd = "../outside" },
		"request-shape":     func(i *ResidentFixedIntent) { i.requestID = "arbitrary" },
		"sequence-cap":      func(i *ResidentFixedIntent) { i.sequence = 33 },
		"zero-sequence":     func(i *ResidentFixedIntent) { i.sequence = 0 },
		"other-generation":  func(i *ResidentFixedIntent) { i.generation = fmt.Sprintf("%064x", 2) },
		"zero-deadline":     func(i *ResidentFixedIntent) { i.deadline = time.Time{} },
		"malformed-hash":    func(i *ResidentFixedIntent) { i.run.commandSHA256 = "abc" },
		"oversized-message": func(i *ResidentFixedIntent) { i.run.cwd = strings.Repeat("<", 1024) },
		// json.Marshal would replace the bytes, so the wire value would differ from the signed one.
		"invalid-utf8": func(i *ResidentFixedIntent) { i.run.cwd = "src/\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			next := i
			edit(&next)
			if msg, e := residentEncodeRun(b, next, key); !errors.Is(e, ErrRefused) || msg != nil {
				t.Fatal("out-of-scope intent signed", e)
			}
		})
	}
}
