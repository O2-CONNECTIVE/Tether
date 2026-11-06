package agenttransport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func TestResidentWorkRegistrationBindsEachCorrelationFieldInHostProof(t *testing.T) {
	key := fixtureSigner(t, 7)
	deadline := time.Now().Add(10 * time.Second).Truncate(time.Millisecond)
	d := fixtureDispatch(key, "launch-1", deadline)
	b := agentgateway.ResidentWorkBinding{Host: hostLabel, Principal: member1, Generation: strings.Repeat("a", 64), RequestID: strings.Repeat("1", 32), Sequence: 1, DeadlineMillis: deadline.UnixMilli()}
	nonce := make([]byte, 32)
	nonce[0] = 9
	sig, e := sign(key, key.PublicKey(), workTranscript(testEndpoint, hostLabel, member1, d, nonce, b))
	if e != nil {
		t.Fatal(e)
	}
	message := encodeWorkRegister(hostLabel, member1, d, sig, b)
	r, e := decodeWorkRegister(message)
	if e != nil || r.resident == nil || verify(key.PublicKey(), workTranscript(testEndpoint, r.host, r.principal, r.dispatch, nonce, *r.resident), r.signature) != nil {
		t.Fatal("valid actual binding refused", e)
	}
	for _, field := range []string{"resident_generation", "resident_sequence", "resident_request_id", "resident_deadline_ms"} {
		t.Run(field, func(t *testing.T) {
			var m map[string]string
			_ = json.Unmarshal(message, &m)
			switch field {
			case "resident_generation":
				m[field] = strings.Repeat("b", 64)
			case "resident_sequence":
				m[field] = "2"
			case "resident_request_id":
				m[field] = strings.Repeat("2", 32)
			case "resident_deadline_ms":
				m[field] = "1900000000000"
			}
			changed, e := decodeWorkRegister(residentJSON(m))
			if e != nil {
				t.Fatal(e)
			}
			if verify(key.PublicKey(), workTranscript(testEndpoint, changed.host, changed.principal, changed.dispatch, nonce, *changed.resident), changed.signature) == nil {
				t.Fatal("host proof omitted", field)
			}
		})
	}
	legacy, e := sign(key, key.PublicKey(), transcript(testEndpoint, hostLabel, member1, d, nonce))
	if e != nil {
		t.Fatal(e)
	}
	if verify(key.PublicKey(), workTranscript(testEndpoint, hostLabel, member1, d, nonce, b), legacy) == nil {
		t.Fatal("legacy proof became resident work proof")
	}
	for _, extra := range []string{`"approved":"true"`, `"command":"true"`, `"resident_sequence":"1"`} {
		bad := append(append([]byte(nil), message[:len(message)-1]...), []byte(","+extra+"}")...)
		if _, e := decodeWorkRegister(bad); e == nil {
			t.Fatal("unknown/duplicate registration field accepted")
		}
	}
}
