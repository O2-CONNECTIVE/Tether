package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

// execute is driven only with fakes: no document, key, terminal, process,
// listener or network is touched, and no real grant is issued.

var fixedNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fakeOwner struct {
	r        *recorder
	closeErr error
}

func (o *fakeOwner) Dispatch() maclaunch.Dispatch { return maclaunch.Dispatch{} }
func (o *fakeOwner) Serve(context.Context, net.Conn) error {
	return errors.New("fake owner is never served")
}
func (o *fakeOwner) Close(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		o.r.t.Error("unbounded owner close")
	}
	o.r.log("owner-close")
	return o.closeErr
}

type fakeApproval struct {
	r     *recorder
	owner agenttransport.Owner
	err   error
}

func (a *fakeApproval) ApproveLocal(ctx context.Context) (agenttransport.Owner, error) {
	a.r.log("approve")
	a.r.approveCtx = ctx
	return a.owner, a.err
}

type fakeSession struct {
	r        *recorder
	run      func() error
	closeErr error
}

func (s *fakeSession) Run(context.Context) error { s.r.log("run"); return s.run() }
func (s *fakeSession) Close(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		s.r.t.Error("unbounded client close")
	}
	s.r.log("client-close")
	return s.closeErr
}

type recorder struct {
	t          *testing.T
	mu         sync.Mutex
	calls      []string
	doc        []byte
	readErr    error
	signer     ssh.Signer
	loadErr    error
	prepareErr error
	approval   *fakeApproval
	owner      *fakeOwner
	clientErr  error
	session    *fakeSession
	prepared   maclaunch.Config
	client     agenttransport.ClientConfig
	clientFor  agenttransport.Owner
	approveCtx context.Context
}

func newRecorder(t *testing.T, doc []byte) *recorder {
	r := &recorder{t: t, doc: doc, signer: signerOf(t, 0x21)}
	r.owner = &fakeOwner{r: r}
	r.approval = &fakeApproval{r: r, owner: r.owner}
	r.session = &fakeSession{r: r, run: func() error { return errors.Join(agenttransport.ErrRefused, errors.New("channel lost")) }}
	return r
}

func (r *recorder) log(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recorder) trace() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, " ")
}

func (r *recorder) deps() deps {
	return deps{
		readTrust: func(ctx context.Context, path string) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok || path != configPath {
				r.t.Error("unbounded or unexpected document read")
			}
			r.log("read")
			return r.doc, r.readErr
		},
		loadKey: func(ctx context.Context, cfg agentkeys.Config) (ssh.Signer, error) {
			if _, ok := ctx.Deadline(); !ok || cfg.Path != "/Users/synthetic/rescue/keys/mac-1" || cfg.Fingerprint != fingerprint(r.t, 0x21) {
				r.t.Error("unbounded or unexpected key load")
			}
			r.log("load")
			if r.loadErr != nil {
				return nil, r.loadErr
			}
			return r.signer, nil
		},
		prepare: func(cfg maclaunch.Config) (approval, error) {
			r.log("prepare")
			r.prepared = cfg
			if r.prepareErr != nil {
				return nil, r.prepareErr
			}
			return r.approval, nil
		},
		newClient: func(cfg agenttransport.ClientConfig, owner agenttransport.Owner) (session, error) {
			r.log("client")
			r.client, r.clientFor = cfg, owner
			if r.clientErr != nil {
				return nil, r.clientErr
			}
			return r.session, nil
		},
		clock: func() time.Time { return fixedNow },
	}
}

func run(t *testing.T, ctx context.Context, r *recorder) (int, string) {
	t.Helper()
	var diagnostic bytes.Buffer
	code := execute(ctx, []string{"-config", configPath}, &diagnostic, r.deps())
	out := diagnostic.String()
	for _, secret := range []string{"synthetic", "member-", "mac-1", "SHA256:", "ssh-ed25519", "example.invalid", "/Users"} {
		if strings.Contains(out, secret) {
			t.Fatalf("diagnostic leaks %q: %q", secret, out)
		}
	}
	return code, out
}

func TestEntryRefusesUsageBeforeReading(t *testing.T) {
	for _, args := range [][]string{nil, {"-config"}, {"-config", ""}, {"-config", configPath, "-yes"}, {"-approve", configPath}} {
		r := newRecorder(t, encode(t, macDoc(t)))
		var diagnostic bytes.Buffer
		if code := execute(context.Background(), args, &diagnostic, r.deps()); code != 2 || r.trace() != "" {
			t.Fatalf("args %q: exit=%d trace=%q", args, code, r.trace())
		}
	}
}

func TestEntryStopsBeforeLaterSteps(t *testing.T) {
	invalid := macDoc(t)
	invalid["approved"] = true
	for _, tc := range []struct {
		name  string
		setup func(r *recorder)
		doc   []byte
		code  int
		want  string
	}{
		{"unreadable", func(r *recorder) { r.readErr = agentkeys.ErrDenied }, nil, 2, "read"},
		{"read cleanup unconfirmed", func(r *recorder) { r.readErr = agentkeys.ErrCleanup }, encode(t, macDoc(t)), 2, "read"},
		{"approval in document", func(*recorder) {}, encode(t, invalid), 2, "read"},
		{"host key refused", func(r *recorder) { r.loadErr = agentkeys.ErrDenied }, encode(t, macDoc(t)), 2, "read load"},
		{"loaded key is not the referenced one", func(r *recorder) { r.signer = signerOf(t, 0x11) }, encode(t, macDoc(t)), 2, "read load"},
		{"prepare refused", func(r *recorder) { r.prepareErr = maclaunch.ErrConfig }, encode(t, macDoc(t)), 2, "read load prepare"},
		// A refused or failed approval consumed the proposal inside maclaunch;
		// there is no owner, so nothing else is closed or connected.
		{"terminal approval refused", func(r *recorder) { r.approval.owner, r.approval.err = nil, maclaunch.ErrDenied }, encode(t, macDoc(t)), 1, "read load prepare approve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRecorder(t, tc.doc)
			tc.setup(r)
			if code, _ := run(t, context.Background(), r); code != tc.code || r.trace() != tc.want {
				t.Fatalf("exit=%d trace=%q", code, r.trace())
			}
			if !bytes.Equal(tc.doc, make([]byte, len(tc.doc))) {
				t.Fatal("document bytes were not wiped")
			}
		})
	}
}

func TestEntryFeedsTheSameSignerToApprovalAndTransport(t *testing.T) {
	r := newRecorder(t, encode(t, macDoc(t)))
	ctx := context.Background()
	if code, _ := run(t, ctx, r); code != 1 || r.trace() != "read load prepare approve client run client-close" {
		t.Fatalf("exit=%d trace=%q", code, r.trace())
	}
	p, c := r.prepared, r.client
	if p.HostKey != r.signer || c.HostKey != r.signer {
		t.Fatal("maclaunch and agenttransport did not receive the same loaded signer")
	}
	if len(p.AllowedKeys) != 1 || !bytes.Equal(p.AllowedKeys[0].Marshal(), publicOf(t, 0x11).Marshal()) {
		t.Fatal("approval does not pin exactly the configured principal key")
	}
	if p.Runtime.CleanupDiagnostics == nil || p.Runtime.StartDiagnostics == nil || p.Runtime.WorkDir != "/Users/synthetic/rescue-work" || p.Runtime.Home != "/Users/synthetic" || p.Runtime.TermGrace != 0 ||
		!p.Deadline.Equal(fixedNow.Add(time.Hour)) || p.CleanupTimeout != 10*time.Second || p.Budgets != agentgateway.ChannelBudgets() {
		t.Fatalf("prepared = %+v", p)
	}
	if c.Endpoint != "wss://gateway.example.invalid/agent-register" || c.HostLabel != "mac-1" || c.PrincipalLabel != "member-1" ||
		c.Budgets != agenttransport.DefaultBudgets() {
		t.Fatalf("client = %+v", c)
	}
	if r.clientFor != agenttransport.Owner(r.owner) || r.approveCtx != ctx {
		t.Fatal("transport did not receive the approved owner, or approval did not get the session lifetime")
	}
}

func TestEntryClosesOnlyTheApprovedOwnerWhenClientIsRefused(t *testing.T) {
	r := newRecorder(t, encode(t, macDoc(t)))
	r.clientErr = agenttransport.ErrConfig
	if code, _ := run(t, context.Background(), r); code != 1 || r.trace() != "read load prepare approve client owner-close" {
		t.Fatalf("exit=%d trace=%q", code, r.trace())
	}
	r = newRecorder(t, encode(t, macDoc(t)))
	r.clientErr, r.owner.closeErr = agenttransport.ErrConfig, agenttransport.ErrCleanupUnknown
	if code, out := run(t, context.Background(), r); code != 1 || r.trace() != "read load prepare approve client owner-close owner-close" || !strings.Contains(out, "確認できません") {
		t.Fatalf("exit=%d trace=%q out=%q", code, r.trace(), out)
	}
}

func TestEntryRetriesUnconfirmedCleanupOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		closeErr error
		want     string
	}{
		{"confirmed by retry", nil, "接続を終了しました"},
		{"still unconfirmed", errors.Join(agenttransport.ErrCleanupUnknown, context.DeadlineExceeded), "確認できません"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRecorder(t, encode(t, macDoc(t)))
			r.session.run = func() error { return errors.Join(agenttransport.ErrRefused, agenttransport.ErrCleanupUnknown) }
			r.session.closeErr = tc.closeErr
			code, out := run(t, context.Background(), r)
			if code != 1 || r.trace() != "read load prepare approve client run client-close" || !strings.Contains(out, tc.want) {
				t.Fatalf("exit=%d trace=%q out=%q", code, r.trace(), out)
			}
		})
	}
}

func TestEntryInterruptEndsCleanly(t *testing.T) {
	r := newRecorder(t, encode(t, macDoc(t)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.session.run = func() error { cancel(); return context.Canceled }
	if code, out := run(t, ctx, r); code != 0 || r.trace() != "read load prepare approve client run client-close" || !strings.Contains(out, "再開しません") {
		t.Fatalf("exit=%d trace=%q out=%q", code, r.trace(), out)
	}
}

func TestNativeDepsAreTheProtectedLoaders(t *testing.T) {
	d := nativeDeps()
	if d.prepare == nil || d.newClient == nil || d.clock == nil ||
		reflect.ValueOf(d.readTrust).Pointer() != reflect.ValueOf(agentkeys.ReadTrustConfig).Pointer() ||
		reflect.ValueOf(d.loadKey).Pointer() != reflect.ValueOf(agentkeys.LoadCurrentUser).Pointer() {
		t.Fatal("production composition is not the protected loader")
	}
}

func TestNativeNewClientNeverReturnsTypedNil(t *testing.T) {
	// agenttransport.NewClient refuses a nil owner without any I/O.
	if s, err := nativeDeps().newClient(agenttransport.ClientConfig{}, nil); err == nil || s != nil {
		t.Fatalf("session=%v err=%v", s, err)
	}
}

func TestCommandLayerCleanupUnknownNeverReportsSuccessfulInterrupt(t *testing.T) {
	for _, closeErr := range []error{agentexec.ErrCleanupUnknown, nil} {
		r := newRecorder(t, encode(t, macDoc(t)))
		ctx, cancel := context.WithCancel(context.Background())
		r.session.run = func() error { cancel(); return errors.Join(agenttransport.ErrClosed, agentexec.ErrCleanupUnknown) }
		r.session.closeErr = closeErr
		code, out := run(t, ctx, r)
		if r.trace() != "read load prepare approve client run client-close" {
			t.Fatalf("missing same-client confirmation: %s", r.trace())
		}
		if closeErr != nil && (code != 1 || !strings.Contains(out, "確認できません") || strings.Contains(out, "接続を終了しました")) {
			t.Fatalf("unknown cleanup reported clean: %d %s", code, out)
		}
		if closeErr == nil && code != 0 {
			t.Fatalf("confirmed interrupted cleanup: %d %s", code, out)
		}
		cancel()
	}
}
func TestArbitraryCleanupErrorStillGetsSameClientConfirmation(t *testing.T) {
	r := newRecorder(t, encode(t, macDoc(t)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.session.run = func() error { cancel(); return errors.New("synthetic non-sentinel cleanup failure") }
	r.session.closeErr = errors.New("still unknown")
	code, out := run(t, ctx, r)
	if code != 1 || !strings.Contains(out, "確認できません") || r.trace() != "read load prepare approve client run client-close" {
		t.Fatalf("unconfirmed %d %s %s", code, out, r.trace())
	}
}
func TestApprovalFailureDoesNotClaimGrantWasNeverIssued(t *testing.T) {
	r := newRecorder(t, encode(t, macDoc(t)))
	r.approval.owner = nil
	r.approval.err = errors.Join(maclaunch.ErrDenied, agentexec.ErrCleanupUnknown)
	code, out := run(t, context.Background(), r)
	if code != 1 || !strings.Contains(out, "確認できません") || strings.Contains(out, "発行していません") || r.trace() != "read load prepare approve" {
		t.Fatalf("approval failure %d %s %s", code, out, r.trace())
	}
}

func TestApprovalRefusalReasonsRemainFixedAndNeverConnect(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{context.DeadlineExceeded, "入力待ちの期限が切れました"}, {context.Canceled, "承認操作を中止しました"}, {io.EOF, "承認入力が終了しました"}} {
		t.Run(tc.want, func(t *testing.T) {
			r := newRecorder(t, encode(t, macDoc(t)))
			r.approval.owner = nil
			r.approval.err = errors.Join(maclaunch.ErrDenied, tc.err, errors.New("SYNTHETIC_SECRET"))
			code, out := run(t, context.Background(), r)
			if code != 1 || r.trace() != "read load prepare approve" || !strings.Contains(out, tc.want) || strings.Contains(out, "SYNTHETIC_SECRET") {
				t.Fatal(code, r.trace(), out)
			}
		})
	}
}

func TestStartRefusalDiagnosticUsesOnlyFixedVocabulary(t *testing.T) {
	for _, tc := range []struct {
		code macagent.StartFailure
		want string
	}{
		{macagent.StartFailureNone, ""},
		{macagent.StartFailureInheritedDescriptor, "NATIVE_START_REFUSED category=inherited_descriptor\n"},
		{macagent.StartFailureExecPermission, "NATIVE_START_REFUSED category=exec_permission\n"},
		{macagent.StartFailure(999), "NATIVE_START_REFUSED category=unknown\n"},
	} {
		var b bytes.Buffer
		reportStartFailure(&b, tc.code)
		if b.String() != tc.want {
			t.Fatalf("unexpected fixed diagnostic: %q", b.String())
		}
	}
}

func TestEmptyCleanupDiagnosticDoesNotClaimFailure(t *testing.T) {
	var b bytes.Buffer
	reportCleanupFailures(&b, nil)
	reportCleanupFailures(&b, &macagent.CleanupDiagnostics{})
	if b.Len() != 0 {
		t.Fatal("empty history emitted diagnostics")
	}
}

func TestFixedCommandPilotCallerWiringAndGeneralUnchanged(t *testing.T) {
	for _, mode := range []string{"-config", "-fixed-command-pilot-config"} {
		t.Run(mode, func(t *testing.T) {
			r := newRecorder(t, encode(t, macDoc(t)))
			var diagnostic bytes.Buffer
			code := execute(context.Background(), []string{mode, configPath}, &diagnostic, r.deps())
			if code != 1 || r.trace() != "read load prepare approve client run client-close" {
				t.Fatal(code, r.trace())
			}
			if mode == "-config" {
				if r.prepared.FixedStart != nil {
					t.Fatal("general mode narrowed")
				}
				return
			}
			f := r.prepared.FixedStart
			want := "/usr/bin/printf 'TETHER_MCP_GUI_BEGIN\\n'; /bin/sleep 5; /usr/bin/printf 'TETHER_MCP_GUI_END\\n'"
			if f == nil || f.RequestID != "tether-fixed-command-v1" || f.Command != want || f.TimeoutMillis != 15000 {
				t.Fatal("pilot not wired")
			}
		})
	}
}
func TestFixedCommandPilotRejectsUnknownOrOverrideArguments(t *testing.T) {
	for _, args := range [][]string{{"-fixed-command-pilot", "x"}, {"-fixed-command-pilot-config", configPath, "-command", "x"}, {"-config", configPath, "-fixed-command-pilot-config"}, {"-fixed-command-pilot-config", configPath, "-yes"}} {
		r := newRecorder(t, encode(t, macDoc(t)))
		var out bytes.Buffer
		if code := execute(context.Background(), args, &out, r.deps()); code != 2 || r.trace() != "" {
			t.Fatal(code, r.trace())
		}
	}
}

func TestFinishRecordsRunObservationWithoutChangingCleanup(t *testing.T) {
	r := newRecorder(t, nil)
	r.session.run = func() error { return agenttransport.ErrRefused }
	var diagnostic bytes.Buffer
	code := finish(context.Background(), r.session, r.owner, &macagent.TerminalDisposition{}, time.Second, &diagnostic)
	if code != 1 || r.trace() != "run client-close" {
		t.Fatal("finish control changed")
	}
	if !strings.Contains(diagnostic.String(), "NATIVE_RUN_OBSERVATION category=registration_refused\n") {
		t.Fatal("fixed Run observation missing")
	}
}

type panicRunError struct{}

func (panicRunError) Error() string { panic("Error must never be formatted") }

func TestRunObservationFixedSentinelsOnly(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"nil", nil, []string{"returned_nil"}},
		{"refused", agenttransport.ErrRefused, []string{"registration_refused"}},
		{"rejected", agentchannel.ErrRejected, []string{"channel_rejected"}},
		{"protocol", agentwire.ErrProtocol, []string{"wire_protocol"}},
		{"deadline", context.DeadlineExceeded, []string{"context_deadline"}},
		{"canceled", context.Canceled, []string{"context_canceled"}},
		{"peer", agentwire.ErrPeerLost, []string{"peer_end"}},
		{"transport_closed", agenttransport.ErrClosed, []string{"closed"}},
		{"channel_closed", agentchannel.ErrClosed, []string{"closed"}},
		{"wire_closed", agentwire.ErrClosed, []string{"closed"}},
		{"transport_config", agenttransport.ErrConfig, []string{"config"}},
		{"channel_config", agentchannel.ErrConfig, []string{"config"}},
		{"wire_config", agentwire.ErrUnconfigured, []string{"config"}},
		{"transport_used", agenttransport.ErrUsed, []string{"already_used"}},
		{"channel_used", agentchannel.ErrUsed, []string{"already_used"}},
		{"wire_used", agentwire.ErrUsed, []string{"already_used"}},
		{"transport_cleanup", agenttransport.ErrCleanupUnknown, []string{"run_cleanup_unknown"}},
		{"channel_cleanup_alias", agentchannel.ErrCleanupUnknown, []string{"run_cleanup_unknown"}},
		{"exec_cleanup", agentexec.ErrCleanupUnknown, []string{"run_cleanup_unknown"}},
		{"sensitive", errors.New("https://secret.invalid/token private-key member command"), []string{"unknown"}},
		{"panic_string", panicRunError{}, []string{"unknown"}},
		{"joined", errors.Join(agentwire.ErrPeerLost, agentwire.ErrProtocol, agentexec.ErrCleanupUnknown, panicRunError{}), []string{"wire_protocol", "peer_end", "run_cleanup_unknown"}},
		// No invented distinction: the reason is not an errors.Is sentinel.
		{"heartbeat_unclassified", &agentchannel.Error{Reason: agentchannel.ReasonHeartbeat, Err: agentchannel.ErrClosed}, []string{"closed"}},
		{"read_stall_unclassified", &agentchannel.Error{Reason: agentchannel.ReasonReadStall, Err: agentchannel.ErrClosed}, []string{"closed"}},
		{"raw_eof_not_inferred", io.EOF, []string{"unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runObservationCodes(tc.err); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("codes=%v want=%v", got, tc.want)
			}
		})
	}
}
func TestFinishRunObservationKeepsExistingDisposition(t *testing.T) {
	for _, cancelOuter := range []bool{false, true} {
		for _, closeFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("cancel_%t_close_failure_%t", cancelOuter, closeFails), func(t *testing.T) {
				r := newRecorder(t, nil)
				r.session.run = func() error { return errors.Join(agentwire.ErrPeerLost, agentexec.ErrCleanupUnknown, panicRunError{}) }
				if closeFails {
					r.session.closeErr = errors.New("sensitive cleanup text")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelOuter {
					cancel()
				}
				var diagnostic bytes.Buffer
				code := finish(ctx, r.session, r.owner, &macagent.TerminalDisposition{}, time.Second, &diagnostic)
				want := 1
				if cancelOuter && !closeFails {
					want = 0
				}
				if code != want || r.trace() != "run client-close" {
					t.Fatal("existing finish disposition changed")
				}
				out := diagnostic.String()
				for _, s := range []string{"NATIVE_RUN_OBSERVATION category=peer_end\n", "NATIVE_RUN_OBSERVATION category=run_cleanup_unknown\n"} {
					if strings.Count(out, s) != 1 {
						t.Fatal("fixed observation missing or repeated")
					}
				}
				if strings.Contains(out, "sensitive") {
					t.Fatal("raw error leaked")
				}
				terminal := strings.Contains(out, "TERMINAL_CLEANUP_UNKNOWN")
				if terminal != closeFails {
					t.Fatal("Run history changed final cleanup classification")
				}
				if strings.Index(out, "NATIVE_RUN_OBSERVATION") < strings.Index(out, "\n") {
					t.Fatal("observation emitted before cleanup report")
				}
			})
		}
	}
}

// Even a custom Is method is consulted only after the existing cleanup.
type afterCleanupRunError struct{ check func() }

func (e afterCleanupRunError) Error() string { panic("must not format") }
func (e afterCleanupRunError) Is(error) bool { e.check(); return false }
func TestFinishClassifiesOnlyAfterCleanup(t *testing.T) {
	r := newRecorder(t, nil)
	checked := false
	r.session.run = func() error {
		return afterCleanupRunError{check: func() {
			checked = true
			if r.trace() != "run client-close" {
				t.Error("classification before cleanup")
			}
		}}
	}
	var diagnostic bytes.Buffer
	if finish(context.Background(), r.session, r.owner, &macagent.TerminalDisposition{}, time.Second, &diagnostic) != 1 || !checked {
		t.Fatal("classification or disposition missing")
	}
	if !strings.Contains(diagnostic.String(), "NATIVE_RUN_OBSERVATION category=unknown\n") {
		t.Fatal("unknown observation missing")
	}
}

func TestFinishChannelCleanupAliasIsObservedWithoutChangingFinalCleanup(t *testing.T) {
	if agenttransport.ErrCleanupUnknown != agentchannel.ErrCleanupUnknown {
		t.Fatal("transport cleanup alias contract changed")
	}
	r := newRecorder(t, nil)
	r.session.run = func() error { return agentchannel.ErrCleanupUnknown }
	var diagnostic bytes.Buffer
	code := finish(context.Background(), r.session, r.owner, &macagent.TerminalDisposition{}, time.Second, &diagnostic)
	if code != 1 || r.trace() != "run client-close" {
		t.Fatal("finish disposition changed")
	}
	out := diagnostic.String()
	if strings.Count(out, "NATIVE_RUN_OBSERVATION category=run_cleanup_unknown\n") != 1 {
		t.Fatal("channel cleanup observation missing")
	}
	if strings.Contains(out, "TERMINAL_CLEANUP_UNKNOWN") {
		t.Fatal("historical error changed successful final Close")
	}
}
