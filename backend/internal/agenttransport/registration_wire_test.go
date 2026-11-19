package agenttransport

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func legacyRegisterTranscript(resource, host, principal string, d agentgateway.Dispatch, nonce []byte) []byte {
	var out []byte
	field := func(value []byte) {
		out = binary.BigEndian.AppendUint32(out, uint32(len(value)))
		out = append(out, value...)
	}
	b := d.Binding
	field([]byte(transcriptDomain))
	field([]byte(resource))
	field([]byte(host))
	field([]byte(principal))
	field(binary.BigEndian.AppendUint64(nil, b.Grant.Generation))
	field([]byte(b.Grant.Identity.LaunchID))
	field([]byte(b.Grant.Identity.Epoch))
	field([]byte(b.Grant.Identity.PolicyDigest))
	field(b.KeySetDigest[:])
	field([]byte{byte(b.Profile)})
	field(binary.BigEndian.AppendUint64(nil, uint64(d.Deadline.UnixNano())))
	field(d.HostKey)
	field(nonce)
	return out
}

func TestResidentV1RegistrationOmitsConnectionGeneration(t *testing.T) {
	hostKey := rescueKey(0x61)
	d := fixtureDispatch(hostKey, "launch-v1-wire", time.Unix(1_800_000_000, 123456789))
	d.Binding.ConnectionGeneration = 7
	const endpoint, host, principal = "https://gateway.invalid/mcp", "host-v1", "principal-v1"
	nonce := []byte("registration-nonce")
	wantTranscript := legacyRegisterTranscript(endpoint, host, principal, d, nonce)
	if got := transcript(endpoint, host, principal, d, nonce); !bytes.Equal(got, wantTranscript) {
		t.Fatal("registration transcript changed from the v1 pre-425 format")
	}

	wire := encodeRegister(host, principal, d, []byte("signature"))
	var fields map[string]string
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["connection_generation"]; exists {
		t.Fatal("v1 registration wire contains connection_generation")
	}
	decoded, err := decodeRegister(wire)
	if err != nil || decoded.dispatch.Binding.ConnectionGeneration != 1 {
		t.Fatalf("decoded resident generation = %d, error %v; want internal generation 1", decoded.dispatch.Binding.ConnectionGeneration, err)
	}
	fields["connection_generation"] = "7"
	withGeneration, _ := json.Marshal(fields)
	if _, err := decodeRegister(withGeneration); err == nil {
		t.Fatal("v1 registration accepted connection_generation from the wire")
	}
}

func TestResidentWorkV1RegistrationOmitsConnectionGeneration(t *testing.T) {
	hostKey := rescueKey(0x62)
	d := fixtureDispatch(hostKey, "launch-v1-work", time.Unix(1_800_000_000, 123456789))
	d.Binding.ConnectionGeneration = 9
	const endpoint, host, principal = "https://gateway.invalid/mcp", "host-v1", "principal-v1"
	nonce := []byte("resident-work-nonce")
	work := agentgateway.ResidentWorkBinding{Generation: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Sequence: 1,
		RequestID: "0123456789abcdef0123456789abcdef", DeadlineMillis: 1_800_000_000_000}
	wantTranscript := legacyRegisterTranscript(endpoint, host, principal, d, nonce)
	for _, value := range []string{"resident-fixed-work-v1", work.Generation, "1", work.RequestID, "1800000000000"} {
		wantTranscript = binary.BigEndian.AppendUint32(wantTranscript, uint32(len(value)))
		wantTranscript = append(wantTranscript, value...)
	}
	if got := workTranscript(endpoint, host, principal, d, nonce, work); !bytes.Equal(got, wantTranscript) {
		t.Fatal("resident-work signature transcript changed from the v1 pre-425 format")
	}

	wire := encodeWorkRegister(host, principal, d, []byte("signature"), work)
	var fields map[string]string
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["connection_generation"]; exists {
		t.Fatal("resident-work v1 wire contains connection_generation")
	}
	decoded, err := decodeWorkRegister(wire)
	if err != nil || decoded.dispatch.Binding.ConnectionGeneration != 1 {
		t.Fatalf("decoded resident-work generation = %d, error %v; want internal generation 1", decoded.dispatch.Binding.ConnectionGeneration, err)
	}
	fields["connection_generation"] = "9"
	withGeneration, _ := json.Marshal(fields)
	if _, err := decodeWorkRegister(withGeneration); err == nil {
		t.Fatal("resident-work v1 registration accepted connection_generation from the wire")
	}
}

func TestRescueAllowRetainsConnectionGeneration(t *testing.T) {
	d := fixtureDispatch(rescueKey(0x63), "launch-rescue-generation", time.Now().Add(time.Hour))
	d.Binding.ConnectionGeneration = 2
	wire := encodeRescueAnswer(agentgateway.RescueAnswer{Answer: agentgateway.RescueAllow, Dispatch: d, Signature: []byte("signature")})
	var fields map[string]string
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["connection_generation"] != "2" {
		t.Fatalf("rescue allow connection_generation = %q; want 2", fields["connection_generation"])
	}
	decoded, err := decodeRescueAnswer(wire)
	if err != nil || decoded.Dispatch.Binding.ConnectionGeneration != 2 {
		t.Fatalf("decoded rescue generation = %d, error %v; want 2", decoded.Dispatch.Binding.ConnectionGeneration, err)
	}
}
