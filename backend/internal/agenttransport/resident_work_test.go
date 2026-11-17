package agenttransport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
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

// rebound embeds a real intent but reports another work binding. Embedding
// also promotes the unexported encode, so it satisfies ResidentIntent.
type rebound struct {
	ResidentFixedIntent
	other agentgateway.ResidentWorkBinding
}

func (r rebound) WorkBinding() agentgateway.ResidentWorkBinding { return r.other }

// The registration signs the binding of the intent it was given. Only the two
// intents this package mints may reach it, or a caller could pair a received
// correlation with work the Mac never received and have the host key sign it.
func TestResidentWorkRegistrationAcceptsOnlyMintedIntents(t *testing.T) {
	host := fixtureSigner(t, 7)
	original, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	received := ResidentFixedIntent{residentCorrelation: residentCorrelation{generation: strings.Repeat("a", 64), requestID: strings.Repeat("1", 32), endpoint: testEndpoint, host: hostLabel, principal: member1, sequence: 1, deadline: time.Now().Add(2 * time.Minute), original: original, receiver: &ResidentReceiver{}, hostFingerprint: ssh.FingerprintSHA256(host.PublicKey())}}
	cfg := clientConfig(host)
	cfg.ResidentIntent = received
	if _, e := NewClient(cfg, newStubOwner(host, time.Now().Add(time.Minute))); e != nil {
		t.Fatal("received intent refused", e)
	}
	other := received.WorkBinding()
	other.RequestID = strings.Repeat("2", 32)
	other.Run = agentgateway.ResidentRunScope{CommandSHA256: strings.Repeat("ab", 32), Cwd: ".", TimeoutMillis: 60000}
	cfg.ResidentIntent = rebound{received, other}
	if _, e := NewClient(cfg, newStubOwner(host, time.Now().Add(time.Minute))); !errors.Is(e, ErrConfig) {
		t.Fatal("an intent with a substituted binding reached the registration", e)
	}
}
