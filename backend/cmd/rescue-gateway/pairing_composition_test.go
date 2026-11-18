package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

const pairEndpoint = "https://gateway.example.invalid" + agentgateway.PairPath

func pairingDoc(t *testing.T) map[string]any {
	t.Helper()
	doc := residentDoc(t)
	host := item(doc, "hosts", 0)
	delete(host, "public_key")
	host["paired"] = true
	doc["pairing"] = map[string]any{
		"registry_path": "/tmp/pairing-registry.json", "audience": "pair-api",
		"client_id": "pair-client", "token_types": []any{"at+jwt"},
	}
	return doc
}

func pairToken(t *testing.T) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString(encode(t, map[string]any{"alg": "EdDSA", "kid": "synthetic-ed", "typ": "at+jwt"}))
	claims := enc.EncodeToString(encode(t, map[string]any{
		"iss": "https://issuer.example.invalid/", "aud": "pair-api", "sub": "synthetic-subject-1",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
	}))
	input := header + "." + claims
	return input + "." + enc.EncodeToString(ed25519.Sign(seedKey(0x51), []byte(input)))
}

func registerPair(t *testing.T, l *fakeListener, seed byte) (int, string) {
	t.Helper()
	token := pairToken(t)
	key := publicOf(t, seed).Marshal()
	digest := sha256.Sum256([]byte(token))
	sig, err := signerOf(t, seed).Sign(nil, agentgateway.PairTranscript(pairEndpoint, key, digest))
	if err != nil {
		t.Fatal("sign pair transcript", err)
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicOf(t, seed))))
	body := encode(t, map[string]string{
		"host_public_key": authorized,
		"proof":           base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig)),
	})
	return roundTrip(t, l, "POST "+agentgateway.PairPath+" HTTP/1.1\r\nHost: gateway.example.invalid\r\nConnection: close\r\nAuthorization: Bearer "+token+"\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+string(body))
}

func startPairingEntry(t *testing.T, doc map[string]any, registryBytes []byte) (*recorder, *fakeListener, context.CancelFunc, <-chan int) {
	t.Helper()
	r := newRecorder(t, encode(t, doc))
	r.registryBytes = append([]byte(nil), registryBytes...)
	r.listener = newFakeListener()
	ctx, cancel := context.WithCancel(context.Background())
	var diagnostic strings.Builder
	exited := make(chan int, 1)
	go func() { exited <- execute(ctx, []string{"-config", configPath}, &diagnostic, r.deps()) }()
	select {
	case <-r.listened:
	case code := <-exited:
		cancel()
		t.Fatalf("pairing entry exited before listen: %d (%s)", code, diagnostic.String())
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("pairing entry never listened")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-exited:
			if code != 0 {
				t.Errorf("pairing entry shutdown exit=%d", code)
			}
		case <-time.After(20 * time.Second):
			t.Error("pairing entry did not join")
		}
	})
	return r, r.listener, cancel, exited
}

func TestEntryPairsHostThenReplacesResidentControlA6(t *testing.T) {
	// Empty enrollment must reject every unpaired key before granting resident authority.
	_, l, _, _ := startPairingEntry(t, pairingDoc(t), nil)
	if _, _, err := residentPeerAttempt(t, l, 0x21); err == nil {
		t.Fatal("unpaired legacy host key established resident control")
	}
	if code, body := registerPair(t, l, 0x41); code != http.StatusOK {
		t.Fatalf("pair registration status = %d, want 200 (%s)", code, body)
	}
	ws, transcript := residentPeerWith(t, l, 0x41)
	sid := residentAwaitSession(t, l, residentAuth(t, false, "synthetic-subject-1"))
	args := map[string]any{"request_id": strings.Repeat("a", 32)}
	if code, body := residentTool(t, l, residentAuth(t, false, "synthetic-subject-1"), sid, "tether_fixed_start", args); code != 200 || !strings.Contains(body, `"phase":"pending"`) {
		t.Fatalf("paired fixed work was not dispatched: %d %s", code, body)
	}
	intent := residentReadMessage(t, ws)
	refusal := []string{intent["generation"], intent["sequence"], intent["request_id"], "work_unconfigured"}
	sig, err := signerOf(t, 0x41).Sign(nil, transcript("host-refused", refusal...))
	if err != nil {
		t.Fatal("sign recovery proof", err)
	}
	residentWriteMessage(t, ws, map[string]string{"type": "resident_refused", "generation": refusal[0], "sequence": refusal[1], "request_id": refusal[2], "code": refusal[3], "signature": base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := residentTool(t, l, residentAuth(t, false, "synthetic-subject-1"), sid, "tether_fixed_status", args)
		if code == 200 && strings.Contains(body, `"phase":"finished"`) && strings.Contains(body, `"owner_cleanup":"confirmed"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("signed recovery proof did not settle work: %d %s", code, body)
		}
		time.Sleep(time.Millisecond)
	}
	if code, body := registerPair(t, l, 0x42); code != http.StatusOK {
		t.Fatalf("replacement pair status = %d, want 200 (%s)", code, body)
	}
	// The replaced key's live control must be cut, not merely refused on reconnect.
	closed, stopClosed := context.WithTimeout(context.Background(), 5*time.Second)
	if _, _, err := ws.Read(closed); err == nil || closed.Err() != nil {
		stopClosed()
		t.Fatal("replaced host key kept its resident control open", err)
	}
	stopClosed()
	if code, body := residentTool(t, l, residentAuth(t, false, "synthetic-subject-1"), sid, "tether_fixed_start", map[string]any{"request_id": strings.Repeat("d", 32)}); code != 200 || !(strings.Contains(body, "control_ended") || strings.Contains(body, "busy_or_refused")) {
		t.Fatalf("replaced resident control still started work: %d %s", code, body)
	}
	if _, _, body := residentRPC(t, l, residentAuth(t, false, "synthetic-subject-1"), sid, "tools/list", nil); !strings.Contains(body, "tether_fixed_start") {
		t.Fatal("replacement removed old MCP session catalog", body)
	}
	if _, _, err := residentPeerAttempt(t, l, 0x41); err == nil {
		t.Fatal("replaced host key established a new resident handshake")
	}
}

func TestEntryKeepsOldSessionStatusReadOnlyAfterReplacementA8(t *testing.T) {
	// A pre-replacement MCP session may inspect its work but cannot start work under a retired host key.
	_, l, _, _ := startPairingEntry(t, pairingDoc(t), nil)
	if code, body := registerPair(t, l, 0x41); code != http.StatusOK {
		t.Fatalf("initial pair status = %d, want 200 (%s)", code, body)
	}
	ws, transcript := residentPeerWith(t, l, 0x41)
	defer ws.CloseNow()
	auth := residentAuth(t, false, "synthetic-subject-1")
	sid := residentAwaitSession(t, l, auth)
	args := map[string]any{"request_id": strings.Repeat("b", 32)}
	if code, body := residentTool(t, l, auth, sid, "tether_fixed_start", args); code != 200 || !strings.Contains(body, `"phase":"pending"`) {
		t.Fatalf("initial work did not start: %d %s", code, body)
	}
	intent := residentReadMessage(t, ws)
	refusal := []string{intent["generation"], intent["sequence"], intent["request_id"], "work_unconfigured"}
	sig, err := signerOf(t, 0x41).Sign(nil, transcript("host-refused", refusal...))
	if err != nil {
		t.Fatal(err)
	}
	residentWriteMessage(t, ws, map[string]string{"type": "resident_refused", "generation": refusal[0], "sequence": refusal[1], "request_id": refusal[2], "code": refusal[3], "signature": base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
	if code, body := registerPair(t, l, 0x42); code != http.StatusOK {
		t.Fatalf("replacement pair status = %d, want 200 (%s)", code, body)
	}
	if code, body := residentTool(t, l, auth, sid, "tether_fixed_status", args); code != 200 || !strings.Contains(body, `"phase":"finished"`) {
		t.Fatalf("old work status unavailable after replacement: %d %s", code, body)
	}
	code, body := residentTool(t, l, auth, sid, "tether_fixed_start", map[string]any{"request_id": strings.Repeat("c", 32)})
	if code != 200 || !(strings.Contains(body, "control_ended") || strings.Contains(body, "busy_or_refused")) {
		t.Fatalf("old MCP session started work after replacement: %d %s", code, body)
	}
}

func TestEntryLoadsPairingFromSharedRegistryAcrossExecutionsA10(t *testing.T) {
	// A committed enrollment must survive gateway restart through the configured registry store.
	r1, l1, cancel1, _ := startPairingEntry(t, pairingDoc(t), nil)
	if code, body := registerPair(t, l1, 0x41); code != http.StatusOK {
		t.Fatalf("pair registration status = %d, want 200 (%s)", code, body)
	}
	cancel1()
	select {
	case <-l1.closed:
	case <-time.After(20 * time.Second):
		t.Fatal("first execute did not stop")
	}
	_, l2, _, _ := startPairingEntry(t, pairingDoc(t), r1.registryBytes)
	if _, _, err := residentPeerAttempt(t, l2, 0x41); err != nil {
		t.Fatalf("persisted pairing key did not establish resident control: %v", err)
	}
}
