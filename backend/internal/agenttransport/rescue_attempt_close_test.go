package agenttransport

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

type attemptCloseRegistry struct {
	opened               chan *attemptCloseAttempt
	openCount            atomic.Int32
	firstConnClosed      <-chan struct{}
	mu                   sync.Mutex
	secondOpenedAfterEnd atomic.Bool
	answerStarted        chan struct{}
	answerDone           chan error
}

type attemptCloseAttempt struct {
	reception   agentgateway.RescueReception
	request     chan agentgateway.RescueIdentity
	attemptDone chan struct{}
}

func (r *attemptCloseRegistry) AdmitRescue() error { return nil }

func (r *attemptCloseRegistry) OpenRescue(req agentgateway.RescueRequest) (agentgateway.RescueReception, error) {
	attempt := r.openCount.Add(1)
	if attempt == 2 {
		r.mu.Lock()
		closed := r.firstConnClosed
		r.mu.Unlock()
		select {
		case <-closed:
			r.secondOpenedAfterEnd.Store(true)
		case <-time.After(time.Second):
		}
		if req.Purpose != agentgateway.RescuePurposeResume {
			return agentgateway.RescueReception{}, ErrRefused
		}
	}
	request := make(chan agentgateway.RescueIdentity, 1)
	attemptDone := make(chan struct{})
	res := agentgateway.RescueReception{
		ID: bytesOf(0x42, agentgateway.RescueReceptionSize), AttemptID: uint64(attempt), Code: "123456789012",
		Signature: make([]byte, ed25519.SignatureSize), Request: request,
		Done: make(chan struct{}), AttemptDone: attemptDone,
	}
	r.opened <- &attemptCloseAttempt{reception: res, request: request, attemptDone: attemptDone}
	return res, nil
}

func (*attemptCloseRegistry) EndRescue([]byte)                {}
func (*attemptCloseRegistry) EndRescueAttempt([]byte, uint64) {}
func (*attemptCloseRegistry) DisconnectRescue([]byte, uint64) {}
func (*attemptCloseRegistry) TouchRescue([]byte, uint64)      {}
func (r *attemptCloseRegistry) AnswerRescue(_ []byte, _ uint64, _ agentgateway.RescueAnswer, conn net.Conn, _ func()) error {
	if r.answerStarted != nil {
		close(r.answerStarted)
		_, err := conn.Read(make([]byte, 1))
		r.answerDone <- err
		return err
	}
	return nil
}

func TestAttemptDoneClosesWSSDuringBlockedWriteBeforeResume(t *testing.T) {
	registry := &attemptCloseRegistry{opened: make(chan *attemptCloseAttempt, 2)}
	handler, err := NewRescueHandler(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := handler.Close(ctx); err != nil {
			t.Errorf("RescueHandler.Close = %v", err)
		}
	}()
	transport := newMemTransport(handler)
	url := "ws://gateway.example.invalid" + agentgateway.RescuePath
	key, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytesOf(0x51, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	firstWS, err := dialAttemptCloseWS(url, key, agentgateway.RescuePurposeNew, nil, transport)
	if err != nil {
		t.Fatalf("open first WSS: %v", err)
	}
	defer firstWS.CloseNow()
	first := <-registry.opened
	firstPair := <-transport.pairs
	registry.mu.Lock()
	registry.firstConnClosed = firstPair.server.closedCh
	registry.mu.Unlock()
	writeGate := make(chan struct{})
	var releaseOnce sync.Once
	releaseWrite := func() { releaseOnce.Do(func() { close(writeGate) }) }
	defer releaseWrite()
	writeStarted := make(chan struct{})
	firstPair.server.blockNextWrite(writeStarted, writeGate)
	first.request <- agentgateway.RescueIdentity{Nonce: bytesOf(0x11, 32), Client: key.PublicKey().Marshal(),
		DisplayName: "Operator", Organization: "O2 CONNECTIVE", Profile: agentgateway.RescueProfile}
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("identity WSS write did not block")
	}
	close(first.attemptDone)

	secondWS, err := dialAttemptCloseWS(url, key, agentgateway.RescuePurposeResume, first.reception.ID, transport)
	if err != nil {
		_ = firstWS.CloseNow()
		t.Fatalf("open resumed WSS: %v", err)
	}
	defer secondWS.CloseNow()
	second := <-registry.opened
	secondPair := <-transport.pairs
	_ = second
	_ = secondPair
	openedAfterClose := registry.secondOpenedAfterEnd.Load()
	if !openedAfterClose {
		releaseWrite()
		_ = firstWS.CloseNow()
	}
	select {
	case <-firstPair.server.closedCh:
	case <-time.After(time.Second):
		releaseWrite()
		_ = firstWS.CloseNow()
	}
	select {
	case <-firstPair.served:
	case <-time.After(time.Second):
		releaseWrite()
		_ = firstWS.CloseNow()
	}
	if !openedAfterClose {
		t.Fatal("next resume opened while the previous WSS write was still blocked")
	}
}

func TestAttemptDoneClosesWSSDuringSSHSetupBeforeResume(t *testing.T) {
	registry := &attemptCloseRegistry{opened: make(chan *attemptCloseAttempt, 2), answerStarted: make(chan struct{}), answerDone: make(chan error, 1)}
	handler, err := NewRescueHandler(registry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := handler.Close(ctx); err != nil {
			t.Errorf("RescueHandler.Close = %v", err)
		}
	}()
	transport := newMemTransport(handler)
	url := "ws://gateway.example.invalid" + agentgateway.RescuePath
	key, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytesOf(0x52, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	firstWS, err := dialAttemptCloseWS(url, key, agentgateway.RescuePurposeNew, nil, transport)
	if err != nil {
		t.Fatalf("open first WSS: %v", err)
	}
	defer firstWS.CloseNow()
	first := <-registry.opened
	firstPair := <-transport.pairs
	registry.mu.Lock()
	registry.firstConnClosed = firstPair.server.closedCh
	registry.mu.Unlock()
	first.request <- agentgateway.RescueIdentity{Nonce: bytesOf(0x12, 32), Client: key.PublicKey().Marshal(),
		DisplayName: "Operator", Organization: "O2 CONNECTIVE", Profile: agentgateway.RescueProfile}
	if _, err := readAttemptWSMessage(firstWS); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	answer, _ := json.Marshal(map[string]string{"type": "answer", "answer": "deny",
		"signature": base64.RawURLEncoding.EncodeToString(bytesOf(0x32, ed25519.SignatureSize))})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := firstWS.Write(ctx, websocket.MessageText, answer); err != nil {
		cancel()
		t.Fatalf("send answer: %v", err)
	}
	cancel()
	select {
	case <-registry.answerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not enter AnswerRescue's SSH setup")
	}
	close(first.attemptDone)
	secondWS, err := dialAttemptCloseWS(url, key, agentgateway.RescuePurposeResume, first.reception.ID, transport)
	if err != nil {
		t.Fatalf("open resumed WSS: %v", err)
	}
	defer secondWS.CloseNow()
	_, _ = <-registry.opened, <-transport.pairs
	select {
	case err := <-registry.answerDone:
		if err == nil {
			t.Fatal("SSH setup read succeeded after AttemptDone")
		}
	case <-time.After(time.Second):
		t.Fatal("AttemptDone did not interrupt the SSH setup")
	}
	if !registry.secondOpenedAfterEnd.Load() {
		t.Fatal("next resume opened while the previous WSS SSH setup was still blocked")
	}
}

func dialAttemptCloseWS(url string, key ssh.Signer, purpose string, reception []byte, transport *memTransport) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, Subprotocols: []string{RescueSubprotocol}})
	if err != nil {
		return nil, err
	}
	write := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return ws.Write(ctx, websocket.MessageText, data)
	}
	read := func() (map[string]string, error) {
		kind, data, err := ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if kind != websocket.MessageText {
			return nil, ErrRefused
		}
		var value map[string]string
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		return value, nil
	}
	enc := base64.RawURLEncoding
	if err := write(map[string]string{"type": "hello", "nonce": enc.EncodeToString(bytesOf(0x21, NonceSize))}); err != nil {
		_ = ws.CloseNow()
		return nil, err
	}
	if _, err := read(); err != nil { // challenge
		_ = ws.CloseNow()
		return nil, err
	}
	proof := map[string]string{"type": "proof", "purpose": purpose,
		"key": enc.EncodeToString(key.PublicKey().Marshal()), "signature": enc.EncodeToString(bytesOf(0x31, ed25519.SignatureSize))}
	if len(reception) != 0 {
		proof["reception"] = enc.EncodeToString(reception)
	}
	if err := write(proof); err != nil {
		_ = ws.CloseNow()
		return nil, err
	}
	response, err := read()
	if err != nil {
		_ = ws.CloseNow()
		return nil, err
	}
	if response["type"] != "reception" {
		_ = ws.CloseNow()
		return nil, ErrRefused
	}
	return ws, nil
}

func readAttemptWSMessage(ws *websocket.Conn) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	kind, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageText {
		return nil, ErrRefused
	}
	var message map[string]string
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, err
	}
	return message, nil
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for i := range result {
		result[i] = value
	}
	return result
}
