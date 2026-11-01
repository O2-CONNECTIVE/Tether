// rescue-mac-agent is the recipient's foreground entry for one temporary Mac
// current-user agent session. It reads one local composition document named by
// "-config <path>" through agentkeys.ReadTrustConfig, loads the enrolled host
// key with agentkeys.LoadCurrentUser, prepares maclaunch, asks for approval on
// /dev/tty only (maclaunch.Pending.ApproveLocal), and then registers the one
// approved owner with the gateway over agenttransport and serves it until
// interrupt, channel loss, expiry or revocation. It never reconnects, resumes,
// installs or elevates; see docs/agent-cli.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

const readBudget = 10 * time.Second // one protected document or key read

// approval is a prepared, not yet approved proposal. Its only way forward is
// the local terminal confirmation.
type approval interface {
	ApproveLocal(context.Context) (agenttransport.Owner, error)
}

// session is one single-use registration of the approved owner.
type session interface {
	Run(context.Context) error
	Close(context.Context) error
}

// deps are the native operations. Production always uses nativeDeps; tests
// substitute fakes so that no file, key, terminal, process or network is used.
type deps struct {
	readTrust   func(context.Context, string) ([]byte, error)
	loadKey     func(context.Context, agentkeys.Config) (ssh.Signer, error)
	prepare     func(maclaunch.Config) (approval, error)
	newClient   func(agenttransport.ClientConfig, agenttransport.Owner) (session, error)
	clock       func() time.Time
	statusSink  func() (statusSink, error)
	statusClock func() (uint64, bool)
}

func nativeDeps() deps {
	return deps{
		readTrust: agentkeys.ReadTrustConfig,
		loadKey:   agentkeys.LoadCurrentUser,
		prepare: func(cfg maclaunch.Config) (approval, error) {
			p, err := maclaunch.Prepare(cfg)
			if err != nil {
				return nil, err
			}
			return terminalApproval{p}, nil
		},
		newClient: func(cfg agenttransport.ClientConfig, owner agenttransport.Owner) (session, error) {
			c, err := agenttransport.NewClient(cfg, owner)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		clock:       time.Now,
		statusSink:  openStatusSink,
		statusClock: statusContinuousMillis,
	}
}

// terminalApproval is exactly maclaunch's /dev/tty confirmation; it adds no
// other approval path and never returns a typed nil owner.
type terminalApproval struct{ pending *maclaunch.Pending }

func (t terminalApproval) ApproveLocal(ctx context.Context) (agenttransport.Owner, error) {
	approved, err := t.pending.ApproveLocal(ctx)
	if approved == nil {
		return nil, err
	}
	return approved, err
}

func main() {
	// Interrupt, termination and terminal hangup all end the foreground
	// session through the same revoking cleanup.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	os.Exit(execute(ctx, os.Args[1:], os.Stderr, nativeDeps()))
}

// This opt-in pilot narrows admission at the host, not merely at the client.
// The protected config still supplies existing identity/paths, never command
// bytes. This is not a new shell, sandbox, grant or noninteractive consent.
const fixedPilotCommand = "/usr/bin/printf 'TETHER_MCP_GUI_BEGIN\\n'; /bin/sleep 5; /usr/bin/printf 'TETHER_MCP_GUI_END\\n'"

// execute validates the whole document and loads the host key before
// preparing anything. Only the local terminal approval creates a grant; the
// same signer then proves the host to the gateway. Every exit after approval
// closes only that approved owner. Diagnostics are fixed messages; no path,
// label, fingerprint, endpoint or key is printed.
func execute(ctx context.Context, args []string, diagnostic io.Writer, d deps) int {
	if ctx == nil || d.readTrust == nil || d.loadKey == nil || d.prepare == nil || d.newClient == nil || d.clock == nil ||
		(len(args) != 2 && (len(args) != 3 || args[2] != "-status-stdout")) || (args[0] != "-config" && args[0] != "-fixed-command-pilot-config") || args[1] == "" {
		report(diagnostic, "使い方: rescue-mac-agent -config <path>")
		return 2
	}
	var status *statusFeed
	if len(args) == 3 {
		if d.statusSink == nil || d.statusClock == nil {
			report(diagnostic, "状態表示用の専用pipeがありません")
			return 2
		}
		sink, err := d.statusSink()
		if err != nil || sink == nil {
			report(diagnostic, "状態表示用の専用pipeを開けません")
			return 2
		}
		status = newStatusFeedClock(sink, d.statusClock)
		defer status.end()
	}
	read, cancel := context.WithTimeout(ctx, readBudget)
	raw, err := d.readTrust(read, args[1])
	cancel()
	if err != nil {
		wipe(raw)
		status.failSetup(setupConfig, "not_started")
		report(diagnostic, "設定を読めませんでした")
		return 2
	}
	cfg, err := parseLocalConfig(raw, args[1])
	wipe(raw)
	if err != nil {
		status.failSetup(setupConfig, "not_started")
		report(diagnostic, "設定が不正または不完全です")
		return 2
	}
	status.update("loading_key", "not_started", false)
	load, cancel := context.WithTimeout(ctx, readBudget)
	signer, err := d.loadKey(load, cfg.hostKey)
	cancel()
	if err != nil || signer == nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != cfg.hostKey.Fingerprint {
		status.failSetup(setupKey, "not_started")
		report(diagnostic, "host鍵を取得できませんでした。承認を求めません")
		return 2
	}
	status.update("preparing", "not_started", false)
	var fixedStart *agentexec.FixedStartConstraint
	if args[0] == "-fixed-command-pilot-config" {
		fixedStart = &agentexec.FixedStartConstraint{RequestID: "tether-fixed-command-v1", Command: fixedPilotCommand, TimeoutMillis: 15000}
	}
	startDiagnostics := &macagent.StartDiagnostics{}
	cleanupDiagnostics := &macagent.CleanupDiagnostics{}
	terminalDisposition := &macagent.TerminalDisposition{}
	defer func() { reportCleanupFailures(diagnostic, cleanupDiagnostics) }()
	defer func() { reportStartFailure(diagnostic, startDiagnostics.Failure()) }()
	pending, err := d.prepare(maclaunch.Config{
		FixedStart: fixedStart, HostKey: signer, AllowedKeys: []ssh.PublicKey{cfg.allowed},
		Runtime:  macagent.Config{WorkDir: cfg.workDir, Home: cfg.home, StartDiagnostics: startDiagnostics, CleanupDiagnostics: cleanupDiagnostics, TerminalDisposition: terminalDisposition},
		Deadline: d.clock().Add(cfg.session), CleanupTimeout: cfg.cleanup, Budgets: agentgateway.ChannelBudgets(),
	})
	if err != nil || pending == nil {
		status.failSetup(setupPreparation, "not_started")
		report(diagnostic, "ローカル実行環境を準備できませんでした。承認を求めません")
		return 2
	}
	// ctx is the session lifetime: its end after approval revokes the grant.
	status.update("awaiting_approval", "not_started", false)
	owner, err := pending.ApproveLocal(ctx)
	if err != nil || owner == nil {
		approvalCleanupUnknown := setupCleanupUnknown(err)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			report(diagnostic, "入力待ちの期限が切れました。未承認のまま終了し、接続しません")
		case errors.Is(err, context.Canceled):
			report(diagnostic, "承認操作を中止しました。接続しません")
		case errors.Is(err, io.EOF):
			report(diagnostic, "承認入力が終了しました。接続しません")
		default:
			report(diagnostic, "承認は成立しませんでした。接続を開始しません")
		}
		if owner != nil {
			status.attach(owner)
			status.update("stopping", "pending", false)
			closeErr := closeApproved(owner, cfg.cleanup)
			err = errors.Join(err, closeErr)
			if closeErr != nil {
				terminalUnknown(owner, nil, terminalDisposition, cfg.cleanup, diagnostic, status)
			} else if approvalCleanupUnknown {
				status.unknown()
			} else {
				status.failSetup(setupApproval, "confirmed")
			}
		}
		if owner == nil {
			if approvalCleanupUnknown {
				status.unknown()
			} else {
				status.failSetup(setupApproval, "not_started")
			}
		}
		if setupCleanupUnknown(err) {
			report(diagnostic, "承認処理後の回収を確認できませんでした")
		}
		return 1
	}
	status.attach(owner)
	client, err := d.newClient(agenttransport.ClientConfig{
		Endpoint: cfg.endpoint, HostLabel: cfg.host, PrincipalLabel: cfg.principal, HostKey: signer, Budgets: agenttransport.DefaultBudgets(),
	}, owner)
	if err != nil || client == nil {
		report(diagnostic, "登録を構成できませんでした。承認を失効させます")
		status.update("stopping", "pending", false)
		if err := closeApproved(owner, cfg.cleanup); err != nil {
			report(diagnostic, "承認済みsessionの回収を確認できませんでした")
			terminalUnknown(owner, client, terminalDisposition, cfg.cleanup, diagnostic, status)
		} else {
			status.failSetup(setupConnection, "confirmed")
		}
		return 1
	}
	return finish(ctx, client, owner, terminalDisposition, cfg.cleanup, diagnostic, status)
}

// Classification only: retain uncertainty from any owned approval layer before
// the first terminal display frame. It does not change cleanup control or errors.
func setupCleanupUnknown(err error) bool {
	return errors.Is(err, agentexec.ErrCleanupUnknown) || errors.Is(err, agenttransport.ErrCleanupUnknown) || errors.Is(err, agentchannel.ErrCleanupUnknown)
}

// closeApproved makes at most two finite attempts on the same partially
// acquired owner. No replacement owner or authority is created.
func closeApproved(owner agenttransport.Owner, budget time.Duration) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		cleanup, stop := context.WithTimeout(context.Background(), budget)
		err = owner.Close(cleanup)
		stop()
		if err == nil {
			return nil
		}
	}
	return err
}

// finish always confirms the same client after Run, irrespective of the
// error layer. Command, channel and issuer cleanup errors are not equivalent
// sentinels. Close retries only unconfirmed ownership; one bounded attempt is
// made here, and any remaining error prevents a successful exit.
func finish(ctx context.Context, client session, owner agenttransport.Owner, terminal *macagent.TerminalDisposition, budget time.Duration, diagnostic io.Writer, feeds ...*statusFeed) int {
	var status *statusFeed
	if len(feeds) > 0 {
		status = feeds[0]
	}
	_ = client.Run(ctx)
	status.update("stopping", "pending", false)
	cleanup, stop := context.WithTimeout(context.Background(), budget)
	err := client.Close(cleanup)
	stop()
	if err != nil {
		report(diagnostic, "承認済みsessionの回収を確認できませんでした")
		terminalUnknown(owner, client, terminal, budget, diagnostic, status)
		return 1
	}
	status.update("closed", "confirmed", true)
	report(diagnostic, "接続を終了しました。承認は失効し、再開しません")
	if ctx.Err() != nil {
		return 0
	}
	return 1
}

func report(w io.Writer, message string) {
	if w != nil {
		fmt.Fprintln(w, message)
	}
}

// reportStartFailure emits only a closed vocabulary after owned cleanup, never
// an operating-system error, requested command, path, key or terminal input.
func reportStartFailure(w io.Writer, code macagent.StartFailure) {
	if code != macagent.StartFailureNone {
		report(w, "NATIVE_START_REFUSED category="+code.String())
	}
}

func reportCleanupFailures(w io.Writer, d *macagent.CleanupDiagnostics) {
	for _, code := range d.Failures() {
		report(w, "NATIVE_CLEANUP_FAILURE category="+code)
	}
}

// terminalUnknown is a local foreground disposal, not successful cleanup.
// Missing sealing capabilities prohibit even the optional one-shot child reap.
func terminalUnknown(owner agenttransport.Owner, client session, terminal *macagent.TerminalDisposition, budget time.Duration, diagnostic io.Writer, feeds ...*statusFeed) {
	var status *statusFeed
	if len(feeds) > 0 {
		status = feeds[0]
	}
	sealed := false
	if revoker, ok := owner.(interface{ RevokeTerminalUnknown() error }); ok {
		sealed = revoker.RevokeTerminalUnknown() == nil
	}
	transportStopped := client == nil
	if stopper, ok := client.(interface{ StopTerminalUnknown(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		transportStopped = stopper.StopTerminalUnknown(ctx) == nil
		cancel()
	}
	_ = terminal.RelinquishUnknown(sealed && transportStopped)
	status.unknown()
	report(diagnostic, "TERMINAL_CLEANUP_UNKNOWN")
	report(diagnostic, "回収未確認のまま所有を終了します。関連プロセスが残っている可能性があります。成功扱いせず、この承認は再利用しません")
}
