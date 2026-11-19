package maclaunch

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
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
	mu      sync.Mutex
	done    chan struct{}
	refuses []bool
	allows  []Dispatch
	client  net.Conn
	closes  int
	// entered and gate, when set before the answer, hold Allow open as a slow
	// write to the gateway would.
	entered, gate chan struct{}
}

func newRescueReception() *rescueReception { return &rescueReception{done: make(chan struct{})} }

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
	defer r.mu.Unlock()
	host, client := memPair()
	r.allows = append(r.allows, d)
	r.client = client
	return host, nil
}

func (r *rescueReception) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}

func (r *rescueReception) lose() { close(r.done) }

func (r *rescueReception) state() (refuses []bool, allows []Dispatch, closes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.refuses...), append([]Dispatch(nil), r.allows...), r.closes
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

type rescueFixture struct {
	r      *Rescue
	helper *rescueHelper
	rec    *rescueReception
	clocks *rescueClocks
	cfg    RescueConfig
}

func newRescueFixture(t *testing.T, d time.Duration) *rescueFixture {
	t.Helper()
	base := config(t)
	budgets := base.Budgets
	budgets.Handshake = 10 * time.Second // the tests open the channel themselves after the answer
	f := &rescueFixture{helper: &rescueHelper{}, rec: newRescueReception(), clocks: newRescueClocks()}
	f.cfg = RescueConfig{HostKey: rescueK, Runtime: base.Runtime, Duration: d, CleanupTimeout: base.CleanupTimeout, Budgets: budgets, Helper: f.helper, Reception: f.rec}
	r, err := newRescue(f.cfg, &neverRuntime{}, f.clocks.clock())
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
	_ = ch.Close()
	// No grace in this slice: the channel's end is the end, and K is discarded.
	f.waitDone(t)
	if _, _, closes := f.rec.state(); closes != 1 {
		t.Fatalf("the reception was closed %d times, want 1", closes)
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

// Only the #399 lifetimes; zero selects the 2-hour default.
func TestNewRescueAcceptsOnlyChosenLifetimes(t *testing.T) {
	base := newRescueFixture(t, 0).cfg
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
		"HostKey なし":   func(c *RescueConfig) { c.HostKey = nil },
		"Helper なし":    func(c *RescueConfig) { c.Helper = nil },
		"Reception なし": func(c *RescueConfig) { c.Reception = nil },
	} {
		cfg := base
		change(&cfg)
		if _, err := newRescue(cfg, &neverRuntime{}, newRescueClocks().clock()); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: newRescue = %v, want ErrConfig", name, err)
		}
	}
}
