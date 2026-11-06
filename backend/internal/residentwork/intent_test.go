package residentwork

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func fixture() (Binding, Intent) {
	i := Intent{Version: Version, RequestID: "request1", OperationID: "status", ExpiresUnixMilli: 1793760000000}
	i.SourceNonce[0] = 1
	i.ServerNonce[0] = 2
	i.PolicySHA256[0] = 3
	return Binding{"wss://example.test/register", "host1", "member1"}, i
}
func TestIntentTranscriptIndependentGoldenAndDomainSeparation(t *testing.T) {
	b, i := fixture()
	d, e := IntentTranscript(b, i)
	if e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", sha256.Sum256(d)) != "a73a494d9b8eff2b60681cfc933c735fe3235fff96a18533eaacd701834ab386" {
		t.Fatal("intent transcript changed")
	}
	h, e := HostProofTranscript(b, i.SourceNonce, i.ServerNonce, i.PolicySHA256)
	if e != nil || fmt.Sprintf("%x", sha256.Sum256(h)) != "18155fb39c8b6c559a891645af7746f554bb16b39686759667c92620a71f15e6" {
		t.Fatal("host proof transcript changed", e)
	}
}
func TestIntentFieldsAreBoundedAndSigned(t *testing.T) {
	for _, kind := range []string{"version", "request", "operation", "expiry", "zero-nonce", "same-nonce", "binding"} {
		t.Run(kind, func(t *testing.T) {
			b, i := fixture()
			switch kind {
			case "version":
				i.Version++
			case "request":
				i.RequestID = strings.Repeat("a", 49)
			case "operation":
				i.OperationID = "BAD OP"
			case "expiry":
				i.ExpiresUnixMilli = 0
			case "zero-nonce":
				i.SourceNonce = [32]byte{}
			case "same-nonce":
				i.ServerNonce = i.SourceNonce
			case "binding":
				b.Endpoint = "http://example.test/register"
			}
			if _, e := IntentTranscript(b, i); e == nil {
				t.Fatal("invalid intent accepted")
			}
		})
	}
	b, i := fixture()
	base, _ := IntentTranscript(b, i)
	i.ExpiresUnixMilli++
	other, _ := IntentTranscript(b, i)
	if string(base) == string(other) {
		t.Fatal("expiry not signed")
	}
}
