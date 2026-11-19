package agenttransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

type requestRescueRegistry struct {
	request  chan agentgateway.RescueIdentity
	done     chan struct{}
	ended    chan []byte
	answered chan rescueAnswerCall
	once     sync.Once
}

type rescueAnswerCall struct {
	id   []byte
	a    agentgateway.RescueAnswer
	conn net.Conn
}

func newRequestRescueRegistry() *requestRescueRegistry {
	return &requestRescueRegistry{
		request: make(chan agentgateway.RescueIdentity, 1), done: make(chan struct{}),
		ended: make(chan []byte, 1), answered: make(chan rescueAnswerCall, 1),
	}
}

func (*requestRescueRegistry) AdmitRescue() error { return nil }
func (r *requestRescueRegistry) OpenRescue(agentgateway.RescueRequest) (agentgateway.RescueReception, error) {
	return agentgateway.RescueReception{
		ID: bytes.Repeat([]byte{0x71}, agentgateway.RescueReceptionSize), AttemptID: 1, Code: "123456789012",
		Request: r.request, Done: r.done,
	}, nil
}
func (r *requestRescueRegistry) EndRescue(id []byte) {
	r.ended <- append([]byte(nil), id...)
	r.endReception()
}
func (r *requestRescueRegistry) EndRescueAttempt(id []byte, _ uint64) { r.EndRescue(id) }
func (r *requestRescueRegistry) DisconnectRescue(id []byte, _ uint64) { r.EndRescue(id) }
func (*requestRescueRegistry) TouchRescue([]byte, uint64)             {}

func (r *requestRescueRegistry) endReception() {
	r.once.Do(func() { close(r.done) })
}
func (r *requestRescueRegistry) AnswerRescue(id []byte, _ uint64, a agentgateway.RescueAnswer, conn net.Conn, _ func()) error {
	r.answered <- rescueAnswerCall{id: append([]byte(nil), id...), a: a, conn: conn}
	return conn.Close()
}

type rescueWS struct {
	conn *websocket.Conn
	ctx  context.Context
}

func openRescueWebSocket(t *testing.T, h *RescueHandler, key ssh.Signer) (*rescueWS, []byte) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	endpoint := "wss" + strings.TrimPrefix(srv.URL, "https") + agentgateway.RescuePath
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	conn, _, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{HTTPClient: srv.Client(), Subprotocols: []string{RescueSubprotocol}})
	dialCancel()
	if err != nil {
		t.Fatalf("dial rescue websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	writeJSON := func(v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := conn.Write(writeCtx, websocket.MessageText, b); err != nil {
			t.Fatalf("write rescue websocket: %v", err)
		}
	}
	readJSON := func() map[string]json.RawMessage {
		t.Helper()
		readCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		_, b, err := conn.Read(readCtx)
		if err != nil {
			t.Fatalf("read rescue websocket: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("decode rescue websocket frame: %v", err)
		}
		return m
	}
	writeJSON(map[string]string{"type": "hello", "nonce": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, agentgateway.RescueNonceSize))})
	challenge := readJSON()
	var typ, encodedNonce string
	_ = json.Unmarshal(challenge["type"], &typ)
	_ = json.Unmarshal(challenge["nonce"], &encodedNonce)
	if typ != "challenge" {
		t.Fatalf("first response type %q, want challenge", typ)
	}
	gatewayNonce, err := base64.RawURLEncoding.DecodeString(encodedNonce)
	if err != nil {
		t.Fatalf("decode challenge nonce: %v", err)
	}
	keyWire := key.PublicKey().Marshal()
	sig, err := key.Sign(rand.Reader, agentgateway.RescueTranscript(rescueEndpoint, keyWire, gatewayNonce, agentgateway.RescuePurposeNew, nil))
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(map[string]string{"type": "proof", "purpose": agentgateway.RescuePurposeNew,
		"key": base64.RawURLEncoding.EncodeToString(keyWire), "signature": base64.RawURLEncoding.EncodeToString(sig.Blob)})
	reception := readJSON()
	_ = json.Unmarshal(reception["type"], &typ)
	if typ != "reception" {
		t.Fatalf("proof response type %q, want reception", typ)
	}
	id, err := base64.RawURLEncoding.DecodeString(rawString(t, reception["reception"]))
	if err != nil {
		t.Fatalf("decode reception id: %v", err)
	}
	return &rescueWS{conn: conn, ctx: ctx}, id
}

func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode string field: %v", err)
	}
	return s
}

func readRescueFrame(t *testing.T, ws *rescueWS) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(ws.ctx, 5*time.Second)
	defer cancel()
	_, b, err := ws.conn.Read(ctx)
	if err != nil {
		t.Fatalf("rescue frame was not delivered within 5s: %v", err)
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(b, &frame); err != nil {
		t.Fatalf("decode rescue frame: %v", err)
	}
	return frame
}

// The host must receive a signed, settings-derived identity before it answers;
// otherwise it cannot show the person and organization that the gateway verified.
func TestRescueHandlerForwardsIdentityAndSignedAnswer(t *testing.T) {
	registry := newRequestRescueRegistry()
	h, err := NewRescueHandler(registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("RescueHandler.Close: %v", err)
		}
	})
	ws, id := openRescueWebSocket(t, h, rescueKey(0x49))
	identity := agentgateway.RescueIdentity{
		Nonce: bytes.Repeat([]byte{0x22}, 32), Client: rescueKey(0x43).PublicKey().Marshal(),
		DisplayName: "山田（サポート）", Organization: "O2 CONNECTIVE", Profile: agentgateway.RescueProfile,
	}
	requestTranscript := agentgateway.RescueRequestTranscript(rescueEndpoint, id, rescueIdentity.PublicKey().Marshal(), identity.Nonce,
		identity.Client, identity.DisplayName, identity.Organization, identity.Profile)
	requestSignature, err := rescueIdentity.Sign(rand.Reader, requestTranscript)
	if err != nil {
		t.Fatal(err)
	}
	identity.Signature = requestSignature.Blob
	registry.request <- identity
	frame := readRescueFrame(t, ws)
	if got := rawString(t, frame["type"]); got != "request" {
		t.Fatalf("frame type = %q, want request", got)
	}
	if got := rawString(t, frame["display_name"]); got != identity.DisplayName {
		t.Fatalf("display_name = %q", got)
	}
	if got := rawString(t, frame["organization"]); got != identity.Organization {
		t.Fatalf("organization = %q", got)
	}
	if got := rawString(t, frame["profile"]); got != agentgateway.RescueProfile {
		t.Fatalf("profile = %q", got)
	}
	if got, _ := base64.RawURLEncoding.DecodeString(rawString(t, frame["nonce"])); !bytes.Equal(got, identity.Nonce) {
		t.Fatalf("nonce = %x", got)
	}
	if got, _ := base64.RawURLEncoding.DecodeString(rawString(t, frame["client"])); !bytes.Equal(got, identity.Client) {
		t.Fatalf("client = %x", got)
	}
	if got, _ := base64.RawURLEncoding.DecodeString(rawString(t, frame["signature"])); !bytes.Equal(got, identity.Signature) {
		t.Fatalf("signature = %x", got)
	}
	if err := rescueIdentity.PublicKey().Verify(requestTranscript, &ssh.Signature{Format: requestSignature.Format, Blob: identity.Signature}); err != nil {
		t.Fatalf("identity signature does not verify: %v", err)
	}

	d := fixtureDispatch(rescueIdentity, "rescue-launch", time.Now().Add(time.Minute))
	answerKey := rescueKey(0x49)
	digest := sha256.Sum256(requestTranscript)
	signature, err := answerKey.Sign(rand.Reader, agentgateway.RescueAnswerTranscript(id, identity.Nonce, digest[:], agentgateway.RescueAllow, d))
	if err != nil {
		t.Fatal(err)
	}
	answer := map[string]any{"type": "answer", "answer": agentgateway.RescueAllow,
		"signature":  base64.RawURLEncoding.EncodeToString(signature.Blob),
		"generation": strconv.FormatUint(d.Binding.Grant.Generation, 10), "connection_generation": strconv.FormatUint(d.Binding.ConnectionGeneration, 10),
		"launch_id": d.Binding.Grant.Identity.LaunchID, "epoch": d.Binding.Grant.Identity.Epoch,
		"policy_digest": d.Binding.Grant.Identity.PolicyDigest, "key_set_digest": hex.EncodeToString(d.Binding.KeySetDigest[:]),
		"profile": profileMac, "deadline_unix_nano": strconv.FormatInt(d.Deadline.UnixNano(), 10), "host_key": base64.RawURLEncoding.EncodeToString(d.HostKey)}
	b, _ := json.Marshal(answer)
	if err := ws.conn.Write(ws.ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write answer frame: %v", err)
	}
	select {
	case call := <-registry.answered:
		if !bytes.Equal(call.id, id) || call.a.Answer != agentgateway.RescueAllow || call.conn == nil || !sameDispatch(call.a.Dispatch, d) {
			t.Fatalf("AnswerRescue call = id %x, answer %q, conn %v", call.id, call.a.Answer, call.conn)
		}
		answerTranscript := agentgateway.RescueAnswerTranscript(id, identity.Nonce, digest[:], agentgateway.RescueAllow, d)
		if err := rescueIdentity.PublicKey().Verify(answerTranscript, &ssh.Signature{Format: signature.Format, Blob: call.a.Signature}); err != nil {
			t.Fatalf("answer signature does not verify: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AnswerRescue was not called within 5s")
	}
}

// Closing the WSS while an identity is awaiting approval must end its gateway
// reception, so a later approval cannot revive a host that has disconnected.
func TestRescueHandlerEndsPendingRequestWhenWebSocketCloses(t *testing.T) {
	registry := newRequestRescueRegistry()
	h, err := NewRescueHandler(registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("RescueHandler.Close: %v", err)
		}
	})
	ws, id := openRescueWebSocket(t, h, rescueKey(0x49))
	registry.request <- agentgateway.RescueIdentity{Nonce: bytes.Repeat([]byte{0x22}, 32), Profile: agentgateway.RescueProfile}
	frame := readRescueFrame(t, ws)
	if got := rawString(t, frame["type"]); got != "request" {
		t.Fatalf("frame type = %q, want request", got)
	}
	if err := ws.conn.Close(websocket.StatusNormalClosure, "host disconnected"); err != nil {
		t.Fatalf("close websocket: %v", err)
	}
	select {
	case got := <-registry.ended:
		if !bytes.Equal(got, id) {
			t.Fatalf("ended reception = %x, want %x", got, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending reception was not ended within 5s")
	}
}

func TestRescueHandlerClosesWebSocketWhenReceptionEnds(t *testing.T) {
	for _, sendRequest := range []bool{false, true} {
		name := "before request"
		if sendRequest {
			name = "after request"
		}
		t.Run(name, func(t *testing.T) {
			registry := newRequestRescueRegistry()
			h, err := NewRescueHandler(registry)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := h.Close(ctx); err != nil {
					t.Errorf("RescueHandler.Close: %v", err)
				}
			})
			ws, _ := openRescueWebSocket(t, h, rescueKey(0x49))
			if sendRequest {
				registry.request <- agentgateway.RescueIdentity{Nonce: bytes.Repeat([]byte{0x22}, agentgateway.RescueNonceSize), Profile: agentgateway.RescueProfile}
				ctx, cancel := context.WithTimeout(ws.ctx, 5*time.Second)
				_, b, err := ws.conn.Read(ctx)
				cancel()
				if err != nil {
					_ = ws.conn.CloseNow()
					t.Fatalf("request frame was not delivered before Done: %v", err)
				}
				var frame map[string]json.RawMessage
				if err := json.Unmarshal(b, &frame); err != nil {
					t.Fatalf("decode request frame: %v", err)
				}
				if got := rawString(t, frame["type"]); got != "request" {
					t.Fatalf("frame type = %q, want request", got)
				}
			}
			registry.endReception()
			ctx, cancel := context.WithTimeout(ws.ctx, 5*time.Second)
			defer cancel()
			_, _, err = ws.conn.Read(ctx)
			if websocket.CloseStatus(err) < 0 {
				_ = ws.conn.CloseNow()
				t.Fatalf("host WSS read after reception Done = %v (close status %v), want a close frame", err, websocket.CloseStatus(err))
			}
		})
	}
}

func TestRescueRequestRealGatewayBindsFreshRoute(t *testing.T) {
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	set, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "OKP", "crv": "Ed25519", "kid": "synthetic-ed", "alg": "EdDSA", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(pub),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{
		Issuer: "https://issuer.example.invalid/", Audience: "https://gateway.example.invalid/mcp", Scope: "rescue.agent",
		Types: []string{"at+jwt"}, JWKS: set,
	})
	if err != nil {
		t.Fatal(err)
	}
	identityKey, hostKey := rescueKey(0x49), rescueKey(0x71)
	gateway, err := agentgateway.New(agentgateway.Config{
		Verifier: verifier, Resource: "https://gateway.example.invalid/mcp", Organization: "O2 CONNECTIVE", CallTimeout: 5 * time.Second,
		RescueSigner: identityKey, Budgets: chanBudgets(),
		Principals: []agentgateway.Principal{{Label: "operator", Subject: "synthetic-subject-1", DisplayName: "山田（サポート）", Roles: []agentgateway.Role{agentgateway.RoleRescueOperator}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := gateway.Close(ctx); err != nil {
			t.Errorf("Gateway.Close: %v", err)
		}
	})
	h, err := NewRescueHandler(gateway)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("RescueHandler.Close: %v", err)
		}
	})
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	wsEndpoint := "wss" + strings.TrimPrefix(srv.URL, "https") + agentgateway.RescuePath
	wsCtx, wsCancel := context.WithCancel(context.Background())
	t.Cleanup(wsCancel)
	link := newCloser(wsCancel)
	baseTransport, ok := srv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("TLS test server client has no HTTP transport")
	}
	transport := baseTransport.Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !link.begin() {
			return nil, ErrRefused
		}
		defer link.end()
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return link.attach(conn)
	}
	hostHTTP := &http.Client{Transport: transport}
	t.Cleanup(hostHTTP.CloseIdleConnections)
	dialCtx, dialCancel := context.WithTimeout(wsCtx, 5*time.Second)
	ws, _, err := websocket.Dial(dialCtx, wsEndpoint, &websocket.DialOptions{HTTPClient: hostHTTP, Subprotocols: []string{RescueSubprotocol}})
	dialCancel()
	if err != nil {
		t.Fatalf("dial rescue websocket: %v", err)
	}
	link.setWS(ws)
	if !link.settle() {
		t.Fatal("host WSS transport was not owned")
	}
	t.Cleanup(func() { link.start() })
	writeFrame := func(v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		writeCtx, cancel := context.WithTimeout(wsCtx, 5*time.Second)
		defer cancel()
		if err := ws.Write(writeCtx, websocket.MessageText, b); err != nil {
			t.Fatalf("write host frame: %v", err)
		}
	}
	readFrame := func() map[string]json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(wsCtx, 5*time.Second)
		defer cancel()
		_, b, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("host frame read: %v", err)
		}
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(b, &frame); err != nil {
			t.Fatalf("decode host frame: %v", err)
		}
		return frame
	}
	writeFrame(map[string]string{"type": "hello", "nonce": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, agentgateway.RescueNonceSize))})
	challenge := readFrame()
	gatewayNonce, err := base64.RawURLEncoding.DecodeString(rawString(t, challenge["nonce"]))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := hostKey.Sign(rand.Reader, agentgateway.RescueTranscript(gateway.RescueEndpoint(), hostKey.PublicKey().Marshal(), gatewayNonce, agentgateway.RescuePurposeNew, nil))
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(map[string]string{"type": "proof", "purpose": agentgateway.RescuePurposeNew,
		"key": base64.RawURLEncoding.EncodeToString(hostKey.PublicKey().Marshal()), "signature": base64.RawURLEncoding.EncodeToString(proof.Blob)})
	reception := readFrame()
	receptionID, err := base64.RawURLEncoding.DecodeString(rawString(t, reception["reception"]))
	if err != nil {
		t.Fatal(err)
	}

	mcp := newVerticalMCP(t, gateway)
	mcp.id++
	rec, call := mcp.request(mcp.id, "tools/call", map[string]any{"name": "rescue_request", "arguments": map[string]any{"code": rawString(t, reception["code"])}})
	result, ok := call["result"].(map[string]any)
	if rec.Code != 200 || !ok {
		t.Fatalf("rescue_request MCP call = HTTP %d %v; want a pending result", rec.Code, call)
	}
	pending, _ := result["structuredContent"].(map[string]any)
	failed, _ := result["isError"].(bool)
	if failed || pending["state"] != "pending" {
		t.Fatalf("rescue_request = %v, isError %v; want pending", pending, failed)
	}
	request := readFrame()
	if got := rawString(t, request["type"]); got != "request" {
		t.Fatalf("host frame type = %q, want request", got)
	}
	decodeField := func(name string) []byte {
		t.Helper()
		b, err := base64.RawURLEncoding.DecodeString(rawString(t, request[name]))
		if err != nil {
			t.Fatalf("decode request %s: %v", name, err)
		}
		return b
	}
	requestIdentity := agentgateway.RescueIdentity{
		Nonce: decodeField("nonce"), Client: decodeField("client"), DisplayName: rawString(t, request["display_name"]),
		Organization: rawString(t, request["organization"]), Profile: rawString(t, request["profile"]), Signature: decodeField("signature"),
	}
	transcript := agentgateway.RescueRequestTranscript(gateway.RescueEndpoint(), receptionID, hostKey.PublicKey().Marshal(), requestIdentity.Nonce,
		requestIdentity.Client, requestIdentity.DisplayName, requestIdentity.Organization, requestIdentity.Profile)
	if err := identityKey.PublicKey().Verify(transcript, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: requestIdentity.Signature}); err != nil {
		t.Fatalf("gateway identity proof did not verify: %v", err)
	}
	clientKey, err := ssh.ParsePublicKey(requestIdentity.Client)
	if err != nil {
		t.Fatalf("parse request client key: %v", err)
	}
	runtime := &verticalRuntime{procs: make(chan *verticalProc, 1)}
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{clientKey})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ephemeral.NewCurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	challengeOwner, err := issuer.Prepare(ephemeral.ApprovedPolicy{PolicyDigest: agentexec.MacCurrentUserPolicyDigest(), KeySetDigest: digest,
		GuestBoundary: supervisor.CurrentUserNoIsolation, HardDeadline: time.Now().Add(30 * time.Second), BackendLeaseCeiling: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(context.Background(), challengeOwner, time.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	session, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{clientKey}, HostKey: hostKey, Budgets: chanBudgets()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := errors.Join(issuer.Close(), host.Close(ctx)); err != nil {
			t.Errorf("close fake host: %v", err)
		}
	})
	d := session.Grant()
	dispatch := agentgateway.Dispatch{Binding: agentchannel.Binding{Grant: d.Binding, ConnectionGeneration: 1, KeySetDigest: d.KeySetDigest, Profile: session.Profile()}, Deadline: d.HardDeadline, HostKey: hostKey.PublicKey().Marshal()}
	digestRequest := sha256.Sum256(transcript)
	signature, err := hostKey.Sign(rand.Reader, agentgateway.RescueAnswerTranscript(receptionID, requestIdentity.Nonce, digestRequest[:], agentgateway.RescueAllow, dispatch))
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(map[string]any{"type": "answer", "answer": agentgateway.RescueAllow, "signature": base64.RawURLEncoding.EncodeToString(signature.Blob),
		"generation": strconv.FormatUint(dispatch.Binding.Grant.Generation, 10), "connection_generation": strconv.FormatUint(dispatch.Binding.ConnectionGeneration, 10),
		"launch_id": dispatch.Binding.Grant.Identity.LaunchID,
		"epoch":     dispatch.Binding.Grant.Identity.Epoch, "policy_digest": dispatch.Binding.Grant.Identity.PolicyDigest,
		"key_set_digest": hex.EncodeToString(dispatch.Binding.KeySetDigest[:]), "profile": profileMac,
		"deadline_unix_nano": strconv.FormatInt(dispatch.Deadline.UnixNano(), 10), "host_key": base64.RawURLEncoding.EncodeToString(dispatch.HostKey)})
	// The SSH server writes its version at once, so the channel starts only
	// after the answer frame: the reception reads exactly one answer first.
	serveCtx, serveCancel := context.WithCancel(context.Background())
	t.Cleanup(serveCancel)
	hostStream := newStream(serveCtx, ws, link, time.Second)
	served := make(chan error, 1)
	go func() { served <- host.Serve(serveCtx, hostStream) }()
	status, failed := mcp.tool("rescue_request_status", map[string]any{})
	if failed || status["state"] != "allow" {
		t.Fatalf("rescue_request_status = %v, isError %v; want allow", status, failed)
	}
	fresh := newVerticalMCP(t, gateway)
	started, failed := fresh.start("rescue-route", "printf ready\n\n")
	if failed {
		t.Fatalf("rescue_start failed: %v", started)
	}
	commandID := verticalID(t, started)
	proc := verticalProcNext(t, runtime)
	if proc.input != "printf ready\n\n" {
		t.Fatalf("route command = %q, want one command", proc.input)
	}
	proc.output <- []byte("ready\n")
	close(proc.output)
	close(proc.exited)
	if terminal := fresh.terminal(commandID); terminal["phase"] != "exited" {
		t.Fatalf("route command terminal state = %v; want exited", terminal)
	}
	// The route lives until its deadline; closing the gateway ends it, and the
	// host's channel on the reception stream ends with it.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := gateway.Close(closeCtx); err != nil {
		t.Fatalf("gateway Close: %v", err)
	}
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("agentlink.Host.Serve did not end within 5s of the gateway closing")
	}
}

// A frame the host sends as its answer is an answer even when the transport
// cannot accept it: the gateway must record it as failed (or timeout after
// 75 seconds), not as the host closing the reception. An allow whose dispatch
// fails the registration check must never reach the gateway as an allow.
func TestRescueHandlerHandsUnacceptableAnswerToGatewayAsUnverifiable(t *testing.T) {
	expired := fixtureDispatch(rescueIdentity, "rescue-launch", time.Now().Add(-time.Minute))
	cases := map[string]struct {
		typ   websocket.MessageType
		frame []byte
	}{
		"not json":     {websocket.MessageText, []byte("{")},
		"binary frame": {websocket.MessageBinary, []byte(`{"type":"answer","answer":"deny","signature":"AA"}`)},
		"expired allow": {websocket.MessageText, func() []byte {
			b, _ := json.Marshal(map[string]any{"type": "answer", "answer": agentgateway.RescueAllow,
				"signature":  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, ed25519.SignatureSize)),
				"generation": strconv.FormatUint(expired.Binding.Grant.Generation, 10), "launch_id": expired.Binding.Grant.Identity.LaunchID, "epoch": expired.Binding.Grant.Identity.Epoch,
				"policy_digest": expired.Binding.Grant.Identity.PolicyDigest, "key_set_digest": hex.EncodeToString(expired.Binding.KeySetDigest[:]),
				"profile": profileMac, "deadline_unix_nano": strconv.FormatInt(expired.Deadline.UnixNano(), 10), "host_key": base64.RawURLEncoding.EncodeToString(expired.HostKey)})
			return b
		}()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registry := newRequestRescueRegistry()
			h, err := NewRescueHandler(registry)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := h.Close(ctx); err != nil {
					t.Errorf("RescueHandler.Close: %v", err)
				}
			})
			ws, id := openRescueWebSocket(t, h, rescueKey(0x49))
			registry.request <- agentgateway.RescueIdentity{Nonce: bytes.Repeat([]byte{0x22}, agentgateway.RescueNonceSize), Profile: agentgateway.RescueProfile}
			if got := rawString(t, readRescueFrame(t, ws)["type"]); got != "request" {
				t.Fatalf("frame type = %q, want request", got)
			}
			if err := ws.conn.Write(ws.ctx, tc.typ, tc.frame); err != nil {
				t.Fatalf("write answer frame: %v", err)
			}
			select {
			case call := <-registry.answered:
				if !bytes.Equal(call.id, id) || call.a.Answer == agentgateway.RescueAllow || len(call.a.Signature) != 0 {
					t.Fatalf("AnswerRescue got answer %q with a %d byte signature; want one that never verifies", call.a.Answer, len(call.a.Signature))
				}
			case <-registry.ended:
				t.Fatal("unacceptable answer ended the reception without reaching AnswerRescue")
			case <-time.After(5 * time.Second):
				t.Fatal("AnswerRescue was not called within 5s")
			}
		})
	}
}
