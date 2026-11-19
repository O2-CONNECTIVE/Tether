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

// The reception the product uses is the one-reception client.
var _ RescueReception = (*agenttransport.RescueClient)(nil)

// RescueConfig is supplied at 受付 ON.
type RescueConfig struct {
	// HostKey is K, the channel host key. It must sign with the reception's own
	// K, so that Reception.Close discarding K also disables it.
	HostKey        ssh.Signer
	Runtime        macagent.Config
	Duration       time.Duration
	CleanupTimeout time.Duration
	Budgets        agentchannel.Budgets
	Helper         RescueHelper
	Reception      RescueReception
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
	rescueEnded
)

// Rescue owns one reception from 受付中 to 終了. It never restarts the reception.
type Rescue struct {
	// step serializes the transitions that call the helper or the reception
	// (Show, the grant's issuance and hand-over) with the end, so the end
	// withdraws only a shown prompt and revokes every issued grant before Done.
	step            sync.Mutex
	mu              sync.Mutex
	state           rescueState
	cfg             RescueConfig
	runtime         agentexec.ProfiledRuntime
	helper          RescueHelper
	reception       RescueReception
	clock           rescueClock
	prompt          RescuePrompt
	client          ssh.PublicKey
	shownWall       time.Time
	shownMono       time.Duration
	approvedWall    time.Time
	approvedMono    time.Duration
	approved        *Approved
	cleanupErr      error
	cleanupObserved bool
	life            context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	stop            chan struct{}
	stopOnce        sync.Once
	ticker          *time.Ticker
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
	if cfg.HostKey == nil || cfg.Helper == nil || cfg.Reception == nil || runtime == nil || runtime.AgentProfile() != agentexec.ProfileMacCurrentUser || cfg.CleanupTimeout <= 0 || cfg.CleanupTimeout > time.Minute {
		return nil, ErrConfig
	}
	if _, err := agentchannel.CanonicalKeys([]ssh.PublicKey{cfg.HostKey.PublicKey()}); err != nil {
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
	r := &Rescue{cfg: cfg, runtime: runtime, helper: cfg.Helper, reception: cfg.Reception, clock: clock, life: life, cancel: cancel, done: make(chan struct{}), stop: make(chan struct{}), ticker: ticker}
	go r.loop()
	return r, nil
}

// Receive moves 受付中 to 確認中 and returns after Show. Any other state, or a
// host-side end or lost reception not handled yet, refuses without a prompt.
// The 60 seconds count from before Show.
func (r *Rescue) Receive(req RescueRequest) error {
	if req.client == nil {
		return ErrDenied
	}
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
		if !bytes.Equal(v.HostKey(), r.cfg.HostKey.PublicKey().Marshal()) {
			return ErrDenied
		}
		return r.Receive(RescueRequest{client: v.Client(), displayName: v.DisplayName(), organization: v.Organization(), profile: v.Profile()})
	case <-r.done:
		return ErrDenied
	}
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
	r.mu.Unlock()
	go func() { _ = a.Serve(r.life, conn); r.finish(false, false, false) }()
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

// cutOff reports a host-side end or a lost reception, even before the loop
// handles it.
func (r *Rescue) cutOff() bool {
	select {
	case <-r.stop:
		return true
	case <-r.reception.Done():
		return true
	default:
		return false
	}
}

// issue grants C alone, with K as the host key, until the time of approval
// plus the chosen lifetime, through the same issuance as the terminal path,
// and hands its dispatch to the reception.
func (r *Rescue) issue(approvedAt time.Time, client ssh.PublicKey) (*Approved, net.Conn, error) {
	p, err := prepare(Config{HostKey: r.cfg.HostKey, AllowedKeys: []ssh.PublicKey{client}, Runtime: r.cfg.Runtime, Deadline: approvedAt.Add(r.cfg.Duration), CleanupTimeout: r.cfg.CleanupTimeout, Budgets: r.cfg.Budgets}, r.runtime)
	if err != nil {
		return nil, nil, err
	}
	a, err := p.completeApproved(r.life)
	if err == nil {
		var conn net.Conn
		if conn, err = r.reception.Allow(a.Dispatch()); err == nil {
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
func (r *Rescue) Stop() { r.stopOnce.Do(func() { close(r.stop) }) }

// Done closes after the end: the grant revoked, its cleanup run within
// CleanupTimeout, K discarded.
func (r *Rescue) Done() <-chan struct{} { return r.done }

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
		select {
		case <-r.stop:
			r.finish(false, false, true)
			return
		case <-r.reception.Done():
			r.finish(false, false, true)
			return
		case <-r.clock.tick:
			if r.recheck() {
				return
			}
		case <-r.life.Done():
			return
		}
	}
}

// recheck ends a prompt past 60 seconds or a session past its lifetime. It
// decides under step, so no answer or issuance interleaves with the decision.
func (r *Rescue) recheck() bool {
	r.step.Lock()
	defer r.step.Unlock()
	r.mu.Lock()
	wall, mono := r.clock.wall(), r.clock.mono()
	timedOut := r.state == rescueConfirming && r.late(wall, mono)
	expired := r.state == rescueSession && r.expired(wall, mono)
	r.mu.Unlock()
	if timedOut || expired {
		r.end(timedOut, timedOut, timedOut)
	}
	return timedOut || expired
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
	if r.state == rescueEnded {
		r.mu.Unlock()
		return
	}
	confirming, id, a := r.state == rescueConfirming, r.prompt.RequestID, r.approved
	r.state = rescueEnded
	r.mu.Unlock()
	if r.ticker != nil {
		r.ticker.Stop()
	}
	if confirming && withdraw {
		r.helper.Withdraw(id)
	}
	if refuse {
		_ = r.reception.Refuse(timedOut)
	}
	_ = r.reception.Close()
	if a != nil {
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CleanupTimeout)
		cleanupErr := a.Close(ctx)
		cancel()
		_, observed := a.CleanupResult()
		r.mu.Lock()
		r.cleanupErr, r.cleanupObserved = cleanupErr, observed
		r.mu.Unlock()
	}
	r.cancel()
	close(r.done)
}
