// Package maclaunch is the recipient-side, one-shot current-user approval
// owner. Only its local controlling terminal may approve: MCP, relay frames,
// stdin, environment variables and saved approval booleans are not inputs.
// It does not enroll keys, dial, listen, install or start a command on approval.
package maclaunch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

var (
	ErrConfig   = errors.New("maclaunch: invalid local proposal")
	ErrDenied   = errors.New("maclaunch: explicit local approval not obtained")
	ErrConsumed = errors.New("maclaunch: this proposal was already used")
)

// Config is supplied by recipient-launched trusted composition. Keys must
// already be enrolled and HostKey already owned; this is not an enrollment API.
// Deadline is absolute, including time spent considering the prompt. Runtime
// paths are local selections, never command API fields or a sandbox promise.
type Config struct {
	HostKey        ssh.Signer
	AllowedKeys    []ssh.PublicKey
	Runtime        macagent.Config
	Deadline       time.Time
	CleanupTimeout time.Duration
	Budgets        agentchannel.Budgets
	FixedStart     *agentexec.FixedStartConstraint
}

// Pending has no granted Authority. It cannot be serialized or resumed. Any
// confirmation attempt consumes it, including rejection or terminal failure.
type Pending struct {
	mu            sync.Mutex
	used          bool
	issuer        *ephemeral.Issuer
	challenge     ephemeral.Challenge
	hostKey       ssh.Signer
	hostPublic    ssh.PublicKey
	keys          []ssh.PublicKey
	runtime       agentexec.ProfiledRuntime
	runtimeConfig macagent.Config
	deadline      time.Time
	cleanup       time.Duration
	budgets       agentchannel.Budgets
	phrase        string
	preparedAt    time.Time
	initialBudget time.Duration
	fixedStart    *agentexec.FixedStartConstraint
	clock         func() time.Time
}

// Prepare validates the local Mac runtime without starting a process. Actual
// approval is only ApproveLocal; Prepare itself creates no Authority or Session.
func Prepare(cfg Config) (*Pending, error) {
	if !localSupported() {
		return nil, ErrConfig
	}
	runtime, err := macagent.New(cfg.Runtime)
	if err != nil {
		return nil, errors.Join(ErrConfig, err)
	}
	return prepare(cfg, runtime)
}

func prepare(cfg Config, runtime agentexec.ProfiledRuntime) (*Pending, error) {
	now := time.Now()
	var fixed *agentexec.FixedStartConstraint
	if cfg.FixedStart != nil {
		copied := *cfg.FixedStart
		if copied.Validate() != nil {
			return nil, ErrConfig
		}
		fixed = &copied
	}
	if runtime == nil || runtime.AgentProfile() != agentexec.ProfileMacCurrentUser || cfg.HostKey == nil || !now.Add(time.Second).Before(cfg.Deadline) || cfg.Deadline.Sub(now) > agentchannel.MaxClientLifetime || cfg.CleanupTimeout <= 0 || cfg.CleanupTimeout > time.Minute {
		return nil, ErrConfig
	}
	for _, p := range []string{cfg.Runtime.WorkDir, cfg.Runtime.Home} {
		if p == "/" || !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\x00\r\n") {
			return nil, ErrConfig
		}
	}
	// Reject bad channel timing before asking for consent. NewHost validates it
	// again; this is a local presentation check, not a replacement for that check.
	b := cfg.Budgets
	if b.Handshake <= 0 || b.Handshake > agentchannel.MaxHandshake || b.HeartbeatInterval < agentchannel.MinHeartbeatInterval || b.HeartbeatInterval > agentchannel.MaxHeartbeatInterval || b.HeartbeatTimeout <= 0 || b.HeartbeatTimeout > agentchannel.MaxHeartbeatTimeout || b.ReadStall <= b.HeartbeatInterval+b.HeartbeatTimeout || b.ReadStall > agentchannel.MaxReadStall || b.WriteStall <= 0 || b.WriteStall > agentchannel.MaxWriteStall || b.Cleanup <= 0 || b.Cleanup > agentchannel.MaxCleanup {
		return nil, ErrConfig
	}
	keys, err := agentchannel.CanonicalKeys(cfg.AllowedKeys)
	if err != nil {
		return nil, ErrConfig
	}
	host, err := agentchannel.CanonicalKeys([]ssh.PublicKey{cfg.HostKey.PublicKey()})
	if err != nil {
		return nil, ErrConfig
	}
	digest, err := runbackend.KeySetDigest(keys)
	if err != nil {
		return nil, ErrConfig
	}
	issuer, err := ephemeral.NewCurrentUser()
	if err != nil {
		return nil, err
	}
	challenge, err := issuer.Prepare(ephemeral.ApprovedPolicy{PolicyDigest: agentexec.MacCurrentUserPolicyDigest(), KeySetDigest: digest, GuestBoundary: supervisor.CurrentUserNoIsolation, HardDeadline: cfg.Deadline, BackendLeaseCeiling: time.Second})
	if err != nil {
		_ = issuer.Close()
		return nil, err
	}
	sum := sha256.Sum256([]byte(challenge.Binding().Identity.LaunchID))
	return &Pending{fixedStart: fixed, issuer: issuer, challenge: challenge, hostKey: cfg.HostKey, hostPublic: host[0], keys: keys, runtime: runtime, runtimeConfig: cfg.Runtime, deadline: cfg.Deadline, cleanup: cfg.CleanupTimeout, budgets: b, phrase: fmt.Sprintf("approve %x", sum[:8]), clock: time.Now, preparedAt: now, initialBudget: cfg.Deadline.Sub(now)}, nil
}

func (p *Pending) take() error {
	if p == nil {
		return ErrConfig
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used {
		return ErrConsumed
	}
	p.used = true
	return nil
}

// ApproveLocal opens only /dev/tty on Darwin, not stdin or a caller-provided
// stream. There is no noninteractive yes flag. The local user must type the
// fresh proposal phrase after seeing the fixed details. The owned tty must
// support bounded polling; cancellation and expiry refuse, never approve.
// No Authority exists while waiting for input.
func (p *Pending) ApproveLocal(ctx context.Context) (*Approved, error) {
	if err := p.take(); err != nil {
		return nil, err
	}
	if ctx == nil || ctx.Err() != nil {
		_ = p.issuer.Close()
		return nil, ErrDenied
	}
	remaining := p.initialBudget - time.Since(p.preparedAt)
	if remaining <= 0 {
		_ = p.issuer.Close()
		return nil, ErrDenied
	}
	promptCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	tty, err := openTerminal(promptCtx, p.deadline)
	if err != nil {
		_ = p.issuer.Close()
		return nil, errors.Join(ErrDenied, err)
	}
	defer tty.Close()
	return p.confirm(ctx, tty)
}

// approve is the package-private fake-terminal test boundary, not a public
// mechanism for another transport to supply a confirmation.
func (p *Pending) approve(ctx context.Context, tty io.ReadWriter) (*Approved, error) {
	if err := p.take(); err != nil {
		return nil, err
	}
	return p.confirm(ctx, tty)
}

func (p *Pending) confirm(ctx context.Context, tty io.ReadWriter) (approved *Approved, err error) {
	defer func() {
		if approved == nil {
			_ = p.issuer.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || tty == nil {
		return nil, ErrDenied
	}
	remaining := min(p.deadline.Sub(p.clock().Round(0)), p.initialBudget-time.Since(p.preparedAt))
	seconds := max(int64(0), int64((remaining-time.Second)/time.Second))
	var text strings.Builder
	fmt.Fprintf(&text, "\nTether | この Mac の一時操作を承認\n【入力待ち】確認文字列を入力して Enter（表示時点で残り %d 秒）\n\n", seconds)
	fmt.Fprintln(&text, "承認すると、下の Client 鍵の保持者が現在のユーザーとして任意コマンドを実行できます。昇格なし。")
	if p.fixedStart != nil {
		fmt.Fprintln(&text, "固定コマンド試験: この作業承認ではローカルで固定した request ID・command・timeout の1件だけを受け付けます。")
		fmt.Fprintln(&text, "これはAPI受付の制約であり、実行ユーザーの権限縮小やsandboxではありません。")
	}
	fmt.Fprintln(&text, "NO ISOLATION（隔離なし）: ファイル・鍵・ネットワーク等（current user's resources）にアクセス可能。")
	fmt.Fprintln(&text, "作業フォルダも隔離領域ではありません。期限・通信断で失効し、所有プロセス群と入出力を回収。")
	fmt.Fprintln(&text, "実行済みの変更は元に戻せず、切り離された子孫プロセスは回収されない場合があります。")
	fmt.Fprintf(&text, "作業フォルダ: %q\nHOME: %q\n失効時刻: %s（入力待ち時間を含む・延長なし）\n", p.runtimeConfig.WorkDir, p.runtimeConfig.Home, p.deadline.Format(time.RFC3339Nano))
	fmt.Fprintf(&text, "照合用の公開指紋（秘密鍵ではありません）\n  Host: %s\n", ssh.FingerprintSHA256(p.hostPublic))
	for _, k := range p.keys {
		fmt.Fprintf(&text, "  Client: %s\n", ssh.FingerprintSHA256(k))
	}
	fmt.Fprintf(&text, "\n確認文字列: %s\n承認する場合だけ上の文字列を入力し Enter。空の Enter / Ctrl+C は中止。\n入力して Enter > ", p.phrase)
	written, writeErr := io.WriteString(tty, text.String())
	if writeErr == nil && written != text.Len() {
		writeErr = io.ErrShortWrite
	}
	if err = writeErr; err != nil {
		return nil, errors.Join(ErrDenied, err)
	}
	line, readErr := bufio.NewReaderSize(tty, 128).ReadSlice('\n')
	observed := p.clock().Round(0)
	elapsed := time.Since(p.preparedAt)
	// Refuse a significant rollback even when wall time is still above Prepare.
	// The independently carried elapsed cap below prevents any extension,
	// including sub-millisecond drift allowed by this presentation sanity check.
	regressed := observed.Sub(p.preparedAt.Round(0))+time.Millisecond < elapsed
	if !observed.Add(time.Second).Before(p.deadline) || time.Since(p.preparedAt) >= p.initialBudget {
		return nil, errors.Join(ErrDenied, context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return nil, errors.Join(ErrDenied, ctx.Err())
	}
	if regressed || observed.Before(p.preparedAt.Round(0)) {
		return nil, ErrDenied
	}
	if readErr != nil {
		return nil, errors.Join(ErrDenied, readErr)
	}
	if strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r") != p.phrase {
		return nil, ErrDenied
	}
	// Fix the already displayed host public key independently of a mutable
	// caller signer. Every signature is checked against it by agentchannel.
	fixed := &pinnedSigner{signer: p.hostKey, public: p.hostPublic}
	authority, err := p.issuer.Launch(ctx, p.challenge, p.clock, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		return nil, errors.Join(ErrDenied, err)
	}
	session, err := agentexec.New(agentexec.Config{FixedStart: p.fixedStart, ElapsedDeadline: p.preparedAt.Add(p.initialBudget), Issuer: p.issuer, Authority: authority, Runtime: p.runtime, Clock: p.clock, CleanupTimeout: p.cleanup})
	if err != nil {
		return nil, err
	}
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: p.keys, HostKey: fixed, Budgets: p.budgets})
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), p.cleanup)
		defer cancel()
		return nil, errors.Join(err, session.Close(cleanup))
	}
	g := session.Grant()
	if ctx.Err() != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), p.cleanup)
		defer cancel()
		return nil, errors.Join(ErrDenied, host.Close(cleanup))
	}
	// Keep the caller's lifetime, not the temporary prompt deadline context.
	// Prompt return must not revoke a successfully approved session.
	go func() {
		select {
		case <-ctx.Done():
			_ = p.issuer.Revoke(g.Binding)
		case <-session.Done():
		}
	}()
	return &Approved{issuer: p.issuer, session: session, host: host, dispatch: Dispatch{Binding: agentchannel.Binding{Grant: g.Binding, KeySetDigest: g.KeySetDigest, Profile: session.Profile()}, Deadline: g.HardDeadline, HostKey: append([]byte(nil), p.hostPublic.Marshal()...)}}, nil
}

type pinnedSigner struct {
	signer ssh.Signer
	public ssh.PublicKey
}

func (p *pinnedSigner) PublicKey() ssh.PublicKey { return p.public }
func (p *pinnedSigner) Sign(random io.Reader, data []byte) (*ssh.Signature, error) {
	sig, err := p.signer.Sign(random, data)
	if err != nil || sig == nil || p.public.Verify(data, sig) != nil {
		return nil, ErrConfig
	}
	return sig, nil
}

// Dispatch is public routing/pinning metadata, NOT a bearer grant or approval.
// The client still needs the private key corresponding to an approved key.
// Its authentic delivery to that client is the dispatch transport's obligation.
type Dispatch struct {
	Binding  agentchannel.Binding
	Deadline time.Time
	HostKey  []byte
}

// Approved owns precisely this issuer, Session and Host. It does not export a
// way to mint additional grants or replace the runtime, keys or authority.
type Approved struct {
	issuer   *ephemeral.Issuer
	session  *agentexec.Session
	host     *agentlink.Host
	dispatch Dispatch
}

func (a *Approved) Dispatch() Dispatch {
	if a == nil {
		return Dispatch{}
	}
	d := a.dispatch
	d.HostKey = append([]byte(nil), d.HostKey...)
	return d
}

// Serve transfers the already supplied opaque connection to the same Host.
// No dial, listener, automatic retry or reconnect is performed.
func (a *Approved) Serve(ctx context.Context, conn net.Conn) error {
	if a == nil || a.host == nil {
		return ErrConfig
	}
	err := a.host.Serve(ctx, conn)
	if !errors.Is(err, agentchannel.ErrUsed) {
		_ = a.issuer.Close()
	}
	return err
}

// Close revokes before waiting for bounded command cleanup. Calling it again
// only retries this owner's unconfirmed cleanup, never a replacement owner.
func (a *Approved) Close(ctx context.Context) error {
	if a == nil {
		return ErrConfig
	}
	return errors.Join(a.issuer.Close(), a.host.Close(ctx))
}

// RevokeTerminalUnknown seals only the local issuer. It never retries command
// cleanup and is not a remote command. The caller must separately end transport.
func (a *Approved) RevokeTerminalUnknown() error {
	if a == nil || a.issuer == nil {
		return ErrConfig
	}
	return a.issuer.Close()
}

// LocalSummary exposes only local display state, never an authority or key.
func (a *Approved) LocalSummary() (agentexec.LocalSummary, bool, bool) {
	if a == nil || a.session == nil || a.host == nil {
		return agentexec.LocalSummary{}, false, false
	}
	v, ok := a.session.LocalSummary()
	if !ok {
		return v, false, false
	}
	connected, ok := a.host.LocallyAuthenticated()
	return v, connected && !v.Revoked, ok
}
