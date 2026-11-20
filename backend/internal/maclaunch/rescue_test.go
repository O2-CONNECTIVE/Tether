package maclaunch

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

// The host's rescue permission state machine (docs/rescue-connection-start.md,
// 3. 許可と拒否, ホストの状態, 期限とホスト側の切断), driven by a fake native
// helper, a fake reception and fake clocks. A grant exists only after the
// user's answer through the helper, so every refusal test also checks that the
// reception was never asked to send an allow.

// rescueHelper stands in for the native helper's authenticated connection
// (#410). A second instance stands in for a WebView IPC handler.
type rescueHelper struct {
	mu        sync.Mutex
	shows     []RescuePrompt
	withdraws []string
}

func (h *rescueHelper) Show(p RescuePrompt) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shows = append(h.shows, p)
	return nil
}

func (h *rescueHelper) Withdraw(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.withdraws = append(h.withdraws, id)
}

func (h *rescueHelper) shown() []RescuePrompt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]RescuePrompt(nil), h.shows...)
}

func (h *rescueHelper) withdrawn() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.withdraws...)
}

// rescueReception stands in for the reception connection that owns K. Allow
// hands the host one end of an in-memory stream and keeps the other for the
// 担当者's channel client.
type rescueReception struct {
	mu              sync.Mutex
	done            chan struct{}
	requests        chan agenttransport.VerifiedRescueIdentity
	refuses         []bool
	allows          []Dispatch
	client          net.Conn
	closes          int
	resumes         int
	opens           int
	code            string
	key             ssh.Signer
	closeStarted    chan struct{}
	closeReturned   chan struct{}
	closeOnce       sync.Once
	closeDone       chan struct{}
	closeErr        error
	logicallyClosed bool
	onClose         func()
	onResume        func()
	onAllow         func(Dispatch, net.Conn)
	// entered and gate, when set before the answer, hold Allow open as a slow
	// write to the gateway would.
	entered, gate    chan struct{}
	openEntered      chan struct{}
	openGate         chan struct{}
	openCanceled     chan error
	openReturned     chan struct{}
	prepareEntered   chan struct{}
	prepareGate      chan struct{}
	ignoreOpenCancel bool
	acceptRead       chan struct{}
	requestEntered   chan struct{}
	requestGate      chan struct{}
	requestCalls     chan struct{}
	requestCount     int
	requestGateAt    int
}

func newRescueReception() *rescueReception {
	return &rescueReception{done: make(chan struct{}), closeDone: make(chan struct{}), requests: make(chan agenttransport.VerifiedRescueIdentity, 1), requestCalls: make(chan struct{}, 16), code: "123456789012", key: rescueK}
}

func freshRescueReception(seed byte, code string) *rescueReception {
	r := newRescueReception()
	r.code, r.key = code, rescueSigner(seed)
	return r
}

func (r *rescueReception) Prepare() error {
	r.mu.Lock()
	if r.logicallyClosed {
		r.mu.Unlock()
		return agentchannel.ErrClosed
	}
	if r.key == nil {
		r.key = rescueK
	}
	entered, gate := r.prepareEntered, r.prepareGate
	r.mu.Unlock()
	if entered != nil {
		close(entered)
		<-gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.logicallyClosed || r.key == nil {
		return agentchannel.ErrClosed
	}
	return nil
}

func (r *rescueReception) Open(ctx context.Context) (string, error) {
	if r.openEntered != nil {
		close(r.openEntered)
		if r.ignoreOpenCancel {
			<-r.openGate
		} else {
			select {
			case <-r.openGate:
			case <-ctx.Done():
				if r.openCanceled != nil {
					r.openCanceled <- ctx.Err()
				}
				return "", ctx.Err()
			}
		}
	}
	r.mu.Lock()
	r.opens++
	code := r.code
	openReturned := r.openReturned
	r.mu.Unlock()
	if openReturned != nil {
		close(openReturned)
	}
	return code, nil
}
func (r *rescueReception) Resume(context.Context) error {
	r.mu.Lock()
	if r.logicallyClosed {
		r.mu.Unlock()
		return ErrDenied
	}
	r.resumes++
	onResume := r.onResume
	r.mu.Unlock()
	if onResume != nil {
		onResume()
	}
	return nil
}
func (r *rescueReception) Request() <-chan agenttransport.VerifiedRescueIdentity {
	r.mu.Lock()
	r.requestCount++
	call := r.requestCount
	select {
	case r.requestCalls <- struct{}{}:
	default:
	}
	var entered, gate chan struct{}
	if call == r.requestGateAt {
		entered, gate = r.requestEntered, r.requestGate
		r.requestGateAt = 0
	}
	r.mu.Unlock()
	if entered != nil {
		close(entered)
		<-gate
	}
	return r.requests
}

func (r *rescueReception) gateNextRequest(entered, gate chan struct{}) {
	r.mu.Lock()
	r.requestEntered, r.requestGate = entered, gate
	r.requestGateAt = r.requestCount + 1
	r.mu.Unlock()
}
func (r *rescueReception) HostKey() ssh.Signer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.logicallyClosed {
		return nil
	}
	if r.key == nil {
		return rescueK
	}
	return r.key
}

func (r *rescueReception) StartClose() {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.logicallyClosed = true
		r.key = nil
		r.closes++
		onClose, started, returned := r.onClose, r.closeStarted, r.closeReturned
		r.mu.Unlock()
		if started != nil {
			close(started)
		}
		go func() {
			if onClose != nil {
				onClose()
			}
			if returned != nil {
				close(returned)
			}
			close(r.closeDone)
		}()
	})
}

func (r *rescueReception) Done() <-chan struct{} { return r.done }

func (r *rescueReception) Refuse(timedOut bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuses = append(r.refuses, timedOut)
	return nil
}

func (r *rescueReception) Allow(d Dispatch) (net.Conn, error) {
	if r.entered != nil {
		close(r.entered)
		<-r.gate
	}
	r.mu.Lock()
	rawHost, client := memPair()
	var host net.Conn = rawHost
	if r.acceptRead != nil {
		host = &signalReadConn{Conn: host, started: r.acceptRead}
	}
	r.allows = append(r.allows, d)
	r.client = client
	onAllow := r.onAllow
	r.mu.Unlock()
	if onAllow != nil {
		onAllow(d, client)
	}
	return host, nil
}

type signalReadConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *signalReadConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Read(p)
}

func (r *rescueReception) Close() error {
	r.StartClose()
	<-r.closeDone
	return r.closeErr
}

func (r *rescueReception) lose() { close(r.done) }

func (r *rescueReception) state() (refuses []bool, allows []Dispatch, closes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.refuses...), append([]Dispatch(nil), r.allows...), r.closes
}

func (r *rescueReception) hasKey() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.key != nil
}

func (r *rescueReception) clientConn() net.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// rescueClocks moves the monotonic and the wall clock independently, as a
// sleep (wall only) or a clock change (wall back) would.
type rescueClocks struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
	tick chan time.Time
}

func newRescueClocks() *rescueClocks {
	// The issuer and the channel still read the real clock, so start near it.
	return &rescueClocks{wall: time.Now().Round(0), tick: make(chan time.Time)}
}

func (c *rescueClocks) clock() rescueClock {
	return rescueClock{
		wall: func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.wall },
		mono: func() time.Duration { c.mu.Lock(); defer c.mu.Unlock(); return c.mono },
		tick: c.tick,
	}
}

func (c *rescueClocks) advance(mono, wall time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += mono
	c.wall = c.wall.Add(wall)
}

func (c *rescueClocks) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

// recheck delivers one tick to the state machine.
func (c *rescueClocks) recheck(t *testing.T) {
	t.Helper()
	select {
	case c.tick <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("the state machine did not take a tick within 5s")
	}
}

// settle delivers two ticks, so the first one was fully handled before it
// returns. The second one may still be handled after the clocks move next.
func (c *rescueClocks) settle(t *testing.T) {
	t.Helper()
	c.recheck(t)
	c.recheck(t)
}

// recheckEnding delivers one tick after the clocks moved past an end, or none
// if the tick still handled from settle already found that end.
func (c *rescueClocks) recheckEnding(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case c.tick <- time.Now():
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the state machine neither took a tick nor ended within 10s")
	}
}

func rescueSigner(seed byte) ssh.Signer {
	s, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		panic(err)
	}
	return s
}

var (
	rescueK = rescueSigner(0x4b) // the host's one-reception key K
	rescueC = rescueSigner(0x43) // the 担当者's one-request key C
)

func rescueRequestFor(c ssh.PublicKey) RescueRequest {
	return RescueRequest{client: c, displayName: "山田（サポート）", organization: "O2 CONNECTIVE", profile: "mac_current_user"}
}

type rescueUncleanRuntime struct{ started chan struct{} }

func (*rescueUncleanRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }
func (r *rescueUncleanRuntime) StartShell(context.Context, supervisor.Binding, []byte) (agentexec.GuestProcess, error) {
	close(r.started)
	return rescueUncleanProcess{}, nil
}

type rescueUncleanProcess struct{}

func (rescueUncleanProcess) Next(ctx context.Context) (agentexec.Stream, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (rescueUncleanProcess) Wait(ctx context.Context) (agentexec.ExitStatus, error) {
	<-ctx.Done()
	return agentexec.ExitStatus{}, ctx.Err()
}
func (rescueUncleanProcess) Terminate(context.Context) error {
	return errors.New("terminate unconfirmed")
}
func (rescueUncleanProcess) Release(context.Context) error { return errors.New("release unconfirmed") }

type rescueWorkRuntime struct{ started chan struct{} }

func (*rescueWorkRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }
func (r *rescueWorkRuntime) StartShell(context.Context, supervisor.Binding, []byte) (agentexec.GuestProcess, error) {
	close(r.started)
	return rescueWorkProcess{}, nil
}

type rescueWorkProcess struct{}

func (rescueWorkProcess) Next(ctx context.Context) (agentexec.Stream, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}
func (rescueWorkProcess) Wait(ctx context.Context) (agentexec.ExitStatus, error) {
	<-ctx.Done()
	return agentexec.ExitStatus{}, ctx.Err()
}
func (rescueWorkProcess) Terminate(context.Context) error { return nil }
func (rescueWorkProcess) Release(context.Context) error   { return nil }

type rescueFixture struct {
	r            *Rescue
	helper       *rescueHelper
	rec          *rescueReception
	clocks       *rescueClocks
	cfg          RescueConfig
	factoryMu    sync.Mutex
	factoryCalls int
	factoryQueue []rescueReceptionResult
	events       chan RescueEvent
}

type rescueReceptionResult struct {
	rec RescueReception
	err error
}

func (f *rescueFixture) makeReception(context.Context) (RescueReception, error) {
	f.factoryMu.Lock()
	defer f.factoryMu.Unlock()
	f.factoryCalls++
	if len(f.factoryQueue) != 0 {
		result := f.factoryQueue[0]
		f.factoryQueue = f.factoryQueue[1:]
		return result.rec, result.err
	}
	return f.rec, nil
}

func (f *rescueFixture) queueReception(rec RescueReception, err error) {
	f.factoryMu.Lock()
	defer f.factoryMu.Unlock()
	f.factoryQueue = append(f.factoryQueue, rescueReceptionResult{rec: rec, err: err})
}

func (f *rescueFixture) factoryCallCount() int {
	f.factoryMu.Lock()
	defer f.factoryMu.Unlock()
	return f.factoryCalls
}

func waitRescueEvent(t *testing.T, events <-chan RescueEvent, want RescueEventKind) RescueEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Kind == want {
				return event
			}
		case <-timer.C:
			t.Fatalf("Rescue event %d did not arrive", want)
			return RescueEvent{}
		}
	}
}

func waitFactoryCalls(t *testing.T, f *rescueFixture, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.factoryCallCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("factory calls = %d, want at least %d", f.factoryCallCount(), want)
}

func waitShownCount(t *testing.T, helper *rescueHelper, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(helper.shown()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("shown prompts = %d, want at least %d", len(helper.shown()), want)
}

func newRescueFixture(t *testing.T, d time.Duration) *rescueFixture {
	return newRescueFixtureWithTicker(t, d, false)
}

func newRescueFixtureWithTicker(t *testing.T, d time.Duration, realTicker bool) *rescueFixture {
	t.Helper()
	base := config(t)
	budgets := base.Budgets
	budgets.Handshake = 10 * time.Second // the tests open the channel themselves after the answer
	f := &rescueFixture{helper: &rescueHelper{}, rec: newRescueReception(), clocks: newRescueClocks(), events: make(chan RescueEvent, 16)}
	f.factoryQueue = []rescueReceptionResult{{rec: f.rec}}
	f.cfg = RescueConfig{Runtime: base.Runtime, Duration: d, CleanupTimeout: base.CleanupTimeout, Budgets: budgets, Helper: f.helper, ReceptionFactory: f.makeReception, OnStateChange: func(event RescueEvent) { f.events <- event }}
	clock := f.clocks.clock()
	if realTicker {
		clock.tick = nil
	}
	r, err := newRescue(f.cfg, &neverRuntime{}, clock)
	if err != nil {
		t.Fatalf("newRescue = %v", err)
	}
	f.r = r
	t.Cleanup(func() {
		r.Stop()
		select {
		case <-r.Done():
		case <-time.After(10 * time.Second):
			t.Error("Rescue did not end within 10s of Stop")
		}
	})
	return f
}

// prompt receives a request for C and returns the one prompt it showed.
func (f *rescueFixture) prompt(t *testing.T) RescuePrompt {
	t.Helper()
	if err := f.r.Receive(rescueRequestFor(rescueC.PublicKey())); err != nil {
		t.Fatalf("Receive = %v", err)
	}
	shown := f.helper.shown()
	if len(shown) != 1 {
		t.Fatalf("Receive showed %d prompts, want 1", len(shown))
	}
	return shown[0]
}

func (f *rescueFixture) allow(t *testing.T, p RescuePrompt) Dispatch {
	t.Helper()
	if err := f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true}); err != nil {
		t.Fatalf("Answer(allow) = %v", err)
	}
	_, allows, _ := f.rec.state()
	if len(allows) != 1 {
		t.Fatalf("the reception was asked for %d allows, want 1", len(allows))
	}
	return allows[0]
}

func rescueInGrace(t *testing.T, duration time.Duration) (*rescueFixture, Dispatch, *Approved) {
	t.Helper()
	f := newRescueFixture(t, duration)
	d := f.allow(t, f.prompt(t))
	approved := f.r.approved
	channel, err := openChannel(context.Background(), t, f, d, rescueC)
	if err != nil {
		t.Fatalf("generation 1 channel: %v", err)
	}
	waitHostAuthenticated(t, approved)
	if err := channel.Close(); err != nil {
		t.Fatalf("close generation 1: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	return f, d, approved
}

func TestRescueStopDuringReceptionKeyPreparationClosesGeneratedKey(t *testing.T) {
	f, _, _ := rescueInGrace(t, 4*time.Hour)
	next := newRescueReception()
	next.key = nil
	next.prepareEntered, next.prepareGate = make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releasePrepare := func() { releaseOnce.Do(func() { close(next.prepareGate) }) }
	t.Cleanup(releasePrepare)
	f.queueReception(next, nil)
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	f.r.recheck()
	select {
	case <-next.prepareEntered:
	case <-time.After(time.Second):
		t.Fatal("replacement reception key preparation did not start")
	}
	f.r.Stop()
	f.waitDone(t)
	_, _, closes := next.state()
	if closes != 1 || next.hasKey() {
		t.Fatalf("Done closed with reception closes = %d and K retained = %v", closes, next.hasKey())
	}
	releasePrepare()
	select {
	case <-next.closeDone:
	case <-time.After(time.Second):
		t.Fatal("reception close did not finish after key preparation returned")
	}
}

func TestRescueStopDuringReceptionFactoryCreationClosesGeneratedKey(t *testing.T) {
	f, _, _ := rescueInGrace(t, 4*time.Hour)
	factoryEntered, factoryGate := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseFactory := func() { releaseOnce.Do(func() { close(factoryGate) }) }
	t.Cleanup(releaseFactory)
	var client *agenttransport.RescueClient
	f.r.cfg.ReceptionFactory = func(context.Context) (RescueReception, error) {
		var err error
		client, err = agenttransport.NewRescueClient(agenttransport.RescueClientConfig{
			Endpoint: "wss://gateway.example.invalid" + agentgateway.RescuePath,
			Pins:     []ssh.PublicKey{rescueK.PublicKey()},
		})
		if err != nil {
			return nil, err
		}
		close(factoryEntered)
		<-factoryGate
		return client, nil
	}
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	f.r.recheck()
	select {
	case <-factoryEntered:
	case <-time.After(time.Second):
		t.Fatal("replacement reception factory did not start")
	}
	f.r.Stop()
	f.waitDone(t)
	if client.HostKey() != nil {
		t.Fatal("Done closed while a factory-owned K remained available")
	}
	releaseFactory()
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("late reception was not closed after its factory returned")
	}
}

func waitHostAuthenticated(t *testing.T, approved *Approved) {
	t.Helper()
	if approved == nil {
		t.Fatal("approved Host is unavailable")
	}
	approved.hostMu.Lock()
	host := approved.host
	approved.hostMu.Unlock()
	if host == nil {
		t.Fatal("approved Host is unavailable")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		connected, observed := host.LocallyAuthenticated()
		if connected && observed {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("Host did not reach authenticated channel state")
		}
		runtime.Gosched()
	}
}

func (f *rescueFixture) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-f.r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Rescue did not end within 10s")
	}
}

func (f *rescueFixture) requireLive(t *testing.T) {
	t.Helper()
	select {
	case <-f.r.Done():
		t.Fatal("Rescue ended")
	default:
	}
}

// requireNoGrant checks the end without a grant: no allow was sent, the
// reception was closed once (K discarded), and a later allow does nothing.
func (f *rescueFixture) requireNoGrant(t *testing.T, p RescuePrompt) {
	t.Helper()
	f.waitDone(t)
	if err := f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true}); !errors.Is(err, ErrDenied) {
		t.Fatalf("Answer(allow) after the end = %v, want ErrDenied", err)
	}
	_, allows, closes := f.rec.state()
	if len(allows) != 0 {
		t.Fatalf("the reception was asked for %d allows, want none", len(allows))
	}
	if closes != 1 {
		t.Fatalf("the reception was closed %d times, want 1 (K discarded once)", closes)
	}
}

// openChannel is the 担当者's channel over the stream the reception carried.
func openChannel(ctx context.Context, t *testing.T, f *rescueFixture, d Dispatch, signer ssh.Signer) (*agentchannel.Channel, error) {
	t.Helper()
	hostKey, err := ssh.ParsePublicKey(d.HostKey)
	if err != nil {
		t.Fatalf("dispatch host key: %v", err)
	}
	return agentchannel.Open(ctx, agentchannel.ClientConfig{Binding: d.Binding, HostKey: hostKey, Signer: signer, Deadline: d.Deadline, Budgets: f.cfg.Budgets}, f.rec.clientConn())
}

// 受付 OFF のホストへの要求: a request after the reception ended never reaches the user.
func TestRescueRequestAfterReceptionEndShowsNoPrompt(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*rescueFixture)
	}{
		{"受付 OFF", func(f *rescueFixture) { f.r.Stop() }},
		{"コードの失効で受付の接続が終わった", func(f *rescueFixture) { f.rec.lose() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRescueFixture(t, 0)
			tc.end(f)
			f.waitDone(t)
			if err := f.r.Receive(rescueRequestFor(rescueC.PublicKey())); !errors.Is(err, ErrDenied) {
				t.Fatalf("Receive after the end = %v, want ErrDenied", err)
			}
			if shown := f.helper.shown(); len(shown) != 0 {
				t.Fatalf("showed %d prompts after the end", len(shown))
			}
			if _, _, closes := f.rec.state(); closes != 1 {
				t.Fatalf("the reception was closed %d times, want 1", closes)
			}
		})
	}
}

// ポップアップ: the verified identity, the scope, the chosen lifetime, C's
// fingerprint and the no-isolation warning, under a fresh host-chosen id.
func TestRescuePromptShowsVerifiedIdentity(t *testing.T) {
	f := newRescueFixture(t, 0)
	p := f.prompt(t)
	if id, err := hex.DecodeString(p.RequestID); err != nil || len(id) != 16 {
		t.Fatalf("RequestID = %q, want 16 random bytes in hex", p.RequestID)
	}
	want := RescuePrompt{RequestID: p.RequestID, Organization: "O2 CONNECTIVE", DisplayName: "山田（サポート）", Profile: "mac_current_user",
		Duration: 2 * time.Hour, ClientFingerprint: ssh.FingerprintSHA256(rescueC.PublicKey()), Warning: RescueWarning}
	if p != want {
		t.Fatalf("prompt = %+v\nwant %+v", p, want)
	}
	if !strings.Contains(p.Warning, "隔離なし") {
		t.Fatalf("Warning = %q, want the no-isolation warning", p.Warning)
	}
	if again := newRescueFixture(t, 0).prompt(t); again.RequestID == p.RequestID {
		t.Fatal("two receptions chose the same RequestID")
	}
}

// verifiedFor is C's request as the reception with host key k verifies it on
// the wire: the gateway's pinned identity key signed it for that reception.
func verifiedFor(t *testing.T, c ssh.PublicKey, k ssh.Signer) agenttransport.VerifiedRescueIdentity {
	t.Helper()
	const endpoint = "wss://gateway.example.invalid" + agentgateway.RescuePath
	identity, reception := rescueSigner(0x49), bytes.Repeat([]byte{0x72}, agentgateway.RescueReceptionSize)
	kWire, nonce := k.PublicKey().Marshal(), bytes.Repeat([]byte{0x6e}, agentgateway.RescueNonceSize)
	id := agentgateway.RescueIdentity{Nonce: nonce, Client: c.Marshal(), DisplayName: "山田（サポート）", Organization: "O2 CONNECTIVE", Profile: agentgateway.RescueProfile}
	sig, err := identity.Sign(rand.Reader, agentgateway.RescueRequestTranscript(endpoint, reception, kWire, id.Nonce, id.Client, id.DisplayName, id.Organization, id.Profile))
	if err != nil {
		t.Fatal(err)
	}
	id.Signature = sig.Blob
	v, err := agenttransport.VerifyRescueRequest(endpoint, reception, kWire, []ssh.PublicKey{identity.PublicKey()}, id)
	if err != nil {
		t.Fatalf("VerifyRescueRequest = %v", err)
	}
	return v
}

// The wire reaches the state machine only through the reception's verified
// request: the popup shows exactly what the identity key signed, and a value
// that was never verified shows nothing.
func TestRescueReceivesOnlyARequestTheReceptionVerified(t *testing.T) {
	t.Run("verified", func(t *testing.T) {
		f := newRescueFixture(t, 0)
		requests := make(chan agenttransport.VerifiedRescueIdentity, 1)
		requests <- verifiedFor(t, rescueC.PublicKey(), rescueK)
		if err := f.r.ReceiveFrom(requests); err != nil {
			t.Fatalf("ReceiveFrom = %v", err)
		}
		shown := f.helper.shown()
		if len(shown) != 1 {
			t.Fatalf("showed %d prompts, want 1", len(shown))
		}
		p := shown[0]
		if p.ClientFingerprint != ssh.FingerprintSHA256(rescueC.PublicKey()) || p.DisplayName != "山田（サポート）" ||
			p.Organization != "O2 CONNECTIVE" || p.Profile != agentgateway.RescueProfile {
			t.Fatalf("prompt = %+v, want the verified identity", p)
		}
		// The grant goes to that C.
		d := f.allow(t, p)
		want, err := runbackend.KeySetDigest([]ssh.PublicKey{rescueC.PublicKey()})
		if err != nil || d.Binding.KeySetDigest != want {
			t.Fatal("the grant does not allow exactly the verified C")
		}
	})
	// Each reception verifies against its own K, which is also this Rescue's
	// host key: a request verified by another reception is not this one's.
	t.Run("verified by another reception", func(t *testing.T) {
		f := newRescueFixture(t, 0)
		requests := make(chan agenttransport.VerifiedRescueIdentity, 1)
		requests <- verifiedFor(t, rescueC.PublicKey(), rescueSigner(0x4c))
		if err := f.r.ReceiveFrom(requests); !errors.Is(err, ErrDenied) {
			t.Fatalf("ReceiveFrom = %v, want ErrDenied", err)
		}
		if shown := f.helper.shown(); len(shown) != 0 {
			t.Fatalf("showed %d prompts for another reception's request", len(shown))
		}
	})
	t.Run("never verified", func(t *testing.T) {
		f := newRescueFixture(t, 0)
		requests := make(chan agenttransport.VerifiedRescueIdentity, 1)
		requests <- agenttransport.VerifiedRescueIdentity{}
		if err := f.r.ReceiveFrom(requests); !errors.Is(err, ErrDenied) {
			t.Fatalf("ReceiveFrom = %v, want ErrDenied", err)
		}
		if shown := f.helper.shown(); len(shown) != 0 {
			t.Fatalf("showed %d prompts for an unverified value", len(shown))
		}
	})
	t.Run("after the end", func(t *testing.T) {
		f := newRescueFixture(t, 0)
		f.r.Stop()
		f.waitDone(t)
		returned := make(chan error, 1)
		go func() { returned <- f.r.ReceiveFrom(make(chan agenttransport.VerifiedRescueIdentity)) }()
		select {
		case err := <-returned:
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("ReceiveFrom = %v, want ErrDenied", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("ReceiveFrom waited for a request after the end")
		}
	})
}

// One code, one prompt: a second request on the same reception never reaches the user.
func TestRescueSecondRequestShowsNoPrompt(t *testing.T) {
	f := newRescueFixture(t, 0)
	p := f.prompt(t)
	other := rescueSigner(0x44).PublicKey()
	if err := f.r.Receive(rescueRequestFor(other)); !errors.Is(err, ErrDenied) {
		t.Fatalf("second Receive = %v, want ErrDenied", err)
	}
	if shown := f.helper.shown(); len(shown) != 1 {
		t.Fatalf("showed %d prompts, want only the first", len(shown))
	}
	// The first request is intact: its C, not the second one's, is allowed.
	d := f.allow(t, p)
	keys, err := agentchannel.CanonicalKeys([]ssh.PublicKey{rescueC.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	want, err := runbackend.KeySetDigest(keys)
	if err != nil {
		t.Fatal(err)
	}
	if d.Binding.KeySetDigest != want {
		t.Fatal("the grant does not allow the first request's C")
	}
}

// The native helper (#410) computes the same digest over what it displayed,
// so the encoding is fixed here byte for byte, and every field is covered.
func TestRescuePromptDigest(t *testing.T) {
	base := RescuePrompt{RequestID: "00112233445566778899aabbccddeeff", Organization: "O2 CONNECTIVE", DisplayName: "山田（サポート）", Profile: "mac_current_user",
		Duration: 4 * time.Hour, ClientFingerprint: "SHA256:abc", Warning: RescueWarning}
	h := sha256.New()
	h.Write([]byte("rescue-host.maclaunch.rescue-prompt\x00v1\x00"))
	for _, field := range []string{base.RequestID, base.Organization, base.DisplayName, base.Profile, strconv.Itoa(4 * 3600), base.ClientFingerprint, base.Warning} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(field)))
		h.Write(n[:])
		h.Write([]byte(field))
	}
	if got, want := base.Digest(), [32]byte(h.Sum(nil)); got != want {
		t.Fatalf("Digest = %x, want %x", got, want)
	}
	for name, change := range map[string]func(*RescuePrompt){
		"RequestID":         func(p *RescuePrompt) { p.RequestID = "ffeeddccbbaa99887766554433221100" },
		"Organization":      func(p *RescuePrompt) { p.Organization = "Other" },
		"DisplayName":       func(p *RescuePrompt) { p.DisplayName = "佐藤" },
		"Profile":           func(p *RescuePrompt) { p.Profile = "windows_guest" },
		"Duration":          func(p *RescuePrompt) { p.Duration = 8 * time.Hour },
		"ClientFingerprint": func(p *RescuePrompt) { p.ClientFingerprint = "SHA256:abd" },
		"Warning":           func(p *RescuePrompt) { p.Warning = "" },
		"field boundary":    func(p *RescuePrompt) { p.Organization, p.DisplayName = "O2 CONNECTIVE山", "田（サポート）" },
	} {
		changed := base
		change(&changed)
		if changed.Digest() == base.Digest() {
			t.Errorf("changing %s keeps the digest", name)
		}
	}
}

// 拒否: no grant, a K-signed deny, K discarded.
func TestRescueDenyIssuesNoGrant(t *testing.T) {
	f := newRescueFixture(t, 0)
	p := f.prompt(t)
	if err := f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: false}); err != nil {
		t.Fatalf("Answer(deny) = %v", err)
	}
	f.requireNoGrant(t, p)
	if refuses, _, _ := f.rec.state(); len(refuses) != 1 || refuses[0] {
		t.Fatalf("refusals = %v, want one deny", refuses)
	}
}

// 60 秒の自動拒否 on either clock, and when the wall clock goes back.
func TestRescuePromptTimesOutOnEitherClock(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mono, wall time.Duration
	}{
		{"単調時計だけが進む", RescuePromptTimeout, 0},
		{"壁時計だけが進む", 0, RescuePromptTimeout},
		{"壁時計が戻る", time.Second, -time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRescueFixture(t, 0)
			p := f.prompt(t)
			f.clocks.advance(tc.mono, tc.wall)
			f.clocks.recheck(t)
			f.requireNoGrant(t, p)
			if refuses, _, _ := f.rec.state(); len(refuses) != 1 || !refuses[0] {
				t.Fatalf("refusals = %v, want one timeout", refuses)
			}
			if w := f.helper.withdrawn(); len(w) != 1 || w[0] != p.RequestID {
				t.Fatalf("withdrawn = %v, want the prompt", w)
			}
		})
	}
	t.Run("60 秒の内", func(t *testing.T) {
		f := newRescueFixture(t, 0)
		p := f.prompt(t)
		f.clocks.advance(RescuePromptTimeout-time.Nanosecond, RescuePromptTimeout-time.Nanosecond)
		f.clocks.settle(t)
		f.requireLive(t)
		if w := f.helper.withdrawn(); len(w) != 0 {
			t.Fatalf("withdrawn = %v within 60 seconds", w)
		}
		f.allow(t, p)
	})
}

// An allow that arrives after 60 seconds is discarded even before a recheck ran.
func TestRescueLateAllowIsDiscarded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mono, wall time.Duration
	}{
		{"単調時計で 60 秒", RescuePromptTimeout, 0},
		{"壁時計で 60 秒", 0, RescuePromptTimeout},
		{"壁時計が戻る", time.Second, -time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRescueFixture(t, 0)
			p := f.prompt(t)
			f.clocks.advance(tc.mono, tc.wall)
			if err := f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true}); !errors.Is(err, ErrDenied) {
				t.Fatalf("late Answer(allow) = %v, want ErrDenied", err)
			}
			f.requireNoGrant(t, p)
			if refuses, _, _ := f.rec.state(); len(refuses) != 1 || !refuses[0] {
				t.Fatalf("refusals = %v, want one timeout", refuses)
			}
		})
	}
}

// Only the helper's reply to the current prompt approves: not a WebView IPC
// handler that learned the id and digest, and not a reply to other content.
func TestRescueAcceptsAllowOnlyFromHelperForThisPrompt(t *testing.T) {
	f := newRescueFixture(t, 0)
	p := f.prompt(t)
	shownOther := p
	shownOther.DisplayName = "佐藤"
	webview := &rescueHelper{}
	for name, tc := range map[string]struct {
		from  RescueHelper
		reply RescueReply
	}{
		"WebView の IPC": {webview, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true}},
		"経路なし":          {nil, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true}},
		"要求 id が違う":     {f.helper, RescueReply{RequestID: "ffeeddccbbaa99887766554433221100", Digest: p.Digest(), Allow: true}},
		"digest が違う":    {f.helper, RescueReply{RequestID: p.RequestID, Digest: shownOther.Digest(), Allow: true}},
	} {
		if err := f.r.Answer(tc.from, tc.reply); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: Answer = %v, want ErrDenied", name, err)
		}
	}
	if refuses, allows, closes := f.rec.state(); len(refuses) != 0 || len(allows) != 0 || closes != 0 {
		t.Fatalf("refused replies changed the reception: refuses %v, allows %d, closes %d", refuses, len(allows), closes)
	}
	f.requireLive(t)
	f.allow(t, p)
}

// 確認中に受付の接続が切れたら、拒否として終え、後で届いた許可で grant を発行しない。
func TestRescueReceptionLossWhileConfirmingIssuesNoGrant(t *testing.T) {
	f := newRescueFixture(t, 0)
	p := f.prompt(t)
	f.rec.lose()
	f.requireNoGrant(t, p)
	if w := f.helper.withdrawn(); len(w) != 1 || w[0] != p.RequestID {
		t.Fatalf("withdrawn = %v, want the prompt", w)
	}
}

// Host-side end while the prompt is up: no grant, K discarded.
func TestRescueStopWhileConfirmingIssuesNoGrant(t *testing.T) {
	f := newRescueFixture(t, 0)
	p := f.prompt(t)
	f.r.Stop()
	f.requireNoGrant(t, p)
	if w := f.helper.withdrawn(); len(w) != 1 || w[0] != p.RequestID {
		t.Fatalf("withdrawn = %v, want the prompt", w)
	}
}

// A host-side end or a lost reception takes effect once it is observed, not
// when the state machine's loop gets to it: a request or an answer right after
// it is refused.
func TestRescueRefusesRightAfterEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*rescueFixture)
	}{
		{"ホスト側の切断", func(f *rescueFixture) { f.r.Stop() }},
		{"受付の切断", func(f *rescueFixture) { f.rec.lose() }},
	} {
		t.Run(tc.name+"の直後の許可", func(t *testing.T) {
			f := newRescueFixture(t, 0)
			p := f.prompt(t)
			tc.end(f)
			if err := f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true}); !errors.Is(err, ErrDenied) {
				t.Fatalf("Answer(allow) right after the end = %v, want ErrDenied", err)
			}
			f.requireNoGrant(t, p)
		})
		t.Run(tc.name+"の直後の要求", func(t *testing.T) {
			f := newRescueFixture(t, 0)
			tc.end(f)
			if err := f.r.Receive(rescueRequestFor(rescueC.PublicKey())); !errors.Is(err, ErrDenied) {
				t.Fatalf("Receive right after the end = %v, want ErrDenied", err)
			}
			if shown := f.helper.shown(); len(shown) != 0 {
				t.Fatalf("showed %d prompts right after the end", len(shown))
			}
		})
	}
}

// The user's deny ends the prompt in the same step that accepts it: an allow
// for the same prompt racing with it never reaches the reception.
func TestRescueDenyRacingAllowIssuesNoGrant(t *testing.T) {
	for i := range 50 {
		f := newRescueFixture(t, 0)
		p := f.prompt(t)
		start := make(chan struct{})
		var deny, allow error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			deny = f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: false})
		}()
		go func() {
			defer wg.Done()
			<-start
			allow = f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true})
		}()
		close(start)
		wg.Wait()
		if (deny == nil) == (allow == nil) {
			t.Fatalf("iteration %d: deny %v, allow %v, want exactly one accepted", i, deny, allow)
		}
		if _, allows, _ := f.rec.state(); deny == nil && len(allows) != 0 {
			t.Fatalf("iteration %d: the deny was accepted and a grant was handed over", i)
		}
	}
}

// 許可: the grant allows only C, the host key is K, and the lifetime runs
// from the time of approval, not from the prompt.
func TestRescueGrantAllowsOnlyCUntilChosenLifetime(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	p := f.prompt(t)
	if p.Duration != 4*time.Hour {
		t.Fatalf("prompt Duration = %v, want 4h", p.Duration)
	}
	f.clocks.advance(30*time.Second, 30*time.Second)
	approvedAt := f.clocks.now()
	d := f.allow(t, p)
	if !d.Deadline.Equal(approvedAt.Add(4 * time.Hour)) {
		t.Fatalf("Deadline = %v, want the approval time %v + 4h", d.Deadline, approvedAt)
	}
	keys, err := agentchannel.CanonicalKeys([]ssh.PublicKey{rescueC.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	want, err := runbackend.KeySetDigest(keys)
	if err != nil {
		t.Fatal(err)
	}
	if d.Binding.KeySetDigest != want {
		t.Fatal("the grant's key set is not exactly C")
	}
	if !bytes.Equal(d.HostKey, rescueK.PublicKey().Marshal()) {
		t.Fatal("the dispatch host key is not K")
	}
	if refuses, _, closes := f.rec.state(); len(refuses) != 0 || closes != 0 {
		t.Fatalf("allow also refused %v or closed the reception %d times", refuses, closes)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := openChannel(ctx, t, f, d, rescueC)
	if err != nil {
		t.Fatalf("channel with C = %v", err)
	}
	f.requireLive(t)
	waitHostAuthenticated(t, f.r.approved)
	_ = ch.Close()
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	f.clocks.advance(4*time.Minute+59*time.Second, 4*time.Minute+59*time.Second)
	f.r.recheck()
	f.requireLive(t)
	f.r.Stop()
	f.waitDone(t)
	if _, _, closes := f.rec.state(); closes != 1 {
		t.Fatalf("the reception was closed %d times, want 1", closes)
	}
}

func TestRescueGraceExpiresUsingBothClocksAndWallRollback(t *testing.T) {
	cases := []struct {
		name   string
		before func(*rescueClocks)
		after  func(*rescueClocks)
	}{
		{"monotonic only", func(c *rescueClocks) { c.advance(4*time.Minute+59*time.Second, 0) }, func(c *rescueClocks) { c.advance(2*time.Second, 0) }},
		{"wall only", func(c *rescueClocks) { c.advance(0, 4*time.Minute+59*time.Second) }, func(c *rescueClocks) { c.advance(0, 2*time.Second) }},
		{"wall rollback", func(c *rescueClocks) { c.advance(0, 4*time.Minute+59*time.Second) }, func(c *rescueClocks) { c.advance(0, -5*time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRescueFixture(t, 4*time.Hour)
			next := freshRescueReception(0x4e, "987654321098")
			f.queueReception(next, nil)
			p := f.prompt(t)
			d := f.allow(t, p)
			approved := f.r.approved
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ch, err := openChannel(ctx, t, f, d, rescueC)
			if err != nil {
				t.Fatalf("channel: %v", err)
			}
			waitHostAuthenticated(t, approved)
			if err := ch.Close(); err != nil {
				t.Fatalf("close channel: %v", err)
			}
			_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
			select {
			case <-f.r.Done():
				t.Fatal("Rescue ended at transport loss before the grace elapsed")
			default:
			}
			tc.before(f.clocks)
			f.clocks.recheck(t)
			if !approved.issuer.Snapshot().HasAuthority {
				t.Fatal("grant ended before five minutes")
			}
			tc.after(f.clocks)
			f.r.recheck()
			opened := waitRescueEvent(t, f.events, RescueEventReceptionOpened)
			if opened.Code != next.code {
				t.Fatalf("new code = %q, want %q", opened.Code, next.code)
			}
			if approved.issuer.Snapshot().HasAuthority {
				t.Fatal("old grant remained live after grace expiry")
			}
			f.r.mu.Lock()
			state := f.r.state
			f.r.mu.Unlock()
			if state != rescueAccepting {
				t.Fatalf("state after grace expiry = %v, want accepting", state)
			}
		})
	}
}

func TestRescueGraceUsesLastValidFrameRatherThanDetectionTime(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	prompt := f.prompt(t)
	d := f.allow(t, prompt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := openChannel(ctx, t, f, d, rescueC)
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	waitHostAuthenticated(t, f.r.approved)
	f.r.noteActivity() // the last valid application or heartbeat frame
	f.clocks.advance(30*time.Second, 30*time.Second)
	if err := ch.Close(); err != nil {
		t.Fatalf("close channel: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	select {
	case <-f.r.Done():
		t.Fatal("Rescue ended at the transport-loss detection point")
	default:
	}
	approved := f.r.approved
	next := freshRescueReception(0x4e, "987654321098")
	f.queueReception(next, nil)
	f.clocks.advance(4*time.Minute+31*time.Second, 4*time.Minute+31*time.Second)
	f.r.recheck()
	opened := waitRescueEvent(t, f.events, RescueEventReceptionOpened)
	if opened.Code != next.code || approved.issuer.Snapshot().HasAuthority {
		t.Fatalf("last-frame grace result code=%q old-grant-live=%v", opened.Code, approved.issuer.Snapshot().HasAuthority)
	}
}

func TestRescueGraceRechecksWallClockAfterSleepWithinOneSecond(t *testing.T) {
	f := newRescueFixtureWithTicker(t, 4*time.Hour, true)
	prompt := f.prompt(t)
	d := f.allow(t, prompt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := openChannel(ctx, t, f, d, rescueC)
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	waitHostAuthenticated(t, f.r.approved)
	f.r.noteActivity()
	if err := ch.Close(); err != nil {
		t.Fatalf("close channel: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	select {
	case <-f.r.Done():
		t.Fatal("Rescue ended before the wall clock advanced")
	default:
	}
	approved := f.r.approved
	next := freshRescueReception(0x4e, "987654321098")
	f.queueReception(next, nil)
	f.clocks.advance(0, 6*time.Minute)
	started := time.Now()
	opened := waitRescueEvent(t, f.events, RescueEventReceptionOpened)
	if time.Since(started) > 1250*time.Millisecond {
		t.Fatalf("wall-clock expiry took %v without a direct recheck", time.Since(started))
	}
	if opened.Code != next.code || approved.issuer.Snapshot().HasAuthority {
		t.Fatalf("wall-clock expiry code=%q old-grant-live=%v", opened.Code, approved.issuer.Snapshot().HasAuthority)
	}
}

func TestRescueGraceExpiryOpensNewReceptionAndShowsOnlyOnItsRequest(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	first := f.prompt(t)
	d := f.allow(t, first)
	oldApproved := f.r.approved
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := openChannel(ctx, t, f, d, rescueC)
	if err != nil {
		t.Fatalf("generation 1 channel: %v", err)
	}
	waitHostAuthenticated(t, oldApproved)
	if err := ch.Close(); err != nil {
		t.Fatalf("close generation 1: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	second := freshRescueReception(0x4e, "987654321098")
	f.queueReception(second, nil)
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	f.r.recheck()
	opened := waitRescueEvent(t, f.events, RescueEventReceptionOpened)
	if opened.Code != second.code {
		t.Fatalf("new reception code = %q, want %q", opened.Code, second.code)
	}
	if f.factoryCallCount() != 2 || second.opens != 1 {
		t.Fatalf("factory calls = %d, new reception opens = %d", f.factoryCallCount(), second.opens)
	}
	if bytes.Equal(rescueK.PublicKey().Marshal(), second.HostKey().PublicKey().Marshal()) {
		t.Fatal("new reception reused the old K")
	}
	if len(f.helper.shown()) != 1 {
		t.Fatal("opening the replacement reception showed a prompt")
	}
	second.requests <- verifiedFor(t, rescueC.PublicKey(), second.key)
	waitShownCount(t, f.helper, 2)
	newPrompt := f.helper.shown()[1]
	if err := f.r.Answer(f.helper, RescueReply{RequestID: newPrompt.RequestID, Digest: newPrompt.Digest(), Allow: true}); err != nil {
		t.Fatalf("new reception approval: %v", err)
	}
	second.mu.Lock()
	newAllows := append([]Dispatch(nil), second.allows...)
	second.mu.Unlock()
	f.r.mu.Lock()
	newApproved := f.r.approved
	f.r.mu.Unlock()
	if len(newAllows) != 1 || newApproved == nil || newApproved == oldApproved || newApproved.session == oldApproved.session {
		t.Fatalf("new permit did not create a fresh Session: allows=%d approved=%p old=%p", len(newAllows), newApproved, oldApproved)
	}
	if newAllows[0].Binding.ConnectionGeneration != 1 || newAllows[0].Binding.Grant.Identity.LaunchID == oldApproved.session.Grant().Binding.Identity.LaunchID {
		t.Fatal("new permit reused the expired grant instead of creating a new guest Session")
	}
	if conn := second.clientConn(); conn != nil {
		_ = conn.Close()
	}
	f.r.Stop()
	f.waitDone(t)
}

func TestRescueReconnectionRechecksGraceBeforeAdmission(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	next := freshRescueReception(0x4e, "987654321098")
	f.queueReception(next, nil)
	select {
	case <-f.rec.requestCalls:
	case <-time.After(time.Second):
		t.Fatal("loop did not capture the initial reception")
	}
	requestEntered, requestGate := make(chan struct{}), make(chan struct{})
	f.rec.gateNextRequest(requestEntered, requestGate)
	var releaseRequestOnce sync.Once
	releaseRequest := func() { releaseRequestOnce.Do(func() { close(requestGate) }) }
	t.Cleanup(releaseRequest)
	var doneOnce sync.Once
	doneObserved := make(chan struct{})
	f.rec.onClose = func() {
		doneOnce.Do(func() {
			f.rec.lose()
			close(doneObserved)
		})
	}
	offered := make(chan struct {
		dispatch Dispatch
		conn     net.Conn
	}, 1)
	identity := verifiedFor(t, rescueC.PublicKey(), rescueK)
	f.rec.onResume = func() { f.rec.requests <- identity }
	f.rec.onAllow = func(dispatch Dispatch, conn net.Conn) {
		if dispatch.Binding.ConnectionGeneration == 2 {
			f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
			offered <- struct {
				dispatch Dispatch
				conn     net.Conn
			}{dispatch: dispatch, conn: conn}
		}
	}
	prompt := f.prompt(t)
	d := f.allow(t, prompt)
	approved := f.r.approved
	first, err := openChannel(context.Background(), t, f, d, rescueC)
	if err != nil {
		t.Fatalf("generation 1 channel: %v", err)
	}
	waitHostAuthenticated(t, approved)
	if err := first.Close(); err != nil {
		t.Fatalf("close generation 1: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	var offer struct {
		dispatch Dispatch
		conn     net.Conn
	}
	select {
	case offer = <-offered:
	case <-time.After(3 * time.Second):
		t.Fatal("resume did not reach the generation 2 reception handoff")
	}
	if offer.dispatch.Binding.ConnectionGeneration != 2 {
		t.Fatalf("resumed generation = %d, want 2", offer.dispatch.Binding.ConnectionGeneration)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f.r.mu.Lock()
		handoff := f.r.resumeHandoff
		f.r.mu.Unlock()
		if handoff {
			break
		}
		time.Sleep(time.Millisecond)
	}
	f.r.mu.Lock()
	handoff := f.r.resumeHandoff
	f.r.mu.Unlock()
	if !handoff {
		t.Fatal("resume handoff was not recorded")
	}
	select {
	case <-requestEntered:
	case <-time.After(time.Second):
		t.Fatal("loop did not capture the old reception before waiting for its request")
	}
	type openResult struct{ admissionWaitTimedOut bool }
	opened := make(chan openResult, 1)
	openDone := make(chan struct{})
	go func() {
		defer close(openDone)
		hostKey, parseErr := ssh.ParsePublicKey(offer.dispatch.HostKey)
		if parseErr != nil {
			opened <- openResult{}
			return
		}
		channel, _ := agentchannel.Open(context.Background(), agentchannel.ClientConfig{
			Binding: offer.dispatch.Binding, HostKey: hostKey, Signer: rescueC,
			Deadline: offer.dispatch.Deadline, Budgets: f.cfg.Budgets,
		}, offer.conn)
		if channel != nil {
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-doneObserved:
			case <-timer.C:
				_ = channel.Close()
				opened <- openResult{admissionWaitTimedOut: true}
				return
			}
			_ = channel.Close()
		}
		opened <- openResult{}
	}()
	t.Cleanup(func() {
		timer := time.NewTimer(f.cfg.Budgets.Handshake + 4*time.Second + f.cfg.Budgets.Cleanup)
		defer timer.Stop()
		select {
		case <-openDone:
		case <-timer.C:
			t.Error("generation 2 SSH attempt did not finish during cleanup")
		}
	})
	select {
	case <-doneObserved:
	case <-time.After(3 * time.Second):
		t.Fatal("grace expiry did not close the old reception")
	}
	releaseRequest()
	var openedReception RescueEvent
	deadlineTimer := time.NewTimer(3 * time.Second)
	defer deadlineTimer.Stop()
	for {
		select {
		case event := <-f.events:
			if event.Kind == RescueEventReceptionOpened {
				openedReception = event
				goto receptionOpened
			}
		case <-f.r.Done():
			t.Fatal("grace-only expiry ended Rescue instead of opening a new reception")
		case <-deadlineTimer.C:
			t.Fatal("grace-only expiry did not open a new reception")
		}
	}

receptionOpened:
	if openedReception.Code != next.code || f.factoryCallCount() != 2 {
		t.Fatalf("grace-only expiry opened code=%q factory calls=%d", openedReception.Code, f.factoryCallCount())
	}
	if approved.issuer.Snapshot().HasAuthority {
		t.Fatal("expired grace reopened the previous grant")
	}
	f.r.mu.Lock()
	state := f.r.state
	f.r.mu.Unlock()
	if state != rescueAccepting {
		t.Fatalf("state after grace-only expiry = %v, want accepting", state)
	}
	f.requireLive(t)
	select {
	case result := <-opened:
		if result.admissionWaitTimedOut {
			t.Fatal("grace expiry did not close the old reception before the generation 2 channel close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the stale generation 2 SSH attempt did not finish")
	}
}

func TestOldAfterServeCannotEndReplacementReception(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	d := f.allow(t, f.prompt(t))
	approved := f.r.approved
	first, err := openChannel(context.Background(), t, f, d, rescueC)
	if err != nil {
		t.Fatalf("generation 1 channel: %v", err)
	}
	waitHostAuthenticated(t, approved)
	if err := first.Close(); err != nil {
		t.Fatalf("close generation 1: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	next := freshRescueReception(0x4e, "987654321098")
	next.openEntered, next.openGate = make(chan struct{}), make(chan struct{})
	f.queueReception(next, nil)
	f.r.step.Lock()
	afterDone := make(chan struct{})
	go func() {
		f.r.afterServe(approved, 1)
		close(afterDone)
	}()
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	f.r.expireGrace()
	select {
	case <-next.openEntered:
	case <-time.After(3 * time.Second):
		f.r.step.Unlock()
		t.Fatal("replacement reception did not begin opening")
	}
	f.r.step.Unlock()
	close(next.openGate)
	select {
	case <-afterDone:
	case <-time.After(3 * time.Second):
		t.Fatal("old afterServe did not finish")
	}
	opened := waitRescueEvent(t, f.events, RescueEventReceptionOpened)
	if opened.Code != next.code {
		t.Fatalf("replacement code = %q, want %q", opened.Code, next.code)
	}
	f.requireLive(t)
	f.r.mu.Lock()
	state, current := f.r.state, f.r.reception
	f.r.mu.Unlock()
	if state != rescueAccepting || current != next {
		t.Fatalf("old afterServe changed replacement: state=%v current=%p", state, current)
	}
}

func TestRescueReclaimsGenerationTwoThatEndsBeforeConfirmation(t *testing.T) {
	f, _, approved := rescueInGrace(t, 4*time.Hour)
	dispatch, err := approved.NextGeneration()
	if err != nil {
		t.Fatalf("prepare generation 2: %v", err)
	}
	f.r.mu.Lock()
	f.r.resumeHandoff, f.r.resumeReception = true, f.rec
	f.r.resumeApproved, f.r.resumeGeneration = approved, dispatch.Binding.ConnectionGeneration
	f.r.mu.Unlock()
	if dispatch.Binding.ConnectionGeneration != 2 {
		t.Fatalf("handoff generation = %d, want 2", dispatch.Binding.ConnectionGeneration)
	}

	host, peer := net.Pipe()
	_ = peer.Close()
	f.r.serveApproved(approved, dispatch.Binding.ConnectionGeneration, host)
	_ = host.Close()
	select {
	case <-f.r.Done():
	case <-time.After(time.Second):
		t.Fatal("failed generation 2 handoff left Rescue and its reception active")
	}
	_, _, closes := f.rec.state()
	if closes != 1 {
		t.Fatalf("reception close count = %d, want 1 after failed handoff", closes)
	}
	if approved.issuer.Snapshot().HasAuthority {
		t.Fatal("failed generation 2 handoff retained the grant")
	}
}

func TestStopAndGraceTickDoNotStartReception(t *testing.T) {
	f, _, _ := rescueInGrace(t, 4*time.Hour)
	f.queueReception(freshRescueReception(0x4e, "987654321098"), nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.rec.onClose = func() { once.Do(func() { close(entered); <-release }) }
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	rechecked := make(chan struct{})
	go func() {
		f.r.recheck()
		close(rechecked)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("grace tick did not begin closing the old reception")
	}
	f.r.Stop()
	close(release)
	select {
	case <-rechecked:
	case <-time.After(3 * time.Second):
		t.Fatal("grace tick did not finish after reception close")
	}
	f.waitDone(t)
	deadline := time.Now().Add(2 * time.Second)
	for f.factoryCallCount() == 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := f.factoryCallCount(); calls != 1 {
		t.Fatalf("factory calls after Stop raced with grace tick = %d, want 1", calls)
	}
}

func TestDeadlineDuringGraceCleanupDoesNotStartReception(t *testing.T) {
	f, _, approved := rescueInGrace(t, 4*time.Hour)
	next := freshRescueReception(0x4e, "987654321098")
	f.queueReception(next, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.rec.onClose = func() { once.Do(func() { close(entered); <-release }) }
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	rechecked := make(chan struct{})
	go func() {
		f.r.recheck()
		close(rechecked)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("grace expiry did not start closing the old reception")
	}
	f.clocks.advance(4*time.Hour, 4*time.Hour)
	close(release)
	select {
	case <-rechecked:
	case <-time.After(3 * time.Second):
		t.Fatal("grace cleanup did not finish")
	}
	f.waitDone(t)
	if calls := f.factoryCallCount(); calls != 1 {
		t.Fatalf("factory calls after the grant deadline during cleanup = %d, want 1", calls)
	}
	if next.opens != 0 || approved.issuer.Snapshot().HasAuthority {
		t.Fatalf("expired grant reopened reception %d times or remained live", next.opens)
	}
}

func TestStopCancelsInProgressReceptionOpen(t *testing.T) {
	f, _, _ := rescueInGrace(t, 4*time.Hour)
	next := freshRescueReception(0x4e, "987654321098")
	next.openEntered, next.openGate, next.openCanceled = make(chan struct{}), make(chan struct{}), make(chan error, 1)
	f.queueReception(next, nil)
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	f.r.recheck()
	select {
	case <-next.openEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement reception did not reach Open")
	}
	f.r.step.Lock()
	f.r.Stop()
	select {
	case err := <-next.openCanceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Open cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(150 * time.Millisecond):
		f.r.step.Unlock()
		f.waitDone(t)
		t.Fatal("Stop did not cancel the in-progress reception Open before end acquired step")
	}
	f.r.step.Unlock()
	f.waitDone(t)
	if next.opens != 0 {
		t.Fatalf("canceled reception Open completed %d times", next.opens)
	}
}

func TestStopClosesUnadoptedReceptionBeforeReturning(t *testing.T) {
	f, _, _ := rescueInGrace(t, 4*time.Hour)
	next := freshRescueReception(0x4e, "987654321098")
	next.openEntered, next.openGate, next.openReturned = make(chan struct{}), make(chan struct{}), make(chan struct{})
	next.ignoreOpenCancel = true
	f.queueReception(next, nil)
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	f.r.recheck()
	select {
	case <-next.openEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement reception did not reach Open")
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(next.openGate) }) }
	t.Cleanup(release)

	f.r.Stop()
	_, _, closes := next.state()
	if closes != 1 {
		t.Fatalf("Stop returned with pending reception close count = %d, want 1", closes)
	}
	release()
	select {
	case <-next.openReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement Open did not return after its gate was released")
	}
	f.waitDone(t)
	_, _, closes = next.state()
	if closes != 1 {
		t.Fatalf("Stop returned with pending reception close count = %d, want 1", closes)
	}
}

func TestOpenReceptionFactoryReturnsWhenContextIsCanceled(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unlock := func() { releaseOnce.Do(func() { close(release) }) }
	f.r.cfg.ReceptionFactory = func(ctx context.Context) (RescueReception, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return freshRescueReception(0x4e, "987654321098"), nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result, done := make(chan error, 1), make(chan struct{})
	go func() {
		_, _, _, err := f.r.openReception(ctx, nil)
		result <- err
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("openReception did not finish during cleanup")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("factory did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("openReception after factory cancellation = %v", err)
		}
	case <-time.After(50 * time.Millisecond):
		unlock()
		select {
		case <-result:
		case <-time.After(time.Second):
			t.Fatal("openReception remained blocked after releasing the factory")
		}
		t.Fatal("openReception factory did not return when its context was canceled")
	}
}

func TestReceptionOpenedNotificationIsSuppressedAfterStop(t *testing.T) {
	events := make(chan RescueEvent, 1)
	r := &Rescue{cfg: RescueConfig{OnStateChange: func(event RescueEvent) { events <- event }}, stop: make(chan struct{})}
	r.Stop()
	r.emit(RescueEvent{Kind: RescueEventReceptionOpened, Code: "late-code"})
	select {
	case event := <-events:
		t.Fatalf("notification after Stop = %+v", event)
	default:
	}
}

func TestReceptionOpenedNotificationRacingStopCannotResume(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	_ = waitRescueEvent(t, f.events, RescueEventReceptionOpened)
	code := f.rec.code
	originalNotify := f.r.cfg.OnStateChange
	notificationEntered := make(chan RescueEvent, 1)
	releaseNotification := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseNotification) }) }
	defer release()
	f.r.cfg.OnStateChange = func(event RescueEvent) {
		if event.Kind == RescueEventReceptionOpened && event.Code == code {
			notificationEntered <- event
			<-releaseNotification
		}
		originalNotify(event)
	}
	emitDone := make(chan struct{})
	go func() {
		f.r.emit(RescueEvent{Kind: RescueEventReceptionOpened, Code: code})
		close(emitDone)
	}()
	select {
	case event := <-notificationEntered:
		if event.Code != code {
			t.Fatalf("matched code = %q, want %q", event.Code, code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("code did not reach the notification sync point")
	}
	stopReturned := make(chan struct{})
	go func() {
		f.r.Stop()
		close(stopReturned)
	}()
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		release()
		<-stopReturned
		t.Fatal("Stop waited for the blocked notification callback")
	}
	if f.rec.HostKey() != nil {
		t.Fatal("the post-Stop notification still had a usable K")
	}
	release()
	late := waitRescueEvent(t, f.events, RescueEventReceptionOpened)
	if late.Code != code {
		t.Fatalf("late code = %q, want %q", late.Code, code)
	}
	if err := f.rec.Resume(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatalf("resume using the late code = %v, want ErrDenied", err)
	}
	if err := f.r.Receive(rescueRequestFor(rescueC.PublicKey())); !errors.Is(err, ErrDenied) {
		t.Fatalf("request after the late code = %v, want ErrDenied", err)
	}
	select {
	case <-emitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("late notification callback did not return")
	}
	f.waitDone(t)
}

func TestRescueStopStartsDetachedReceptionCloseBeforeReturning(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	current, pending := f.rec, newRescueReception()
	current.closeStarted, pending.closeStarted = make(chan struct{}), make(chan struct{})
	current.closeReturned, pending.closeReturned = make(chan struct{}), make(chan struct{})
	releaseCurrent := make(chan struct{})
	var releaseCurrentOnce sync.Once
	closeCurrent := func() { releaseCurrentOnce.Do(func() { close(releaseCurrent) }) }
	current.onClose = func() { <-releaseCurrent }
	t.Cleanup(closeCurrent)
	f.r.mu.Lock()
	f.r.state = rescueReopening
	f.r.reception = current
	f.r.pendingReception = &rescueReceptionCandidate{reception: pending}
	f.r.mu.Unlock()

	finishDone := make(chan struct{})
	go func() {
		f.r.finish(false, false, false)
		close(finishDone)
	}()
	select {
	case <-current.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("end did not start closing the adopted reception")
	}
	stopDone := make(chan struct{})
	go func() {
		f.r.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for a reception Close that had already started")
	}
	select {
	case <-pending.closeStarted:
	default:
		t.Fatal("Stop returned before starting Close on the detached pre-adoption candidate")
	}
	if pending.HostKey() != nil {
		t.Fatal("pre-adoption candidate retained K after Stop returned")
	}
	closeCurrent()
	select {
	case <-finishDone:
	case <-time.After(time.Second):
		t.Fatal("end did not finish after the current reception Close was released")
	}
	select {
	case <-pending.closeReturned:
	case <-time.After(time.Second):
		t.Fatal("candidate Close did not finish")
	}
}

func TestRescueStopBoundsStalledReceptionAndReportsCleanup(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	f.allow(t, f.prompt(t))
	approved := f.r.approved
	f.r.cfg.CleanupTimeout = 40 * time.Millisecond
	pending := newRescueReception()
	pending.closeStarted, pending.closeReturned = make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseClose := func() { releaseOnce.Do(func() { close(release) }) }
	pending.onClose = func() { <-release }
	t.Cleanup(releaseClose)
	f.r.mu.Lock()
	f.r.state = rescueReopening
	f.r.pendingReception = &rescueReceptionCandidate{reception: pending}
	f.r.mu.Unlock()

	stopDone := make(chan struct{})
	go func() {
		f.r.Stop()
		close(stopDone)
	}()
	select {
	case <-pending.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("Stop did not synchronously start the pending reception Close")
	}
	if approved.issuer.Snapshot().HasAuthority {
		t.Fatal("the grant remained live after the stalled reception Close started")
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return while reception Close was blocked")
	}
	select {
	case <-f.r.Done():
	case <-time.After(time.Second):
		t.Fatal("Done did not finish within the cleanup budget")
	}
	if approved.issuer.Snapshot().HasAuthority {
		t.Fatal("Done closed while the grant was still live")
	}
	_ = waitRescueEvent(t, f.events, RescueEventCleanupRequired)
	releaseClose()
	select {
	case <-pending.closeReturned:
	case <-time.After(time.Second):
		t.Fatal("stalled reception Close did not finish after release")
	}
}

func TestRescueDoneBoundsWaitingForApprovedCloseLock(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	f.r.cfg.CleanupTimeout = 40 * time.Millisecond
	f.allow(t, f.prompt(t))
	approved := f.r.approved
	approved.closeMu.Lock()
	locked := true
	defer func() {
		if locked {
			approved.closeMu.Unlock()
		}
	}()
	stopDone := make(chan struct{})
	go func() {
		f.r.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for Approved.Close's lock")
	}
	select {
	case <-f.r.Done():
	case <-time.After(time.Second):
		t.Fatal("Done waited beyond CleanupTimeout for Approved.Close's lock")
	}
	if approved.issuer.Snapshot().HasAuthority {
		t.Fatal("Done closed while the grant was still live")
	}
	_ = waitRescueEvent(t, f.events, RescueEventCleanupRequired)
	approved.closeMu.Unlock()
	locked = false
	deadline := time.After(time.Second)
	for {
		if _, observed := approved.CleanupResult(); observed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("bounded Approved.Close did not finish after releasing the lock")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRescueResumeNotifiesWithoutShowingNativePrompt(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	runtime := &rescueWorkRuntime{started: make(chan struct{})}
	f.r.runtime = runtime
	prompt := f.prompt(t)
	d := f.allow(t, prompt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := openChannel(ctx, t, f, d, rescueC)
	if err != nil {
		t.Fatalf("generation 1 channel: %v", err)
	}
	waitHostAuthenticated(t, f.r.approved)
	type openResult struct {
		channel *agentchannel.Channel
		err     error
	}
	opened := make(chan openResult, 1)
	releaseResume := make(chan struct{})
	var releaseResumeOnce sync.Once
	defer releaseResumeOnce.Do(func() { close(releaseResume) })
	f.rec.onResume = func() {
		<-releaseResume
		f.rec.requests <- verifiedFor(t, rescueC.PublicKey(), rescueK)
	}
	f.rec.onAllow = func(dispatch Dispatch, conn net.Conn) {
		if dispatch.Binding.ConnectionGeneration == 1 {
			return
		}
		go func() {
			hostKey, parseErr := ssh.ParsePublicKey(dispatch.HostKey)
			if parseErr != nil {
				opened <- openResult{err: parseErr}
				return
			}
			channel, openErr := agentchannel.Open(context.Background(), agentchannel.ClientConfig{
				Binding: dispatch.Binding, HostKey: hostKey, Signer: rescueC, Deadline: dispatch.Deadline, Budgets: f.cfg.Budgets,
			}, conn)
			opened <- openResult{channel: channel, err: openErr}
		}()
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close generation 1: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	f.clocks.advance(4*time.Minute+59*time.Second, 4*time.Minute+59*time.Second)
	f.clocks.recheck(t)
	f.requireLive(t)
	releaseResumeOnce.Do(func() { close(releaseResume) })
	var resumed openResult
	select {
	case resumed = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("generation 2 channel did not open")
	}
	if resumed.err != nil || resumed.channel == nil {
		t.Fatalf("generation 2 channel = %v", resumed.err)
	}
	defer resumed.channel.Close()
	_ = waitRescueEvent(t, f.events, RescueEventReconnected)
	waitHostAuthenticated(t, f.r.approved)
	if len(f.helper.shown()) != 1 {
		t.Fatalf("reconnect showed %d native prompts, want only the original prompt", len(f.helper.shown()))
	}
	client, err := agentwire.NewClient(resumed.channel)
	if err != nil {
		t.Fatalf("generation 2 wire client: %v", err)
	}
	status, err := client.Start(ctx, "resume-work", []byte("whoami\n"), 60_000)
	if err != nil {
		t.Fatalf("generation 2 work start = (%+v, %v)", status, err)
	}
	select {
	case <-runtime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation 2 work did not reach the guest")
	}
	deadline := time.Now().Add(2 * time.Second)
	for status.Phase != agentexec.PhaseRunning && time.Now().Before(deadline) {
		status, err = client.Status(ctx, status.CommandID)
		if err != nil {
			t.Fatalf("generation 2 work status = %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	if status.Phase != agentexec.PhaseRunning {
		t.Fatalf("generation 2 work phase = %v, want running", status.Phase)
	}
	f.r.Stop()
	f.waitDone(t)
}

func TestRescueUnknownCleanupRequiresCleanupAndDoesNotReopen(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	runtime := &rescueUncleanRuntime{started: make(chan struct{})}
	f.r.runtime = runtime
	prompt := f.prompt(t)
	d := f.allow(t, prompt)
	f.r.mu.Lock()
	approved := f.r.approved
	f.r.mu.Unlock()
	status, err := approved.session.Start(agentexec.StartRequest{
		Version: agentexec.Version, Binding: approved.session.Binding(), RequestID: "rescue-cleanup-unknown",
		Mode: approved.session.Mode(), Command: []byte("whoami\n"), TimeoutMillis: 60_000,
	})
	if err != nil {
		t.Fatalf("start fake guest work: %v", err)
	}
	select {
	case <-runtime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("fake guest did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := openChannel(ctx, t, f, d, rescueC)
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	waitHostAuthenticated(t, approved)
	if err := ch.Close(); err != nil {
		t.Fatalf("close channel: %v", err)
	}
	f.waitDone(t)
	cleanupErr, observed := f.r.CleanupResult()
	if !observed || !errors.Is(cleanupErr, agentexec.ErrCleanupUnknown) {
		t.Fatalf("cleanup result = %v observed=%v after command %s", cleanupErr, observed, status.CommandID)
	}
	_ = waitRescueEvent(t, f.events, RescueEventCleanupRequired)
	if f.factoryCallCount() != 1 || f.rec.resumes != 0 {
		t.Fatalf("factory calls = %d, resumes = %d after unknown cleanup", f.factoryCallCount(), f.rec.resumes)
	}
}

func TestRescueDeadlineCancelsOfflineReceptionReopen(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	prompt := f.prompt(t)
	d := f.allow(t, prompt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := openChannel(ctx, t, f, d, rescueC)
	if err != nil {
		t.Fatalf("generation 1 channel: %v", err)
	}
	waitHostAuthenticated(t, f.r.approved)
	if err := ch.Close(); err != nil {
		t.Fatalf("close generation 1: %v", err)
	}
	_ = waitRescueEvent(t, f.events, RescueEventReconnecting)
	f.queueReception(nil, errors.New("gateway offline"))
	f.queueReception(newRescueReception(), nil)
	f.clocks.advance(5*time.Minute+time.Second, 5*time.Minute+time.Second)
	f.r.recheck()
	_ = waitRescueEvent(t, f.events, RescueEventNewReception)
	waitFactoryCalls(t, f, 2)
	if f.factoryCallCount() != 2 {
		t.Fatalf("factory calls after offline reopen = %d, want initial and one retry", f.factoryCallCount())
	}
	select {
	case <-f.r.Done():
		t.Fatal("offline new-reception attempt ended the Rescue before the grant deadline")
	default:
	}
	f.clocks.advance(4*time.Hour, 4*time.Hour)
	f.r.recheck()
	select {
	case <-f.r.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("grant deadline did not end Rescue")
	}
	if f.factoryCallCount() != 2 {
		t.Fatalf("expired grant retried reception factory %d times", f.factoryCallCount())
	}
}

func TestRescueStopRevokesPendingIssuingGrantBeforeDone(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	cfg := config(t)
	cfg.HostKey = f.rec.HostKey()
	cfg.AllowedKeys = []ssh.PublicKey{rescueC.PublicKey()}
	cfg.Runtime = f.r.cfg.Runtime
	cfg.Deadline = time.Now().Add(time.Hour)
	cfg.CleanupTimeout = f.r.cfg.CleanupTimeout
	cfg.Budgets = f.r.cfg.Budgets
	pending, err := prepare(cfg, f.r.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.issuer.Launch(context.Background(), pending.challenge, pending.clock, func(context.Context, *supervisor.Authority) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !pending.issuer.Snapshot().HasAuthority {
		t.Fatal("test setup did not create an issuing grant")
	}
	f.r.mu.Lock()
	f.r.state, f.r.issuing = rescueIssuing, pending
	f.r.mu.Unlock()
	f.r.Stop()
	f.waitDone(t)
	if pending.issuer.Snapshot().HasAuthority {
		t.Fatal("Done closed while the pending issuing grant was still live")
	}
}

func TestRescueStopRevokesUnregisteredGrantBeforeDone(t *testing.T) {
	f := newRescueFixture(t, 4*time.Hour)
	f.r.cfg.CleanupTimeout = 40 * time.Millisecond
	prompt := f.prompt(t)
	approvedReady := make(chan *Approved)
	continueIssue := make(chan struct{})
	unregistered := make(chan struct{}, 1)
	stopGateEntered := make(chan struct{})
	continueStop := make(chan struct{})
	answerDone := make(chan struct{})
	stopDone := make(chan struct{})
	var issueOnce, stopReleaseOnce, stopGateOnce sync.Once
	var approved *Approved
	lifeCancel := f.r.cancel
	closeMuLocked := false
	stopStarted := false
	releaseIssue := func() { issueOnce.Do(func() { close(continueIssue) }) }
	releaseStop := func() { stopReleaseOnce.Do(func() { close(continueStop) }) }
	f.r.issueTestSync = func(point rescueIssueTestPoint, a *Approved) {
		switch point {
		case rescueIssueAfterComplete:
			approvedReady <- a
			<-continueIssue
		case rescueIssueUnregistered:
			unregistered <- struct{}{}
		}
	}
	f.r.mu.Lock()
	f.r.reopenCancel = func() {
		stopGateOnce.Do(func() { close(stopGateEntered) })
		<-continueStop
	}
	f.r.cancel = nil
	f.r.mu.Unlock()
	go func() {
		_ = f.r.Answer(f.helper, RescueReply{RequestID: prompt.RequestID, Digest: prompt.Digest(), Allow: true})
		close(answerDone)
	}()
	t.Cleanup(func() {
		releaseIssue()
		releaseStop()
		if closeMuLocked {
			approved.closeMu.Unlock()
		}
		if lifeCancel != nil {
			lifeCancel()
		}
		select {
		case <-answerDone:
		case <-time.After(5 * time.Second):
			t.Error("Answer did not return after releasing test gates")
		}
		if stopStarted {
			select {
			case <-stopDone:
			case <-time.After(5 * time.Second):
				t.Error("Stop did not return after releasing test gates")
			}
		}
	})
	select {
	case approved = <-approvedReady:
	case <-time.After(5 * time.Second):
		t.Fatal("completeApproved did not return an Approved owner")
	}
	if !approved.issuer.Snapshot().HasAuthority {
		t.Fatal("test setup did not create a live grant")
	}
	approved.closeMu.Lock()
	closeMuLocked = true
	stopStarted = true
	go func() {
		f.r.Stop()
		close(stopDone)
	}()
	select {
	case <-stopGateEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not set stopRequested before entering its gate")
	}
	releaseIssue()
	select {
	case <-unregistered:
	case <-time.After(5 * time.Second):
		t.Fatal("issue did not clear issuing after Stop was requested")
	}
	f.r.mu.Lock()
	noOwner := f.r.stopRequested && f.r.issuing == nil && f.r.approved == nil
	f.r.mu.Unlock()
	if !noOwner {
		t.Fatal("test did not reach the unregistered-grant window")
	}
	releaseStop()
	f.waitDone(t)
	if approved.issuer.Snapshot().HasAuthority {
		t.Fatal("Done closed while the grant was still live")
	}
	approved.closeMu.Unlock()
	closeMuLocked = false
	select {
	case <-answerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Answer did not return after Approved.Close acquired closeMu")
	}
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after Done closed")
	}
}

// A host-side end while the grant is handed to the reception waits for that
// hand-over and then revokes the grant: Done never closes over a live grant.
func TestRescueStopWhileIssuingRevokesGrant(t *testing.T) {
	f := newRescueFixture(t, 0)
	p := f.prompt(t)
	f.rec.entered, f.rec.gate = make(chan struct{}), make(chan struct{})
	answered := make(chan error, 1)
	go func() {
		answered <- f.r.Answer(f.helper, RescueReply{RequestID: p.RequestID, Digest: p.Digest(), Allow: true})
	}()
	select {
	case <-f.rec.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the grant was not handed to the reception within 10s")
	}
	f.r.Stop()
	select {
	case <-f.r.Done():
		t.Fatal("Rescue ended while its issued grant was still being handed over")
	case <-time.After(200 * time.Millisecond):
	}
	close(f.rec.gate)
	select {
	case <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("Answer did not return within 10s")
	}
	f.waitDone(t)
	_, allows, closes := f.rec.state()
	if len(allows) != 1 || closes != 1 {
		t.Fatalf("allows %d, closes %d, want 1 and 1", len(allows), closes)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ch, err := openChannel(ctx, t, f, allows[0], rescueC); err == nil {
		_ = ch.Close()
		t.Fatal("C opened a channel after the end")
	}
}

// 期限とホスト側の切断: the grant is revoked, so C and the old Binding no longer open a channel.
func TestRescueEndRevokesGrant(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*testing.T, *rescueFixture)
	}{
		{"ホスト側の切断", func(t *testing.T, f *rescueFixture) { f.r.Stop() }},
		{"単調時計で期限", func(t *testing.T, f *rescueFixture) {
			f.clocks.advance(2*time.Hour-time.Nanosecond, 0)
			f.clocks.settle(t)
			f.requireLive(t)
			f.clocks.advance(time.Nanosecond, 0)
			f.clocks.recheckEnding(t, f.r.Done())
		}},
		{"壁時計で期限", func(t *testing.T, f *rescueFixture) {
			f.clocks.advance(0, 2*time.Hour-time.Nanosecond)
			f.clocks.settle(t)
			f.requireLive(t)
			f.clocks.advance(0, time.Nanosecond)
			f.clocks.recheckEnding(t, f.r.Done())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRescueFixture(t, 2*time.Hour)
			d := f.allow(t, f.prompt(t))
			tc.end(t, f)
			f.waitDone(t)
			if _, _, closes := f.rec.state(); closes != 1 {
				t.Fatalf("the reception was closed %d times, want 1 (K discarded)", closes)
			}
			// 停止と回収: the grant's owner ran its cleanup, not only lost its authority.
			f.r.mu.Lock()
			owner := f.r.approved
			f.r.mu.Unlock()
			if owner == nil {
				t.Fatal("no grant was recorded")
			}
			if _, observed := owner.CleanupResult(); !observed {
				t.Fatal("the end did not reclaim the grant")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if ch, err := openChannel(ctx, t, f, d, rescueC); err == nil {
				_ = ch.Close()
				t.Fatal("C with the old Binding opened a channel after the end")
			}
		})
	}
}

func TestRescueEndsReceptionBeforeClosingApprovedChannel(t *testing.T) {
	f := newRescueFixture(t, 2*time.Hour)
	f.rec.acceptRead = make(chan struct{})
	f.allow(t, f.prompt(t))
	select {
	case <-f.rec.acceptRead:
	case <-time.After(2 * time.Second):
		t.Fatal("the approved channel did not enter Accept before Stop")
	}
	a := f.r.approved
	cleanupReporter, ok := any(f.r).(interface{ CleanupResult() (error, bool) })
	if !ok {
		t.Fatal("Rescue does not expose its approved cleanup result")
	}
	cleanupObservedAtReceptionClose := make(chan bool, 1)
	f.rec.onClose = func() {
		_, observed := a.CleanupResult()
		cleanupObservedAtReceptionClose <- observed
	}
	f.r.Stop()
	f.waitDone(t)
	if observed := <-cleanupObservedAtReceptionClose; observed {
		t.Fatal("Approved channel cleanup ran before the reception sent end and closed")
	}
	approvedErr, observed := a.CleanupResult()
	if !observed || !errors.Is(approvedErr, agentchannel.ErrCleanupUnknown) {
		serveErr, serveObserved := a.host.CleanupResult()
		t.Fatalf("Approved cleanup result = (%v, %v), want ErrCleanupUnknown and observed; Serve cleanup = (%v, %v)", approvedErr, observed, serveErr, serveObserved)
	}
	rescueErr, observed := cleanupReporter.CleanupResult()
	if !observed || !errors.Is(rescueErr, agentchannel.ErrCleanupUnknown) {
		t.Fatalf("Rescue cleanup result = (%v, %v), want ErrCleanupUnknown and observed", rescueErr, observed)
	}
}

func TestRescueStopAfterEstablishedChannelDoesNotRequireCleanup(t *testing.T) {
	f := newRescueFixture(t, 2*time.Hour)
	_ = waitRescueEvent(t, f.events, RescueEventReceptionOpened)
	d := f.allow(t, f.prompt(t))
	approved := f.r.approved
	channel, err := openChannel(context.Background(), t, f, d, rescueC)
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	waitHostAuthenticated(t, approved)

	f.r.Stop()
	f.waitDone(t)
	if cleanupErr, observed := approved.CleanupResult(); !observed || cleanupErr != nil {
		t.Fatalf("Approved cleanup result = (%v, %v), want successful observed cleanup", cleanupErr, observed)
	}
	for {
		select {
		case event := <-f.events:
			if event.Kind == RescueEventCleanupRequired {
				t.Fatal("normal Stop after channel establishment reported CleanupRequired")
			}
		default:
			return
		}
	}
}

// Only the #399 lifetimes; zero selects the 2-hour default.
func TestNewRescueAcceptsOnlyChosenLifetimes(t *testing.T) {
	base := newRescueFixture(t, 0).cfg
	base.ReceptionFactory = func(context.Context) (RescueReception, error) {
		return newRescueReception(), nil
	}
	for _, d := range []time.Duration{0, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour} {
		cfg := base
		cfg.Duration = d
		r, err := newRescue(cfg, &neverRuntime{}, newRescueClocks().clock())
		if err != nil {
			t.Errorf("Duration %v: newRescue = %v", d, err)
			continue
		}
		r.Stop()
	}
	for _, d := range []time.Duration{-time.Hour, time.Hour, 3 * time.Hour, 25 * time.Hour} {
		cfg := base
		cfg.Duration = d
		if _, err := newRescue(cfg, &neverRuntime{}, newRescueClocks().clock()); !errors.Is(err, ErrConfig) {
			t.Errorf("Duration %v: newRescue = %v, want ErrConfig", d, err)
		}
	}
	for name, change := range map[string]func(*RescueConfig){
		"Helper なし":            func(c *RescueConfig) { c.Helper = nil },
		"Reception factory なし": func(c *RescueConfig) { c.ReceptionFactory = nil },
	} {
		cfg := base
		change(&cfg)
		if _, err := newRescue(cfg, &neverRuntime{}, newRescueClocks().clock()); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: newRescue = %v, want ErrConfig", name, err)
		}
	}
}
