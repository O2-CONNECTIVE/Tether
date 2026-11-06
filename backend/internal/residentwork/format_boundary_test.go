package residentwork

import (
	"bytes"
	"strings"
	"testing"
)

func TestEveryIntentTranscriptFieldIsIncluded(t *testing.T) {
	changes := map[string]func(*Binding, *Intent){
		"endpoint":      func(b *Binding, _ *Intent) { b.Endpoint = "wss://other.test/register" },
		"host":          func(b *Binding, _ *Intent) { b.HostLabel = "host2" },
		"principal":     func(b *Binding, _ *Intent) { b.PrincipalLabel = "member2" },
		"source nonce":  func(_ *Binding, i *Intent) { i.SourceNonce[0] = 4 },
		"server nonce":  func(_ *Binding, i *Intent) { i.ServerNonce[0] = 4 },
		"policy digest": func(_ *Binding, i *Intent) { i.PolicySHA256[0] = 4 },
		"request":       func(_ *Binding, i *Intent) { i.RequestID = "request2" },
		"operation":     func(_ *Binding, i *Intent) { i.OperationID = "other" },
		"expiry":        func(_ *Binding, i *Intent) { i.ExpiresUnixMilli++ },
	}
	b, i := fixture()
	before, err := IntentTranscript(b, i)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b, i := fixture()
			change(&b, &i)
			after, err := IntentTranscript(b, i)
			if err != nil || bytes.Equal(before, after) {
				t.Fatal("field not bound in encoding", err)
			}
		})
	}
}

func TestTranscriptUsesDomainAndLengthDelimitedFields(t *testing.T) {
	b, i := fixture()
	intent, err := IntentTranscript(b, i)
	if err != nil {
		t.Fatal(err)
	}
	host, err := HostProofTranscript(b, i.SourceNonce, i.ServerNonce, i.PolicySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(intent, []byte("rescue-host.resident-work-intent\x00v1\x00")) ||
		!bytes.HasPrefix(host, []byte("rescue-host.resident-control-host-proof\x00v1\x00")) || bytes.Equal(intent, host) {
		t.Fatal("transcript domains conflated")
	}
	i.RequestID, i.OperationID = "ab", "c"
	left, err := IntentTranscript(b, i)
	if err != nil {
		t.Fatal(err)
	}
	i.RequestID, i.OperationID = "a", "bc"
	right, err := IntentTranscript(b, i)
	if err != nil || bytes.Equal(left, right) {
		t.Fatal("field boundary collision", err)
	}
}

func TestEncodingDoesNotVerifySignatureOrCurrentExpiry(t *testing.T) {
	b, i := fixture()
	i.ExpiresUnixMilli = 1 // positive format value, deliberately not a live grant
	before, err := IntentTranscript(b, i)
	if err != nil {
		t.Fatal("formatter must not invent a current-time authority", err)
	}
	i.Signature = []byte{0, 1, 255} // opaque invalid bytes, no key or credential
	after, err := IntentTranscript(b, i)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("signature became part of its own payload", err)
	}
}

func TestNoncanonicalBindingAndZeroContextAreRefused(t *testing.T) {
	for name, endpoint := range map[string]string{
		"plain": "http://example.test/register", "user": "wss://member@example.test/register",
		"query": "wss://example.test/register?x=1", "empty query": "wss://example.test/register?",
		"fragment": "wss://example.test/register#x", "root": "wss://example.test/",
		"upper host": "wss://EXAMPLE.test/register", "escaped": "wss://example.test/%72egister",
	} {
		t.Run(name, func(t *testing.T) {
			b, i := fixture()
			b.Endpoint = endpoint
			if _, err := IntentTranscript(b, i); err == nil {
				t.Fatal("noncanonical binding accepted")
			}
		})
	}
	for _, part := range []string{"server nonce", "policy digest", "host label", "principal label", "request"} {
		t.Run(part, func(t *testing.T) {
			b, i := fixture()
			switch part {
			case "server nonce":
				i.ServerNonce = [32]byte{}
			case "policy digest":
				i.PolicySHA256 = [32]byte{}
			case "host label":
				b.HostLabel = "host_1"
			case "principal label":
				b.PrincipalLabel = strings.Repeat("p", 33)
			case "request":
				i.RequestID = "has space"
			}
			if _, err := IntentTranscript(b, i); err == nil {
				t.Fatal("invalid context accepted")
			}
		})
	}
}
