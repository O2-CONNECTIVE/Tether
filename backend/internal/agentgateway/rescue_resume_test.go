package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

func prepareAnsweredReception(t *testing.T, g *Gateway, k ssh.Signer) (RescueReception, ed25519.PrivateKey) {
	t.Helper()
	res, err := g.OpenRescue(newRescueRequest(t, g, k))
	if err != nil {
		t.Fatalf("OpenRescue: %v", err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g.rescue.mu.Lock()
	record := g.rescue.records[string(res.ID)]
	record.principal = g.byLabel["op1"]
	record.clientKey = clientKey
	record.answered = true
	g.rescue.mu.Unlock()
	g.TouchRescue(res.ID)
	g.DisconnectRescue(res.ID)
	return res, clientKey
}

func resumeRequest(t *testing.T, g *Gateway, k ssh.Signer, id []byte) RescueRequest {
	t.Helper()
	r := RescueRequest{Key: k.PublicKey().Marshal(), HostNonce: rescueNonce(0x78), GatewayNonce: rescueNonce(0x79), Purpose: RescuePurposeResume, Reception: append([]byte(nil), id...)}
	r.Proof = rescueSign(t, k, RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, r.Reception))
	return r
}

func resumeDispatch(t *testing.T, generation uint64, hostKey ssh.Signer, clientKey ssh.PublicKey) Dispatch {
	t.Helper()
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{clientKey})
	if err != nil {
		t.Fatal(err)
	}
	return Dispatch{
		Binding: agentchannel.Binding{
			Grant: supervisor.Binding{Generation: 7, Identity: supervisor.Identity{
				LaunchID: "launch-425", Epoch: "epoch-425", PolicyDigest: agentexec.MacCurrentUserPolicyDigest(),
			}},
			ConnectionGeneration: generation, KeySetDigest: digest, Profile: agentexec.ProfileMacCurrentUser,
		},
		Deadline: time.Now().Add(time.Hour), HostKey: hostKey.PublicKey().Marshal(),
	}
}

func startFakeChannelHost(t *testing.T, conn net.Conn, hostKey ssh.Signer, clientKey ssh.PublicKey, launchID string) <-chan []byte {
	t.Helper()
	opened := make(chan []byte, 1)
	go func() {
		config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != launchID || !bytes.Equal(key.Marshal(), clientKey.Marshal()) {
				return nil, ErrRefused
			}
			return nil, nil
		}}
		config.AddHostKey(hostKey)
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer server.Close()
		go func() {
			for request := range requests {
				if request.WantReply {
					ok := request.Type == agentchannel.HeartbeatRequest && len(request.Payload) == 0
					_ = request.Reply(ok, nil)
				}
			}
		}()
		for open := range channels {
			if open.ChannelType() != agentchannel.ChannelType {
				_ = open.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			ch, channelRequests, err := open.Accept()
			if err != nil {
				return
			}
			opened <- append([]byte(nil), open.ExtraData()...)
			go func() {
				for request := range channelRequests {
					if request.WantReply {
						_ = request.Reply(false, nil)
					}
				}
			}()
			go func() { _, _ = io.Copy(io.Discard, ch); _ = ch.Close() }()
		}
	}()
	return opened
}

type resumeMemConn struct {
	incoming  <-chan []byte
	outgoing  chan<- []byte
	closed    chan struct{}
	peerClose <-chan struct{}
	closeOnce sync.Once
	rest      []byte
}

func resumeMemPair() (*resumeMemConn, *resumeMemConn) {
	ab, ba := make(chan []byte, 128), make(chan []byte, 128)
	closedA, closedB := make(chan struct{}), make(chan struct{})
	return &resumeMemConn{incoming: ba, outgoing: ab, closed: closedA, peerClose: closedB},
		&resumeMemConn{incoming: ab, outgoing: ba, closed: closedB, peerClose: closedA}
}

func (c *resumeMemConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.rest) == 0 {
		select {
		case msg := <-c.incoming:
			c.rest = msg
		default:
			select {
			case msg := <-c.incoming:
				c.rest = msg
			case <-c.peerClose:
				return 0, io.EOF
			case <-c.closed:
				return 0, net.ErrClosed
			}
		}
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *resumeMemConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	msg := append([]byte(nil), p...)
	select {
	case c.outgoing <- msg:
		return len(p), nil
	case <-c.closed:
		return 0, net.ErrClosed
	case <-c.peerClose:
		return 0, io.ErrClosedPipe
	}
}

func (c *resumeMemConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *resumeMemConn) LocalAddr() net.Addr            { return resumeMemAddr{} }
func (c *resumeMemConn) RemoteAddr() net.Addr           { return resumeMemAddr{} }
func (*resumeMemConn) SetDeadline(time.Time) error      { return nil }
func (*resumeMemConn) SetReadDeadline(time.Time) error  { return nil }
func (*resumeMemConn) SetWriteDeadline(time.Time) error { return nil }

type resumeMemAddr struct{}

func (resumeMemAddr) Network() string { return "memory" }
func (resumeMemAddr) String() string  { return "memory" }

func seedRescueAnswer(t *testing.T, g *Gateway, k ssh.Signer, clientPrivate ed25519.PrivateKey, d Dispatch) (RescueReception, RescueIdentity, *rescueReceptionRecord) {
	t.Helper()
	res, err := g.OpenRescue(newRescueRequest(t, g, k))
	if err != nil {
		t.Fatalf("OpenRescue: %v", err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	identity := RescueIdentity{Nonce: rescueNonce(0x65), Client: clientSigner.PublicKey().Marshal(),
		DisplayName: "Operator", Organization: "O2 CONNECTIVE", Profile: RescueProfile}
	transcript := RescueRequestTranscript(g.RescueEndpoint(), res.ID, k.PublicKey().Marshal(), identity.Nonce, identity.Client,
		identity.DisplayName, identity.Organization, identity.Profile)
	identity.Signature = rescueSign(t, g.rescueSigner, transcript)
	digest := sha256.Sum256(transcript)
	g.rescue.mu.Lock()
	record := g.rescue.records[string(res.ID)]
	record.principal = g.byLabel["op1"]
	record.clientKey = append(ed25519.PrivateKey(nil), clientPrivate...)
	record.nonce = append([]byte(nil), identity.Nonce...)
	record.digest = append([]byte(nil), digest[:]...)
	record.requested = rescueClockTime(g.clock())
	record.dispatch = cloneRescueDispatch(d)
	record.awaitingAnswer = true
	g.rescue.mu.Unlock()
	return res, identity, record
}

func cloneRescueDispatch(d Dispatch) Dispatch {
	d.HostKey = append([]byte(nil), d.HostKey...)
	return d
}

func newClientPrivateKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return private, signer.PublicKey()
}

func newResumeGateway(t *testing.T, clock func() time.Time, anotherPrincipal bool) *Gateway {
	t.Helper()
	base := time.Now()
	cfg := rescueReceptionConfig(t, &base)
	cfg.Clock = clock
	cfg.Budgets = ChannelBudgets()
	if anotherPrincipal {
		cfg.Principals = append(cfg.Principals, Principal{Label: "op2", Subject: "s2", Roles: []Role{RoleRescueOperator}, Signer: rescueTestSigner(0x22)})
	}
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := g.Close(ctx); err != nil {
			t.Errorf("Gateway.Close: %v", err)
		}
	})
	return g
}

func rescueTestSigner(seed byte) ssh.Signer {
	return mustRescueSigner(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
}

func signAllowedAnswer(t *testing.T, g *Gateway, k ssh.Signer, id []byte, identity RescueIdentity, d Dispatch) RescueAnswer {
	t.Helper()
	transcript := RescueRequestTranscript(g.RescueEndpoint(), id, k.PublicKey().Marshal(), identity.Nonce,
		identity.Client, identity.DisplayName, identity.Organization, identity.Profile)
	digest := sha256.Sum256(transcript)
	answer := RescueAllow
	return RescueAnswer{Answer: answer, Dispatch: d, Signature: rescueSign(t, k,
		RescueAnswerTranscript(id, identity.Nonce, digest[:], answer, d))}
}

func TestResumeReusesAnsweredReceptionAndClientKey(t *testing.T) {
	clock := newRescueExpiryClock(time.Now())
	cfg := rescueReceptionConfig(t, new(time.Time))
	cfg.Clock = clock.Now
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := g.Close(ctx); err != nil {
			t.Errorf("Gateway.Close: %v", err)
		}
	})
	k := rescueHostKey(20)
	first, clientKey := prepareAnsweredReception(t, g, k)
	resume, err := g.OpenRescue(resumeRequest(t, g, k, first.ID))
	if err != nil {
		t.Fatalf("OpenRescue(resume) = %v; want same reception", err)
	}
	if !bytes.Equal(resume.ID, first.ID) || resume.Code != "" || resume.Done != first.Done || resume.Request != first.Request {
		t.Fatalf("resumed reception = %+v; want same id, request and done, with no new code", resume)
	}
	identity := <-resume.Request
	client, err := ssh.ParsePublicKey(identity.Client)
	if err != nil || !bytes.Equal(client.Marshal(), func() []byte {
		signer, signerErr := ssh.NewSignerFromKey(clientKey)
		if signerErr != nil {
			t.Fatal(signerErr)
		}
		return signer.PublicKey().Marshal()
	}()) {
		t.Fatalf("resumed C = %x; want the reception's existing client key", identity.Client)
	}
	transcript := RescueRequestTranscript(g.RescueEndpoint(), first.ID, k.PublicKey().Marshal(), identity.Nonce, identity.Client, identity.DisplayName, identity.Organization, identity.Profile)
	if err := rescueIdentity.PublicKey().Verify(transcript, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: identity.Signature}); err != nil {
		t.Fatalf("resumed request signature = %v", err)
	}
}

func TestResumeGraceHasNoSlackAndClearsTheClientKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
		accept  bool
	}{
		{"4m59s", 4*time.Minute + 59*time.Second, true},
		{"5m", 5 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newRescueExpiryClock(time.Now())
			cfg := rescueReceptionConfig(t, new(time.Time))
			cfg.Clock = clock.Now
			g, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := g.Close(ctx); err != nil {
					t.Errorf("Gateway.Close: %v", err)
				}
			})
			k := rescueHostKey(21)
			first, _ := prepareAnsweredReception(t, g, k)
			clock.Add(time.Minute)
			g.TouchRescue(first.ID)
			g.rescue.mu.Lock()
			record := g.rescue.records[string(first.ID)]
			g.rescue.mu.Unlock()
			clock.Add(tc.advance)
			if tc.accept {
				resumed, err := g.OpenRescue(resumeRequest(t, g, k, first.ID))
				if err != nil || !bytes.Equal(resumed.ID, first.ID) {
					t.Fatalf("resume at %v = %+v, %v; want the same reception", tc.advance, resumed, err)
				}
				return
			}
			waitRescueDone(t, first.Done, "grace expiry")
			g.rescue.mu.Lock()
			remaining := len(g.rescue.records)
			keyCleared := record.clientKey == nil
			g.rescue.mu.Unlock()
			resumed, err := g.OpenRescue(resumeRequest(t, g, k, first.ID))
			if !errors.Is(err, ErrRefused) || remaining != 0 || !keyCleared {
				t.Fatalf("resume at %v = %+v, %v with %d records and keyCleared=%v; want refusal after immediate expiry", tc.advance, resumed, err, remaining, keyCleared)
			}
		})
	}
}

func TestGatewayConnectRejectsAnotherPrincipalsResume(t *testing.T) {
	clock := newRescueExpiryClock(time.Now())
	g := newResumeGateway(t, clock.Now, true)
	key := rescueHostKey(22)
	otherClient := rescueTestSigner(0x22)
	d := resumeDispatch(t, 1, key, otherClient.PublicKey())
	res := openRescue(t, g, 22)
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g.rescue.mu.Lock()
	record := g.rescue.records[string(res.ID)]
	record.principal = g.byLabel["op1"]
	record.clientKey = clientPrivate
	record.answered = true
	record.connectionGeneration = 1
	record.dispatch = cloneRescueDispatch(d)
	g.rescue.mu.Unlock()
	g.TouchRescue(res.ID)
	g.DisconnectRescue(res.ID)

	clientConn, hostConn := resumeMemPair()
	opened := startFakeChannelHost(t, hostConn, key, otherClient.PublicKey(), d.Binding.Grant.Identity.LaunchID)
	err = g.Connect("op2", d, clientConn)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Connect for another principal = %v; want ErrRefused", err)
	}
	select {
	case data := <-opened:
		t.Fatalf("another principal opened channel with commitment %x", data)
	default:
	}
}

func TestDisconnectBeforeAllowDropsReceptionImmediately(t *testing.T) {
	clock := newRescueExpiryClock(time.Now())
	g := newResumeGateway(t, clock.Now, false)
	res := openRescue(t, g, 27)
	g.TouchRescue(res.ID)
	g.DisconnectRescue(res.ID)
	waitRescueDone(t, res.Done, "pre-allow disconnect")
	g.rescue.mu.Lock()
	_, retained := g.rescue.records[string(res.ID)]
	g.rescue.mu.Unlock()
	if retained {
		t.Fatal("pre-allow disconnect retained the reception")
	}
}

func TestHostEndDropsAnsweredReceptionImmediately(t *testing.T) {
	clock := newRescueExpiryClock(time.Now())
	g := newResumeGateway(t, clock.Now, false)
	res, clientKey := prepareAnsweredReception(t, g, rescueHostKey(28))
	g.EndRescue(res.ID)
	waitRescueDone(t, res.Done, "host end")
	if !bytes.Equal(clientKey, make([]byte, len(clientKey))) {
		t.Fatal("host end did not clear C")
	}
}

func TestResumeAfter459SecondsOpensNextChannel(t *testing.T) {
	clock := newRescueExpiryClock(time.Now())
	g := newResumeGateway(t, clock.Now, false)
	key := rescueHostKey(23)
	clientPrivate, clientPublic := newClientPrivateKey(t)
	d1 := resumeDispatch(t, 1, key, clientPublic)
	first, identity, record := seedRescueAnswer(t, g, key, clientPrivate, d1)

	client1, host1 := resumeMemPair()
	opened1 := startFakeChannelHost(t, host1, key, clientPublic, d1.Binding.Grant.Identity.LaunchID)
	answer1 := signAllowedAnswer(t, g, key, first.ID, identity, d1)
	if err := g.AnswerRescue(first.ID, answer1, client1); err != nil {
		t.Fatalf("generation 1 AnswerRescue = %v", err)
	}
	if data := <-opened1; len(data) != 32 {
		t.Fatalf("generation 1 commitment length = %d; want 32", len(data))
	}
	route1 := g.byLabel["op1"].route
	if route1 == nil || route1.binding.ConnectionGeneration != 1 {
		t.Fatalf("generation 1 route = %+v", route1)
	}
	if record.connectionGeneration != 1 || record.route != route1 {
		t.Fatalf("reception generation/route = %d/%p; want 1/%p", record.connectionGeneration, record.route, route1)
	}
	_ = host1.Close()
	g.TouchRescue(first.ID)
	g.DisconnectRescue(first.ID) // generation 1 transport ended without the explicit host-end frame
	g.rescue.mu.Lock()
	originalClient := append(ed25519.PrivateKey(nil), record.clientKey...)
	g.rescue.mu.Unlock()
	clock.Add(4*time.Minute + 59*time.Second)

	resumed, err := g.OpenRescue(resumeRequest(t, g, key, first.ID))
	if err != nil {
		t.Fatalf("OpenRescue(resume) at 4m59s = %v", err)
	}
	select {
	case <-route1.cleanupDone:
	case <-time.After(3 * time.Second):
		t.Fatal("generation 1 route cleanup did not finish before resume")
	}
	duplicateClient, duplicateHost := resumeMemPair()
	duplicateSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	duplicateOpened := startFakeChannelHost(t, duplicateHost, key, clientPublic, d1.Binding.Grant.Identity.LaunchID)
	if _, err := g.connectWithSigner("op1", d1, duplicateClient, true, duplicateSigner, true); !errors.Is(err, ErrRefused) {
		t.Fatalf("second channel for (LaunchID, generation 1) = %v; want ErrRefused", err)
	}
	select {
	case data := <-duplicateOpened:
		t.Fatalf("duplicate generation opened channel with commitment %x", data)
	default:
	}
	_ = duplicateHost.Close()
	if !bytes.Equal(resumed.ID, first.ID) || resumed.Code != "" || resumed.Done != first.Done {
		t.Fatalf("resume reception = %+v; want same id/done and no new code", resumed)
	}
	identity2 := <-resumed.Request
	if !bytes.Equal(identity2.Client, clientPublic.Marshal()) {
		t.Fatalf("resume C = %x; want the same reception client key", identity2.Client)
	}
	d2 := cloneRescueDispatch(d1)
	d2.Binding.ConnectionGeneration = 2
	client2, host2 := resumeMemPair()
	opened2 := startFakeChannelHost(t, host2, key, clientPublic, d2.Binding.Grant.Identity.LaunchID)
	answer2 := signAllowedAnswer(t, g, key, first.ID, identity2, d2)
	if err := g.AnswerRescue(first.ID, answer2, client2); err != nil {
		t.Fatalf("generation 2 AnswerRescue = %v", err)
	}
	if data := <-opened2; len(data) != 32 {
		t.Fatalf("generation 2 commitment length = %d; want 32", len(data))
	}
	if route := g.byLabel["op1"].route; route == nil || route.binding.ConnectionGeneration != 2 {
		t.Fatalf("generation 2 route = %+v", route)
	}
	g.rescue.mu.Lock()
	keyRetained := bytes.Equal(record.clientKey, originalClient)
	g.rescue.mu.Unlock()
	clear(originalClient)
	if !keyRetained {
		t.Fatal("resume replaced or cleared the reception client key")
	}
	_ = host2.Close()
}

func TestResumeRejectsStaleOrSkippedConnectionGeneration(t *testing.T) {
	for _, generation := range []uint64{1, 3} {
		t.Run(fmt.Sprintf("generation_%d", generation), func(t *testing.T) {
			clock := newRescueExpiryClock(time.Now())
			g := newResumeGateway(t, clock.Now, false)
			key := rescueHostKey(26)
			clientPrivate, clientPublic := newClientPrivateKey(t)
			d1 := resumeDispatch(t, 1, key, clientPublic)
			first, _, record := seedRescueAnswer(t, g, key, clientPrivate, d1)
			g.rescue.mu.Lock()
			record.answered = true
			record.awaitingAnswer = false
			record.connectionGeneration = 1
			g.rescue.mu.Unlock()
			g.TouchRescue(first.ID)
			g.DisconnectRescue(first.ID)
			resumed, err := g.OpenRescue(resumeRequest(t, g, key, first.ID))
			if err != nil {
				t.Fatalf("OpenRescue(resume): %v", err)
			}
			identity := <-resumed.Request
			d2 := cloneRescueDispatch(d1)
			d2.Binding.ConnectionGeneration = generation
			client, host := resumeMemPair()
			opened := startFakeChannelHost(t, host, key, clientPublic, d1.Binding.Grant.Identity.LaunchID)
			err = g.AnswerRescue(first.ID, signAllowedAnswer(t, g, key, first.ID, identity, d2), client)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("generation %d resume answer = %v; want ErrRefused", generation, err)
			}
			select {
			case data := <-opened:
				t.Fatalf("generation %d opened channel with commitment %x", generation, data)
			default:
			}
			g.rescue.mu.Lock()
			_, retained := g.rescue.records[string(first.ID)]
			cleared := record.clientKey == nil
			g.rescue.mu.Unlock()
			if retained || !cleared {
				t.Fatalf("invalid generation left record=%v and client key cleared=%v; want dropped", retained, cleared)
			}
		})
	}
}

func TestResumeKeepsTheHostKeyPinnedAtGenerationTwo(t *testing.T) {
	clock := newRescueExpiryClock(time.Now())
	g := newResumeGateway(t, clock.Now, false)
	key, stranger := rescueHostKey(24), rescueHostKey(25)
	clientPrivate, clientPublic := newClientPrivateKey(t)
	d1 := resumeDispatch(t, 1, key, clientPublic)
	first, _, record := seedRescueAnswer(t, g, key, clientPrivate, d1)
	g.rescue.mu.Lock()
	record.answered = true
	record.awaitingAnswer = false
	record.connectionGeneration = 1
	g.rescue.mu.Unlock()
	g.TouchRescue(first.ID)
	g.DisconnectRescue(first.ID)
	resumed, err := g.OpenRescue(resumeRequest(t, g, key, first.ID))
	if err != nil {
		t.Fatalf("OpenRescue(resume) = %v", err)
	}
	identity := <-resumed.Request
	d2 := cloneRescueDispatch(d1)
	d2.Binding.ConnectionGeneration = 2
	client, host := resumeMemPair()
	opened := startFakeChannelHost(t, host, stranger, clientPublic, d2.Binding.Grant.Identity.LaunchID)
	answer := signAllowedAnswer(t, g, key, first.ID, identity, d2)
	err = g.AnswerRescue(first.ID, answer, client)
	if err == nil {
		t.Fatal("generation 2 accepted a different host key")
	}
	select {
	case data := <-opened:
		t.Fatalf("different host key opened channel with commitment %x", data)
	default:
	}
}
