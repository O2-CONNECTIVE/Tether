package maclaunch

import (
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
// authenticates that connection; a WebView IPC handler never holds it.
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
	// Close ends the reception and discards K.
	Close() error
}

// RescueConfig is supplied at 受付 ON.
type RescueConfig struct {
	HostKey        ssh.Signer // K, the channel host key
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
	mu           sync.Mutex
	state        rescueState
	cfg          RescueConfig
	runtime      agentexec.ProfiledRuntime
	helper       RescueHelper
	reception    RescueReception
	clock        rescueClock
	prompt       RescuePrompt
	client       ssh.PublicKey
	shownWall    time.Time
	shownMono    time.Duration
	promptReady  bool
	approvedWall time.Time
	approvedMono time.Duration
	approved     *Approved
	life         context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	stop         chan struct{}
	stopOnce     sync.Once
	ticker       *time.Ticker
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

// Receive moves 受付中 to 確認中 and returns after Show. Any other state
// refuses without a prompt.
func (r *Rescue) Receive(req RescueRequest) error {
	r.mu.Lock()
	if r.state != rescueAccepting || req.client == nil {
		r.mu.Unlock()
		return ErrDenied
	}
	r.state = rescueConfirming
	r.mu.Unlock()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		r.finish(false, false, false, nil)
		return errors.Join(ErrDenied, err)
	}
	p := RescuePrompt{RequestID: hex.EncodeToString(id[:]), Organization: req.organization, DisplayName: req.displayName, Profile: req.profile, Duration: r.cfg.Duration, ClientFingerprint: ssh.FingerprintSHA256(req.client), Warning: RescueWarning}
	if err := r.helper.Show(p); err != nil {
		r.finish(false, false, false, nil)
		return errors.Join(ErrDenied, err)
	}
	w, m := r.clock.wall(), r.clock.mono()
	r.mu.Lock()
	if r.state == rescueConfirming {
		r.prompt = p
		r.client = req.client
		r.shownWall = w
		r.shownMono = m
		r.promptReady = true
	}
	ended := r.state != rescueConfirming
	r.mu.Unlock()
	if ended {
		return ErrDenied
	}
	return nil
}

// Answer accepts reply only from the helper this Rescue was built with, for the
// current prompt, within RescuePromptTimeout. Allow issues the grant.
func (r *Rescue) Answer(from RescueHelper, reply RescueReply) error {
	r.mu.Lock()
	if r.state != rescueConfirming || from == nil || from != r.helper || reply.RequestID != r.prompt.RequestID || reply.Digest != r.prompt.Digest() {
		r.mu.Unlock()
		return ErrDenied
	}
	wall, mono := r.clock.wall(), r.clock.mono()
	if mono-r.shownMono >= RescuePromptTimeout || wall.Sub(r.shownWall) >= RescuePromptTimeout || wall.Before(r.shownWall) {
		r.mu.Unlock()
		r.finish(true, true, true, nil)
		return ErrDenied
	}
	if !reply.Allow {
		r.mu.Unlock()
		r.finish(false, true, false, nil)
		return nil
	}
	r.state = rescueIssuing
	r.approvedWall = wall
	r.approvedMono = mono
	r.mu.Unlock()
	p, err := prepare(Config{HostKey: r.cfg.HostKey, AllowedKeys: []ssh.PublicKey{r.client}, Runtime: r.cfg.Runtime, Deadline: wall.Add(r.cfg.Duration), CleanupTimeout: r.cfg.CleanupTimeout, Budgets: r.cfg.Budgets}, r.runtime)
	var a *Approved
	if err == nil {
		a, err = p.completeApproved(r.life)
	}
	if err != nil {
		if a != nil {
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CleanupTimeout)
			_ = a.Close(ctx)
			cancel()
		}
		if p != nil {
			_ = p.issuer.Close()
		}
		r.finish(false, false, false, nil)
		return err
	}
	conn, allowErr := r.reception.Allow(a.Dispatch())
	if allowErr != nil {
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CleanupTimeout)
		_ = a.Close(ctx)
		cancel()
		_ = p.issuer.Close()
		r.finish(false, false, false, nil)
		return allowErr
	}
	r.mu.Lock()
	r.approved = a
	if r.state == rescueIssuing {
		r.state = rescueSession
	}
	ended := r.state == rescueEnded
	r.mu.Unlock()
	if ended {
		_ = conn.Close()
		return nil
	}
	go func() { _ = a.Serve(r.life, conn); r.finish(false, false, false, a) }()
	return nil
}

// Stop is a host-side end: the stop operation, 受付 OFF, app exit, logout or restart.
// It does not wait.
func (r *Rescue) Stop() { r.stopOnce.Do(func() { close(r.stop) }) }

// Done closes after the end: the grant revoked and reclaimed, K discarded.
func (r *Rescue) Done() <-chan struct{} { return r.done }

func (r *Rescue) loop() {
	for {
		select {
		case <-r.stop:
			r.finish(false, false, true, nil)
			return
		case <-r.reception.Done():
			r.finish(false, false, true, nil)
			return
		case <-r.clock.tick:
			r.mu.Lock()
			s := r.state
			wall, mono, sw, sm, aw, am, ready := r.clock.wall(), r.clock.mono(), r.shownWall, r.shownMono, r.approvedWall, r.approvedMono, r.promptReady
			r.mu.Unlock()
			if s == rescueConfirming && ready && (mono-sm >= RescuePromptTimeout || wall.Sub(sw) >= RescuePromptTimeout || wall.Before(sw)) {
				r.finish(true, true, true, nil)
				return
			}
			if s == rescueSession && (mono-am >= r.cfg.Duration || !wall.Before(aw.Add(r.cfg.Duration))) {
				r.finish(false, false, false, nil)
				return
			}
		case <-r.life.Done():
			return
		}
	}
}
func (r *Rescue) finish(timeout, refuse, withdraw bool, a *Approved) {
	r.mu.Lock()
	if r.state == rescueEnded {
		r.mu.Unlock()
		return
	}
	old := r.state
	prompt := r.prompt
	if r.approved != nil {
		a = r.approved
	}
	r.state = rescueEnded
	r.mu.Unlock()
	if r.ticker != nil {
		r.ticker.Stop()
	}
	if old == rescueConfirming && withdraw {
		r.helper.Withdraw(prompt.RequestID)
	}
	if refuse {
		_ = r.reception.Refuse(timeout)
	}
	if a != nil {
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CleanupTimeout)
		_ = a.Close(ctx)
		cancel()
	}
	r.cancel()
	_ = r.reception.Close()
	close(r.done)
}
