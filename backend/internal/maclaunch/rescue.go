package maclaunch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"golang.org/x/crypto/ssh"
)

// The host's rescue permission state machine: 受付中 → 確認中 → セッション中 →
// 終了 (docs/rescue-connection-start.md, 3. 許可と拒否, ホストの状態, 期限とホスト側の切断).
// Only a reply from the native helper approves it; it is the second approval
// source of this package beside the controlling terminal. It is for tests at
// the current boundary (current user, no isolation) and is not enabled as a
// product: the Mac guest account is not designed yet.

// RescueDurations are the selectable grant lifetimes from the time of
// approval (#399). A zero RescueConfig.Duration selects RescueDefaultDuration.
var RescueDurations = [...]time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour}

const (
	RescueDefaultDuration = 2 * time.Hour
	// RescuePromptTimeout is the automatic refusal, counted from Show on both
	// the monotonic and the wall clock.
	RescuePromptTimeout = 60 * time.Second
	RescueGracePeriod   = 5 * time.Minute
	// RescueWarning is shown verbatim in every prompt; the digest covers it.
	RescueWarning = "隔離なし: 許可すると、担当者がこの Mac の現在のユーザーとして任意のコマンドを実行でき、ファイル・鍵・ネットワークにアクセスできます。"
)

// RescueRequest is a rescue request whose identity proof was already verified
// against the pinned identity keys, this reception id and K. Its fields are
// unexported: only that verification (after #423) and this package's tests
// construct it.
type RescueRequest struct {
	client                             ssh.PublicKey // C
	displayName, organization, profile string
}

// RescuePrompt is what the native helper displays.
type RescuePrompt struct {
	RequestID         string // fresh per request, chosen by the host
	Organization      string // 「{組織} が確認: {表示名}」
	DisplayName       string
	Profile           string        // 操作範囲
	Duration          time.Duration // lifetime from the time of approval
	ClientFingerprint string        // ssh.FingerprintSHA256 of C
	Warning           string        // RescueWarning
}

// Digest is SHA-256 over the domain "rescue-host.maclaunch.rescue-prompt\x00v1\x00"
// followed by each field in declaration order as a 4-byte big-endian length and
// its bytes; Duration is its whole seconds in decimal. The helper returns the
// digest of what it displayed.
func (p RescuePrompt) Digest() [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("rescue-host.maclaunch.rescue-prompt\x00v1\x00"))
	for _, v := range []string{p.RequestID, p.Organization, p.DisplayName, p.Profile, strconv.FormatInt(int64(p.Duration/time.Second), 10), p.ClientFingerprint, p.Warning} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(v)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(v))
	}
	return [32]byte(h.Sum(nil))
}

// RescueHelper is the native helper's connection as Go sees it. #410
// authenticates that connection; a WebView IPC handler never holds it. Answer
// compares it with ==, so it is a pointer.
type RescueHelper interface {
	// Show hands p to the helper. It does not wait for the user's answer.
	Show(RescuePrompt) error
	// Withdraw removes the prompt after a timeout, reception loss or host-side end.
	Withdraw(requestID string)
}

// RescueReply is the helper's answer to one prompt.
type RescueReply struct {
	RequestID string
	Digest    [32]byte
	Allow     bool
}

// RescueReception is the host end of the reception connection, owner of K.
type RescueReception interface {
	Open(context.Context) (string, error)
	Resume(context.Context) error
	Request() <-chan agenttransport.VerifiedRescueIdentity
	HostKey() ssh.Signer
	// Done closes when the reception connection ends, including code expiry.
	Done() <-chan struct{}
	// Refuse returns a K-signed deny, or timeout when timedOut.
	Refuse(timedOut bool) error
	// Allow returns a K-signed allow with d and the stream that now carries the channel.
	Allow(d Dispatch) (net.Conn, error)
	// Close ends the reception and discards K. The state machine may call it
	// from any goroutine, including one in Receive or Answer, so it must not
	// wait for those calls to return.
	Close() error
}

type RescueEventKind uint8

const (
	RescueEventReceptionOpened RescueEventKind = iota + 1
	RescueEventReconnecting
	RescueEventReconnected
	RescueEventNewReception
	RescueEventCleanupRequired
)

type RescueEvent struct {
	Kind RescueEventKind
	Code string
}

// The reception the product uses owns one K and its matching Resume attempts.
var _ RescueReception = (*agenttransport.RescueClient)(nil)

// RescueConfig is supplied at 受付 ON.
type RescueConfig struct {
	Runtime          macagent.Config
	Duration         time.Duration
	CleanupTimeout   time.Duration
	Budgets          agentchannel.Budgets
	Helper           RescueHelper
	ReceptionFactory func(context.Context) (RescueReception, error)
	OnStateChange    func(RescueEvent)
}

type rescueClock struct {
	wall func() time.Time
	mono func() time.Duration // elapsed on a monotonic clock
	tick <-chan time.Time     // rechecks the 60-second prompt and the grant deadline
}

type rescueState uint8

const (
	rescueAccepting rescueState = iota
	rescueConfirming
	rescueIssuing
	rescueSession
	rescueGrace
	rescueReopening
	rescueNeedsCleanup
	rescueEnded
)

type rescueReceptionCandidate struct {
	reception RescueReception
	once      sync.Once
}

func (c *rescueReceptionCandidate) close() {
	if c != nil {
		c.once.Do(func() { _ = c.reception.Close() })
	}
}

// Rescue owns the reception factory, current K, approval, and every connection
// generation from 受付中 through expiry or a host-side end.
type Rescue struct {
	// step serializes the transitions that call the helper or the reception
	// (Show, the grant's issuance and hand-over) with the end, so the end
	// withdraws only a shown prompt and revokes every issued grant before Done.
	step             sync.Mutex
	mu               sync.Mutex
	state            rescueState
	cfg              RescueConfig
	runtime          agentexec.ProfiledRuntime
	helper           RescueHelper
	reception        RescueReception
	pendingReception *rescueReceptionCandidate
	clock            rescueClock
	prompt           RescuePrompt
	client           ssh.PublicKey
	shownWall        time.Time
	shownMono        time.Duration
	approvedWall     time.Time
	approvedMono     time.Duration
	lastFrameWall    time.Time
	lastFrameMono    time.Duration
	graceWall        time.Time
	graceMono        time.Duration
	lastHostKey      []byte
	resumeRunning    bool
	resumeHandoff    bool
	resumeReception  RescueReception
	resumeApproved   *Approved
	resumeGeneration uint64
	serveGeneration  uint64
	reopenRunning    bool
	reopenCancel     context.CancelFunc
	reopenSequence   uint64
	reopenActive     uint64
	stopRequested    bool
	approved         *Approved
	cleanupErr       error
	cleanupObserved  bool
	life             context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	stop             chan struct{}
	wake             chan struct{}
	stopOnce         sync.Once
	ticker           *time.Ticker
}

// NewRescue validates the configuration and the local Mac runtime in 受付中.
func NewRescue(cfg RescueConfig) (*Rescue, error) {
	if !localSupported() {
		return nil, ErrConfig
	}
	runtime, err := macagent.New(cfg.Runtime)
	if err != nil {
		return nil, ErrConfig
	}
	return newRescue(cfg, runtime, rescueClock{})
}
func newRescue(cfg RescueConfig, runtime agentexec.ProfiledRuntime, clock rescueClock) (*Rescue, error) {
	if cfg.Helper == nil || cfg.ReceptionFactory == nil || runtime == nil || runtime.AgentProfile() != agentexec.ProfileMacCurrentUser || cfg.CleanupTimeout <= 0 || cfg.CleanupTimeout > time.Minute {
		return nil, ErrConfig
	}
	if cfg.Duration == 0 {
		cfg.Duration = RescueDefaultDuration
	}
	valid := false
	for _, d := range RescueDurations {
		if cfg.Duration == d {
			valid = true
		}
	}
	if !valid {
		return nil, ErrConfig
	}
	if !validLocal(cfg.Runtime, cfg.Budgets) {
		return nil, ErrConfig
	}
	if clock.wall == nil {
		clock.wall = func() time.Time { return time.Now().Round(0) }
	}
	if clock.mono == nil {
		start := time.Now()
		clock.mono = func() time.Duration { return time.Since(start) }
	}
	var ticker *time.Ticker
	if clock.tick == nil {
		ticker = time.NewTicker(time.Second)
		clock.tick = ticker.C
	}
	life, cancel := context.WithCancel(context.Background())
	r := &Rescue{cfg: cfg, runtime: runtime, helper: cfg.Helper, clock: clock, life: life, cancel: cancel, done: make(chan struct{}), stop: make(chan struct{}), wake: make(chan struct{}, 1), ticker: ticker}
	reception, code, hostKey, err := r.openReception(life, nil)
	if err != nil {
		cancel()
		if ticker != nil {
			ticker.Stop()
		}
		return nil, errors.Join(ErrConfig, err)
	}
	r.reception, r.lastHostKey = reception, hostKey
	r.emit(RescueEvent{Kind: RescueEventReceptionOpened, Code: code})
	go r.loop()
	return r, nil
}

func validRescueCode(code string) bool {
	if len(code) != 12 {
		return false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (r *Rescue) openReception(parent context.Context, previousKey []byte) (RescueReception, string, []byte, error) {
	reception, code, hostKey, _, err := r.openReceptionCandidate(parent, previousKey, nil)
	return reception, code, hostKey, err
}

func (r *Rescue) openReceptionCandidate(parent context.Context, previousKey []byte, publish func(*rescueReceptionCandidate) bool) (RescueReception, string, []byte, *rescueReceptionCandidate, error) {
	if parent == nil || parent.Err() != nil {
		return nil, "", nil, nil, context.Canceled
	}
	reception, err := r.cfg.ReceptionFactory(parent)
	if err != nil {
		return nil, "", nil, nil, err
	}
	if reception == nil {
		return nil, "", nil, nil, ErrConfig
	}
	candidate := &rescueReceptionCandidate{reception: reception}
	if publish != nil && !publish(candidate) {
		candidate.close()
		return nil, "", nil, candidate, context.Canceled
	}
	if err := parent.Err(); err != nil {
		candidate.close()
		return nil, "", nil, candidate, err
	}
	hostKey := reception.HostKey()
	if hostKey == nil || hostKey.PublicKey() == nil {
		candidate.close()
		return nil, "", nil, candidate, ErrConfig
	}
	public := hostKey.PublicKey()
	if _, err := agentchannel.CanonicalKeys([]ssh.PublicKey{public}); err != nil || (previousKey != nil && bytes.Equal(public.Marshal(), previousKey)) {
		candidate.close()
		return nil, "", nil, candidate, ErrConfig
	}
	ctx, cancel := context.WithTimeout(parent, agentchannel.MaxHandshake)
	defer cancel()
	code, err := reception.Open(ctx)
	if err != nil || !validRescueCode(code) {
		candidate.close()
		if err == nil {
			err = ErrConfig
		}
		return nil, "", nil, candidate, err
	}
	if err := parent.Err(); err != nil {
		candidate.close()
		return nil, "", nil, candidate, err
	}
	return reception, code, append([]byte(nil), public.Marshal()...), candidate, nil
}

func (r *Rescue) emit(event RescueEvent) {
	if r == nil || r.cfg.OnStateChange == nil {
		return
	}
	if event.Kind == RescueEventReceptionOpened {
		r.mu.Lock()
		stopped := r.stopRequested
		r.mu.Unlock()
		if stopped {
			return
		}
	}
	r.cfg.OnStateChange(event)
}

func (r *Rescue) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Receive moves 受付中 to 確認中 and returns after Show. Any other state, or a
// host-side end or lost reception not handled yet, refuses without a prompt.
// The 60 seconds count from before Show.
func (r *Rescue) Receive(req RescueRequest) error {
	if req.client == nil {
		return ErrDenied
	}
	return r.showPrompt(req)
}

func (r *Rescue) showPrompt(req RescueRequest) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return errors.Join(ErrDenied, err)
	}
	p := RescuePrompt{RequestID: hex.EncodeToString(id[:]), Organization: req.organization, DisplayName: req.displayName, Profile: req.profile, Duration: r.cfg.Duration, ClientFingerprint: ssh.FingerprintSHA256(req.client), Warning: RescueWarning}
	r.step.Lock()
	defer r.step.Unlock()
	if r.cutOff() { // the loop ends it
		return ErrDenied
	}
	r.mu.Lock()
	if r.state != rescueAccepting {
		r.mu.Unlock()
		return ErrDenied
	}
	r.state, r.prompt, r.client = rescueConfirming, p, req.client
	r.shownWall, r.shownMono = r.clock.wall(), r.clock.mono()
	r.mu.Unlock()
	if err := r.helper.Show(p); err != nil {
		r.end(false, false, false)
		return errors.Join(ErrDenied, err)
	}
	return nil
}

// ReceiveFrom passes the reception's one verified request to Receive. A
// request verified for another K than this Rescue's host key is another
// reception's and is denied. It returns ErrDenied without a request once the
// state machine has ended.
func (r *Rescue) ReceiveFrom(requests <-chan agenttransport.VerifiedRescueIdentity) error {
	select {
	case v := <-requests:
		r.mu.Lock()
		reception := r.reception
		r.mu.Unlock()
		hostKey := ssh.Signer(nil)
		if reception != nil {
			hostKey = reception.HostKey()
		}
		if hostKey == nil || !bytes.Equal(v.HostKey(), hostKey.PublicKey().Marshal()) {
			return ErrDenied
		}
		return r.Receive(RescueRequest{client: v.Client(), displayName: v.DisplayName(), organization: v.Organization(), profile: v.Profile()})
	case <-r.done:
		return ErrDenied
	}
}

func (r *Rescue) handleVerified(v agenttransport.VerifiedRescueIdentity) {
	r.mu.Lock()
	reception, state, client, approved := r.reception, r.state, r.client, r.approved
	r.mu.Unlock()
	var hostKey ssh.Signer
	if reception != nil {
		hostKey = reception.HostKey()
	}
	if hostKey == nil || !bytes.Equal(v.HostKey(), hostKey.PublicKey().Marshal()) {
		return
	}
	switch state {
	case rescueAccepting:
		_ = r.showPrompt(RescueRequest{client: v.Client(), displayName: v.DisplayName(), organization: v.Organization(), profile: v.Profile()})
	case rescueGrace:
		if client == nil || v.Client() == nil || !bytes.Equal(client.Marshal(), v.Client().Marshal()) || approved == nil {
			_ = reception.Refuse(false)
			return
		}
		r.resumeChannel(reception, approved)
	default:
		_ = reception.Refuse(false)
	}
}

func (r *Rescue) resumeChannel(reception RescueReception, approved *Approved) {
	r.step.Lock()
	if r.cutOff() {
		r.step.Unlock()
		return
	}
	r.mu.Lock()
	if r.state != rescueGrace || r.reception != reception || r.approved != approved {
		r.mu.Unlock()
		r.step.Unlock()
		_ = reception.Refuse(false)
		return
	}
	wall, mono := r.clock.wall(), r.clock.mono()
	deadline := r.expired(wall, mono)
	graceExpired := r.graceExpired(wall, mono)
	r.mu.Unlock()
	if deadline {
		r.end(false, false, false)
		r.step.Unlock()
		return
	}
	if graceExpired {
		r.expireGrace()
		r.step.Unlock()
		return
	}
	dispatch, err := approved.NextGeneration()
	if err != nil {
		r.step.Unlock()
		_ = reception.Refuse(false)
		return
	}
	conn, err := reception.Allow(dispatch)
	if err != nil {
		r.step.Unlock()
		return
	}
	r.mu.Lock()
	if r.state == rescueGrace && r.reception == reception {
		r.resumeHandoff = true
		r.resumeReception = reception
		r.resumeApproved = approved
		r.resumeGeneration = dispatch.Binding.ConnectionGeneration
	}
	r.mu.Unlock()
	r.step.Unlock()
	go r.serveApproved(approved, dispatch.Binding.ConnectionGeneration, conn)
}

// Answer accepts reply only from the helper this Rescue was built with, for the
// current prompt, within RescuePromptTimeout. Allow issues the grant. A
// host-side end that arrives meanwhile waits for the hand-over, then revokes it.
func (r *Rescue) Answer(from RescueHelper, reply RescueReply) error {
	r.step.Lock()
	defer r.step.Unlock()
	if r.cutOff() { // the loop ends it
		return ErrDenied
	}
	r.mu.Lock()
	if r.state != rescueConfirming || from == nil || from != r.helper || reply.RequestID != r.prompt.RequestID || reply.Digest != r.prompt.Digest() {
		r.mu.Unlock()
		return ErrDenied
	}
	wall, mono := r.clock.wall(), r.clock.mono()
	late := r.late(wall, mono)
	if late || !reply.Allow {
		r.mu.Unlock()
		r.end(late, true, late)
		if late {
			return ErrDenied
		}
		return nil
	}
	r.state, r.approvedWall, r.approvedMono = rescueIssuing, wall, mono
	client := r.client
	r.mu.Unlock()
	a, conn, err := r.issue(wall, client)
	if err != nil {
		r.end(false, false, false)
		return err
	}
	r.mu.Lock()
	r.state, r.approved = rescueSession, a
	r.cleanupErr, r.cleanupObserved = nil, false
	r.serveGeneration = 1
	r.mu.Unlock()
	go r.serveApproved(a, 1, conn)
	return nil
}

// late reports the 60 seconds on either clock, or the wall clock before the
// prompt. r.mu is held.
func (r *Rescue) late(wall time.Time, mono time.Duration) bool {
	return mono-r.shownMono >= RescuePromptTimeout || wall.Sub(r.shownWall) >= RescuePromptTimeout || wall.Before(r.shownWall)
}

// expired reports the chosen lifetime from the time of approval on either
// clock. r.mu is held.
func (r *Rescue) expired(wall time.Time, mono time.Duration) bool {
	return mono-r.approvedMono >= r.cfg.Duration || !wall.Before(r.approvedWall.Add(r.cfg.Duration))
}

// graceExpired is measured from the last valid channel frame on both clocks.
func (r *Rescue) graceExpired(wall time.Time, mono time.Duration) bool {
	return mono-r.graceMono >= RescueGracePeriod || !wall.Before(r.graceWall.Add(RescueGracePeriod)) || wall.Before(r.graceWall)
}

func (r *Rescue) serveApproved(a *Approved, generation uint64, conn net.Conn) {
	_ = a.Serve(r.life, conn)
	r.afterServe(a, generation)
}

func (r *Rescue) afterServe(a *Approved, generation uint64) {
	r.step.Lock()
	defer r.step.Unlock()
	r.mu.Lock()
	handoff := r.state == rescueGrace && r.resumeHandoff && r.resumeApproved == a &&
		r.resumeReception != nil && r.resumeReception == r.reception && r.resumeGeneration == generation
	if r.state == rescueEnded || r.state == rescueNeedsCleanup || r.approved != a ||
		(r.serveGeneration != generation && !handoff) {
		r.mu.Unlock()
		return
	}
	r.resumeHandoff, r.resumeRunning = false, false
	r.resumeReception, r.resumeApproved = nil, nil
	r.resumeGeneration = 0
	if a.CanResume() {
		wall, mono := r.lastFrameWall, r.lastFrameMono
		if wall.IsZero() {
			wall, mono = r.clock.wall(), r.clock.mono()
		}
		r.graceWall, r.graceMono = wall, mono
		r.state = rescueGrace
		r.mu.Unlock()
		r.emit(RescueEvent{Kind: RescueEventReconnecting})
		r.startResume()
		return
	}
	r.mu.Unlock()
	r.end(false, false, false)
}

func (r *Rescue) markReconnected(generation uint64, openAdmission func() error) bool {
	r.step.Lock()
	defer r.step.Unlock()
	r.mu.Lock()
	if r.state != rescueGrace || !r.resumeHandoff || r.approved == nil || r.approved != r.resumeApproved || r.reception == nil || r.reception != r.resumeReception || generation != r.resumeGeneration || r.stopRequested {
		r.mu.Unlock()
		return false
	}
	wall, mono := r.clock.wall(), r.clock.mono()
	if r.expired(wall, mono) {
		r.mu.Unlock()
		r.end(false, false, false)
		return false
	}
	if r.graceExpired(wall, mono) {
		r.mu.Unlock()
		r.expireGrace()
		return false
	}
	select {
	case <-r.reception.Done():
		r.mu.Unlock()
		return false
	default:
	}
	if openAdmission == nil {
		r.mu.Unlock()
		return false
	}
	if err := openAdmission(); err != nil {
		r.mu.Unlock()
		r.end(false, false, false)
		return false
	}
	r.state, r.resumeHandoff, r.serveGeneration = rescueSession, false, generation
	r.lastFrameWall, r.lastFrameMono = wall, mono
	r.mu.Unlock()
	r.emit(RescueEvent{Kind: RescueEventReconnected})
	return true
}

func (r *Rescue) startResume() {
	r.mu.Lock()
	if r.state != rescueGrace || r.resumeRunning || r.resumeHandoff || r.reception == nil {
		r.mu.Unlock()
		return
	}
	reception := r.reception
	r.resumeRunning = true
	r.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(r.life, r.cfg.Budgets.Handshake)
		_ = reception.Resume(ctx)
		cancel()
		r.mu.Lock()
		if r.state == rescueGrace && r.reception == reception {
			r.resumeRunning = false
		}
		r.mu.Unlock()
	}()
}

// cutOff reports a host-side end or a lost reception, even before the loop
// handles it.
func (r *Rescue) cutOff() bool {
	select {
	case <-r.stop:
		return true
	default:
	}
	r.mu.Lock()
	reception := r.reception
	r.mu.Unlock()
	if reception == nil {
		return false
	}
	select {
	case <-reception.Done():
		return true
	default:
		return false
	}
}

// issue grants C alone, with K as the host key, until the time of approval
// plus the chosen lifetime, through the same issuance as the terminal path,
// and hands its dispatch to the reception.
func (r *Rescue) issue(approvedAt time.Time, client ssh.PublicKey) (*Approved, net.Conn, error) {
	r.mu.Lock()
	reception := r.reception
	r.mu.Unlock()
	if reception == nil {
		return nil, nil, ErrConfig
	}
	hostKey := reception.HostKey()
	if hostKey == nil {
		return nil, nil, ErrConfig
	}
	p, err := prepare(Config{HostKey: hostKey, AllowedKeys: []ssh.PublicKey{client}, Runtime: r.cfg.Runtime, Deadline: approvedAt.Add(r.cfg.Duration), CleanupTimeout: r.cfg.CleanupTimeout, Budgets: r.cfg.Budgets}, r.runtime)
	if err != nil {
		return nil, nil, err
	}
	p.onActivity, p.onConnected, p.allowReconnect = r.noteActivity, r.markReconnected, true
	a, err := p.completeApproved(r.life)
	if err == nil {
		var conn net.Conn
		if conn, err = reception.Allow(a.Dispatch()); err == nil {
			return a, conn, nil
		}
	}
	if a == nil {
		_ = p.issuer.Close()
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CleanupTimeout)
	defer cancel()
	return nil, nil, errors.Join(err, a.Close(ctx))
}

// Stop is a host-side end: the stop operation, 受付 OFF, app exit, logout or restart.
// It does not wait.
func (r *Rescue) Stop() {
	r.mu.Lock()
	r.stopRequested = true
	r.stopOnce.Do(func() { close(r.stop) })
	if r.reopenCancel != nil {
		r.reopenCancel()
	}
	candidate := r.pendingReception
	r.mu.Unlock()
	candidate.close()
}

// Done closes after the end: the grant revoked, its cleanup run within
// CleanupTimeout, K discarded.
func (r *Rescue) Done() <-chan struct{} { return r.done }

func (r *Rescue) noteActivity() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lastFrameWall, r.lastFrameMono = r.clock.wall(), r.clock.mono()
	r.mu.Unlock()
}

// CleanupResult observes the approved session's last explicit cleanup.
// observed=false means no approved session cleanup result is available.
func (r *Rescue) CleanupResult() (err error, observed bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cleanupErr, r.cleanupObserved
}

func (r *Rescue) loop() {
	for {
		r.mu.Lock()
		reception := r.reception
		r.mu.Unlock()
		var receptionDone <-chan struct{}
		var requests <-chan agenttransport.VerifiedRescueIdentity
		if reception != nil {
			receptionDone, requests = reception.Done(), reception.Request()
		}
		select {
		case <-r.stop:
			r.finish(false, false, true)
			return
		case <-receptionDone:
			if r.finishReception(reception) {
				return
			}
		case v := <-requests:
			r.handleVerified(v)
		case <-r.wake:
			continue
		case <-r.clock.tick:
			if r.recheck() {
				return
			}
			r.startResume()
		case <-r.life.Done():
			r.finish(false, false, true)
			return
		}
	}
}

func (r *Rescue) finishReception(reception RescueReception) bool {
	r.step.Lock()
	defer r.step.Unlock()
	r.mu.Lock()
	current := r.reception
	r.mu.Unlock()
	if current != reception {
		return false
	}
	r.end(false, false, true)
	return true
}

// recheck evaluates all deadlines under step. The grant deadline takes
// priority over the grace deadline, so it cancels a pending reopen.
func (r *Rescue) recheck() bool {
	r.step.Lock()
	defer r.step.Unlock()
	r.mu.Lock()
	wall, mono := r.clock.wall(), r.clock.mono()
	timedOut := r.state == rescueConfirming && r.late(wall, mono)
	grantExpired := (r.state == rescueSession || r.state == rescueGrace || r.state == rescueReopening) && r.expired(wall, mono)
	graceExpired := r.state == rescueGrace && r.graceExpired(wall, mono)
	reopening := r.state == rescueReopening
	r.mu.Unlock()
	if timedOut || grantExpired {
		r.end(timedOut, timedOut, timedOut)
		return true
	}
	if graceExpired {
		r.expireGrace()
		return r.isDone()
	}
	if reopening {
		r.retryReceptionLocked()
	}
	return false
}

// finish takes step and ends, waiting for a transition in progress.
func (r *Rescue) finish(timedOut, refuse, withdraw bool) {
	r.step.Lock()
	defer r.step.Unlock()
	r.end(timedOut, refuse, withdraw)
}

// end runs once, with step held, so whoever decides the end also makes it: it
// withdraws a shown prompt (withdraw), sends a deny or a timeout (refuse),
// revokes the grant and runs its cleanup, discards K and closes Done.
func (r *Rescue) end(timedOut, refuse, withdraw bool) {
	r.mu.Lock()
	if r.state == rescueEnded || r.state == rescueNeedsCleanup {
		r.mu.Unlock()
		return
	}
	confirming, id, a := r.state == rescueConfirming, r.prompt.RequestID, r.approved
	reception := r.reception
	pending := r.pendingReception
	r.pendingReception = nil
	reopenCancel := r.reopenCancel
	r.reopenCancel = nil
	r.reopenRunning = false
	r.reopenActive = 0
	r.state = rescueEnded
	r.mu.Unlock()
	if reopenCancel != nil {
		reopenCancel()
	}
	if r.ticker != nil {
		r.ticker.Stop()
	}
	if confirming && withdraw {
		r.helper.Withdraw(id)
	}
	if refuse && reception != nil {
		_ = reception.Refuse(timedOut)
	}
	if reception != nil {
		_ = reception.Close()
	}
	pending.close()
	var cleanupErr error
	if a != nil {
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CleanupTimeout)
		cleanupErr = a.Close(ctx)
		cancel()
		_, observed := a.CleanupResult()
		r.mu.Lock()
		r.cleanupErr, r.cleanupObserved = cleanupErr, observed
		if cleanupErr != nil {
			r.state = rescueNeedsCleanup
		}
		r.mu.Unlock()
	}
	if cleanupErr != nil {
		r.emit(RescueEvent{Kind: RescueEventCleanupRequired})
	}
	r.cancel()
	close(r.done)
}

func (r *Rescue) isDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// expireGrace runs only with step held. It closes the old reception first,
// then revokes and cleans the Session before trying to register a fresh K.
func (r *Rescue) expireGrace() {
	r.mu.Lock()
	if r.state != rescueGrace {
		r.mu.Unlock()
		return
	}
	reception, approved := r.reception, r.approved
	r.reception = nil
	r.state = rescueReopening
	r.resumeRunning, r.resumeHandoff = false, false
	r.resumeReception, r.resumeApproved = nil, nil
	r.resumeGeneration = 0
	r.mu.Unlock()
	r.emit(RescueEvent{Kind: RescueEventNewReception})
	if reception != nil {
		_ = reception.Close()
	}
	var cleanupErr error
	if approved != nil {
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CleanupTimeout)
		cleanupErr = approved.Close(ctx)
		cancel()
		_, observed := approved.CleanupResult()
		r.mu.Lock()
		r.cleanupErr, r.cleanupObserved = cleanupErr, observed
		r.mu.Unlock()
	}
	if cleanupErr != nil {
		r.mu.Lock()
		r.state = rescueNeedsCleanup
		r.mu.Unlock()
		if r.ticker != nil {
			r.ticker.Stop()
		}
		r.emit(RescueEvent{Kind: RescueEventCleanupRequired})
		r.cancel()
		close(r.done)
		return
	}
	r.mu.Lock()
	r.approved = nil
	r.client = nil
	r.prompt = RescuePrompt{}
	r.mu.Unlock()
	r.retryReceptionLocked()
}

func (r *Rescue) retryReceptionLocked() {
	r.mu.Lock()
	if r.state != rescueReopening || r.reopenRunning {
		r.mu.Unlock()
		return
	}
	wall, mono := r.clock.wall(), r.clock.mono()
	if r.stopRequested || r.expired(wall, mono) {
		r.mu.Unlock()
		r.end(false, false, false)
		return
	}
	previousKey := append([]byte(nil), r.lastHostKey...)
	r.reopenRunning = true
	ctx, cancel := context.WithCancel(r.life)
	r.reopenSequence++
	sequence := r.reopenSequence
	r.reopenActive = sequence
	r.reopenCancel = cancel
	r.mu.Unlock()
	r.openReceptionLocked(previousKey, ctx, cancel, sequence)
}

func (r *Rescue) openReceptionLocked(previousKey []byte, ctx context.Context, cancel context.CancelFunc, sequence uint64) {
	go func() {
		reception, code, hostKey, candidate, err := r.openReceptionCandidate(ctx, previousKey, func(candidate *rescueReceptionCandidate) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.state != rescueReopening || r.reopenActive != sequence || r.stopRequested {
				return false
			}
			r.pendingReception = candidate
			return true
		})
		r.step.Lock()
		defer r.step.Unlock()
		r.mu.Lock()
		if r.state != rescueReopening || r.reopenActive != sequence {
			if r.pendingReception == candidate {
				r.pendingReception = nil
			}
			r.mu.Unlock()
			cancel()
			candidate.close()
			return
		}
		wall, mono := r.clock.wall(), r.clock.mono()
		if r.stopRequested || r.expired(wall, mono) {
			r.reopenRunning = false
			r.reopenCancel = nil
			r.reopenActive = 0
			if r.pendingReception == candidate {
				r.pendingReception = nil
			}
			r.mu.Unlock()
			cancel()
			candidate.close()
			r.end(false, false, false)
			return
		}
		if err != nil {
			r.reopenRunning = false
			r.reopenCancel = nil
			r.reopenActive = 0
			if r.pendingReception == candidate {
				r.pendingReception = nil
			}
			r.mu.Unlock()
			cancel()
			candidate.close()
			return
		}
		r.reception, r.lastHostKey, r.state = reception, hostKey, rescueAccepting
		r.pendingReception = nil
		r.reopenRunning = false
		r.reopenCancel = nil
		r.reopenActive = 0
		r.mu.Unlock()
		cancel()
		r.signal()
		r.emit(RescueEvent{Kind: RescueEventReceptionOpened, Code: code})
	}()
}
