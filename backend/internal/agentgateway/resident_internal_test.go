package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

type residentRunCall struct {
	generation, id string
	deadline       time.Time
	scope          ResidentRunScope
}
type residentControlFixture struct {
	begin   chan struct{}
	runs    chan residentRunCall
	rotated chan string // request IDs reported as the last collected of a generation
}

func (c *residentControlFixture) RotateResident(_, _, _, id string) {
	select {
	case c.rotated <- id:
	default:
	}
}

func (c *residentControlFixture) BeginResidentRun(ctx context.Context, _ string, _ string, generation, id string, deadline time.Time, scope ResidentRunScope) error {
	c.runs <- residentRunCall{generation, id, deadline, scope}
	return c.BeginResidentFixed(ctx, "", "", generation, id, deadline)
}

func (*residentControlFixture) ResidentGeneration(string, string) string {
	return strings.Repeat("a", 64)
}
func (c *residentControlFixture) BeginResidentFixed(ctx context.Context, _ string, _ string, _ string, _ string, _ time.Time) error {
	select {
	case <-c.begin:
		return ErrResidentWorkUnconfigured
	case <-ctx.Done():
		return ctx.Err()
	}
}

type failingResidentConn struct {
	read, closed, started chan struct{}
	once                  sync.Once
	closeErr              error
	closes                atomic.Int32
}

func (c *failingResidentConn) Read([]byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	<-c.read
	return 0, io.EOF
}
func (*failingResidentConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *failingResidentConn) Close() error                   { c.closes.Add(1); <-c.closed; return c.closeErr }
func (*failingResidentConn) LocalAddr() net.Addr              { return fixtureResidentAddr{} }
func (*failingResidentConn) RemoteAddr() net.Addr             { return fixtureResidentAddr{} }
func (*failingResidentConn) SetDeadline(time.Time) error      { return nil }
func (*failingResidentConn) SetReadDeadline(time.Time) error  { return nil }
func (*failingResidentConn) SetWriteDeadline(time.Time) error { return nil }

type fixtureResidentAddr struct{}

func (fixtureResidentAddr) Network() string { return "memory" }
func (fixtureResidentAddr) String() string  { return "memory" }
func residentEvidenceRig(t *testing.T) (*Gateway, *residentSession, ssh.Signer, *residentControlFixture) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	verifier, e := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test/mcp", Scope: "scope", Types: []string{"at+jwt"}, JWKS: jwks})
	if e != nil {
		t.Fatal(e)
	}
	signer, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)))
	host, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)))
	budgets := agentchannel.Budgets{Handshake: time.Second, HeartbeatInterval: 10 * time.Millisecond, HeartbeatTimeout: 20 * time.Millisecond, ReadStall: 100 * time.Millisecond, WriteStall: 50 * time.Millisecond, Cleanup: 50 * time.Millisecond}
	g, e := New(Config{Verifier: verifier, Principals: []Principal{{Label: "member1", Subject: "subject", Signer: signer}}, Budgets: budgets})
	if e != nil {
		t.Fatal(e)
	}
	control := &residentControlFixture{begin: make(chan struct{}), runs: make(chan residentRunCall, 8), rotated: make(chan string, 8)}
	e = g.ConfigureResident("wss://example.test/register", control, []ResidentEnrollment{{HostLabel: "host1", PrincipalLabel: "member1", HostKey: host.PublicKey().Marshal()}})
	if e != nil {
		t.Fatal(e)
	}
	p := g.byLabel["member1"]
	return g, &residentSession{gateway: g, principal: p, entry: g.resident.entries[p], generation: control.ResidentGeneration("", "")}, host, control
}
func signedCollectionFixture(t *testing.T, g *Gateway, s *residentSession, host ssh.Signer, id string) ResidentCollectionProof {
	t.Helper()
	g.mu.Lock()
	b := s.entry.works[id].binding
	g.mu.Unlock()
	b.Sequence = 1
	proof := ResidentCollectionProof{Binding: b, Endpoint: "wss://example.test/register", ClientNonce: bytes.Repeat([]byte{1}, 32), ServerNonce: bytes.Repeat([]byte{2}, 32), HostKey: host.PublicKey().Marshal(), PrincipalKey: s.principal.signer.PublicKey().Marshal(), Cleanup: "confirmed"}
	sig, e := host.Sign(nil, ResidentCollectionTranscript(proof))
	if e != nil {
		t.Fatal(e)
	}
	proof.Signature = ssh.Marshal(sig)
	return proof
}
func TestResidentLateFailedAcquisitionNeverUsesHostCleanupAsGatewayProof(t *testing.T) {
	t.Run("mixed", func(t *testing.T) { residentLateFailedAcquisition(t, false) })
	t.Run("resident_only", func(t *testing.T) { residentLateFailedAcquisition(t, true) })
}

func residentLateFailedAcquisition(t *testing.T, residentOnly bool) {
	g, s, host, control := residentEvidenceRig(t)
	g.residentOnly = residentOnly
	id := strings.Repeat("1", 32)
	s.FixedStart(context.Background(), id)
	conn := &failingResidentConn{read: make(chan struct{}), closed: make(chan struct{}), started: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-conn.read:
		default:
			close(conn.read)
		}
		select {
		case <-conn.closed:
		default:
			close(conn.closed)
		}
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = g.Close(ctx)
	})
	g.mu.Lock()
	b := s.entry.works[id].binding
	g.mu.Unlock()
	b.Sequence = 1
	d := Dispatch{Binding: agentchannel.Binding{Grant: supervisor.Binding{Generation: 1, Identity: supervisor.Identity{LaunchID: "launch-1", Epoch: "epoch-1", PolicyDigest: agentexec.MacCurrentUserPolicyDigest()}}, Profile: agentexec.ProfileMacCurrentUser, KeySetDigest: [32]byte{1}}, Deadline: time.UnixMilli(b.DeadlineMillis), HostKey: host.PublicKey().Marshal()}
	// A registration that names a command run must not attach to work the
	// gateway recorded as fixed, or it would spend that work's one registration.
	run := b
	run.Run = ResidentRunScope{CommandSHA256: strings.Repeat("0", 64), Cwd: ".", TimeoutMillis: 60000}
	left, right := net.Pipe()
	defer right.Close()
	refused := make(chan error, 1)
	go func() { refused <- g.ConnectResident("member1", d, run, left) }()
	select {
	case e := <-refused:
		if !errors.Is(e, ErrRefused) {
			t.Fatal("command-run registration attached to fixed work", e)
		}
	case <-time.After(time.Second):
		t.Fatal("command-run registration attached to fixed work")
	}
	attempted := make(chan error, 1)
	go func() { attempted <- g.ConnectResident("member1", d, b, conn) }()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("real SSH acquisition never started")
	}
	proof := signedCollectionFixture(t, g, s, host, id)
	if e := g.ResidentCollected(proof); e != nil {
		t.Fatal(e)
	}
	close(control.begin) // contradictory refusal after signed collection: no double-close
	time.Sleep(20 * time.Millisecond)
	if v := s.FixedStatus(context.Background(), id); v["gateway_cleanup"] != "pending" {
		t.Fatal("late acquisition declared collected", v)
	}
	close(conn.read) // handshake fails; original raw Close remains blocked
	select {
	case e := <-attempted:
		if !errors.Is(e, agentchannel.ErrCleanupUnknown) {
			t.Fatal("missing actual partial cleanup failure", e)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded failed acquisition did not return")
	}
	deadline := time.Now().Add(time.Second)
	var v map[string]any
	for time.Now().Before(deadline) {
		v = s.FixedStatus(context.Background(), id)
		if v["phase"] == "unknown" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if v["gateway_cleanup"] != "unknown" || v["owner_cleanup"] != "confirmed" || v["phase"] != "unknown" {
		t.Fatal("host collection upgraded gateway unknown", v)
	}
	if next := s.FixedStart(context.Background(), strings.Repeat("2", 32)); next["error"] != "busy_or_refused" {
		t.Fatal("unknown allowed replacement", next)
	}
}
func TestResidentCollectionRejectsAlteredProofAndKeepsSinglePublication(t *testing.T) {
	g, s, host, control := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	s.FixedStart(context.Background(), id)
	t.Cleanup(func() {
		select {
		case <-control.begin:
		default:
			close(control.begin)
		}
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = g.Close(ctx)
	})
	for _, kind := range []string{"signature", "principal", "host", "generation", "request", "deadline", "sequence", "cleanup", "endpoint", "host-key", "principal-key"} {
		t.Run(kind, func(t *testing.T) {
			p := signedCollectionFixture(t, g, s, host, id)
			switch kind {
			case "signature":
				p.Signature[0] ^= 1
			case "principal":
				p.Binding.Principal = "other"
			case "host":
				p.Binding.Host = "other"
			case "generation":
				p.Binding.Generation = strings.Repeat("b", 64)
			case "request":
				p.Binding.RequestID = strings.Repeat("2", 32)
			case "deadline":
				p.Binding.DeadlineMillis++
			case "sequence":
				p.Binding.Sequence = 33
			case "cleanup":
				p.Cleanup = "approved"
			case "endpoint":
				p.Endpoint = "wss://other.test/register"
			case "host-key":
				p.HostKey = bytes.Repeat([]byte{1}, 32)
			case "principal-key":
				p.PrincipalKey = bytes.Repeat([]byte{2}, 32)
			}
			if e := g.ResidentCollected(p); !errors.Is(e, ErrRefused) {
				t.Fatal("altered evidence accepted", e)
			}
		})
	}
	p := signedCollectionFixture(t, g, s, host, id)
	// A correctly signed command-run proof must not collect work the gateway
	// recorded as fixed: the two kinds never stand in for each other.
	run := p
	run.Binding.Run = ResidentRunScope{CommandSHA256: strings.Repeat("0", 64), Cwd: ".", TimeoutMillis: 60000}
	sig, e := host.Sign(nil, ResidentCollectionTranscript(run))
	if e != nil {
		t.Fatal(e)
	}
	run.Signature = ssh.Marshal(sig)
	if e := g.ResidentCollected(run); !errors.Is(e, ErrRefused) {
		t.Fatal("command-run proof collected fixed work", e)
	}
	if e := g.ResidentCollected(p); e != nil {
		t.Fatal(e)
	}
	if e := g.ResidentCollected(p); !errors.Is(e, ErrRefused) {
		t.Fatal("receipt replay accepted", e)
	}
	close(control.begin)
}

func TestResidentPreOpenRefusalPreservesOriginalFailedStreamClose(t *testing.T) {
	g, s, host, control := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	s.FixedStart(context.Background(), id)
	legacy := &failingResidentConn{read: make(chan struct{}), closed: make(chan struct{}), started: make(chan struct{})}
	close(legacy.closed)
	refused := &failingResidentConn{read: make(chan struct{}), closed: make(chan struct{}), started: make(chan struct{}), closeErr: errors.New("synthetic original refused stream close failure")}
	close(refused.closed)
	t.Cleanup(func() {
		select {
		case <-legacy.read:
		default:
			close(legacy.read)
		}
		select {
		case <-control.begin:
		default:
			close(control.begin)
		}
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = g.Close(ctx)
	})
	g.mu.Lock()
	b := s.entry.works[id].binding
	g.mu.Unlock()
	b.Sequence = 1
	d := Dispatch{Binding: agentchannel.Binding{Grant: supervisor.Binding{Generation: 1, Identity: supervisor.Identity{LaunchID: "other-launch", Epoch: "epoch-1", PolicyDigest: agentexec.MacCurrentUserPolicyDigest()}}, Profile: agentexec.ProfileMacCurrentUser, KeySetDigest: [32]byte{1}}, Deadline: time.UnixMilli(b.DeadlineMillis), HostKey: host.PublicKey().Marshal()}
	other := make(chan error, 1)
	go func() { other <- g.Connect("member1", d, legacy) }()
	select {
	case <-legacy.started:
	case <-time.After(time.Second):
		t.Fatal("competing original acquisition not entered")
	}
	d.Binding.Grant.Identity.LaunchID = "resident-launch"
	if e := g.ConnectResident("member1", d, b, refused); !errors.Is(e, agentchannel.ErrCleanupUnknown) {
		t.Fatal("pre-open close failure lost", e)
	}
	if refused.closes.Load() != 1 {
		t.Fatal("original refused stream close repeated", refused.closes.Load())
	}
	if e := g.ResidentCollected(signedCollectionFixture(t, g, s, host, id)); e != nil {
		t.Fatal(e)
	}
	close(control.begin)
	deadline := time.Now().Add(time.Second)
	var result map[string]any
	for time.Now().Before(deadline) {
		result = s.FixedStatus(context.Background(), id)
		if result["phase"] == "unknown" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if result["owner_cleanup"] != "confirmed" || result["gateway_cleanup"] != "unknown" {
		t.Fatal("host collection masked pre-open close failure", result)
	}
	if next := s.FixedStart(context.Background(), strings.Repeat("2", 32)); next["error"] != "busy_or_refused" {
		t.Fatal(next)
	}
	close(legacy.read)
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("competing original not joined")
	}
}
func TestResidentRetainedSessionCannotReadCancelOrReplayAnotherGeneration(t *testing.T) {
	g, old, _, control := residentEvidenceRig(t)
	t.Cleanup(func() {
		close(control.begin)
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = g.Close(ctx)
	})
	id := strings.Repeat("1", 32)
	replacement := &residentWork{binding: ResidentWorkBinding{Generation: strings.Repeat("b", 64), RequestID: id}, phase: "pending", wake: make(chan struct{}, 1)}
	g.mu.Lock()
	old.entry.works[id] = replacement
	g.mu.Unlock()
	if v := old.FixedStatus(context.Background(), id); v["error"] != "not_found" {
		t.Fatal("stale generation read result", v)
	}
	if v := old.FixedCancel(context.Background(), id); v["error"] != "not_found" {
		t.Fatal("stale generation cancelled replacement", v)
	}
	if v := old.FixedStart(context.Background(), id); v["error"] != "request_generation_mismatch" {
		t.Fatal("stale replay rebound", v)
	}
	g.mu.Lock()
	cancelled := replacement.cancelled
	g.mu.Unlock()
	if cancelled {
		t.Fatal("rejected stale cancel mutated work")
	}
	newer := *old
	newer.generation = replacement.binding.Generation
	if v := newer.FixedStatus(context.Background(), id); v["request_id"] != id {
		t.Fatal("actual owning generation cannot read", v)
	}
}

func TestResidentOnlyRejectsGenericRegistrationAndClosesStream(t *testing.T) {
	g, _, _, _ := residentEvidenceRig(t)
	g.residentOnly = true // before registration or any session
	c := &failingResidentConn{closed: make(chan struct{})}
	close(c.closed)
	if err := g.Connect("member1", Dispatch{}, c); !errors.Is(err, ErrRefused) || c.closes.Load() != 1 {
		t.Fatal("generic registration not refused and closed", err)
	}
	if len(g.launches) != 0 || g.byLabel["member1"].route != nil {
		t.Fatal("generic registration consumed state")
	}
}
