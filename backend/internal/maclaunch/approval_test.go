package maclaunch

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

type neverRuntime struct{ starts atomic.Int32 }

func (*neverRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }
func (r *neverRuntime) StartShell(context.Context, supervisor.Binding, []byte) (agentexec.GuestProcess, error) {
	r.starts.Add(1)
	return nil, agentexec.ErrNotStarted
}
func config(t *testing.T) Config {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)).Public())
	if err != nil {
		t.Fatal(err)
	}
	return Config{HostKey: signer, AllowedKeys: []ssh.PublicKey{peer}, Runtime: macagent.Config{WorkDir: "/tmp/demo", Home: "/tmp/home"}, Deadline: time.Now().Add(time.Minute), CleanupTimeout: 7 * time.Second, Budgets: agentchannel.Budgets{Handshake: time.Second, HeartbeatInterval: time.Second, HeartbeatTimeout: time.Second, ReadStall: 3 * time.Second, WriteStall: time.Second, Cleanup: time.Second}}
}

type terminal struct {
	output  bytes.Buffer
	reply   string
	readErr error
	onWrite func()
}

func (t *terminal) Write(p []byte) (int, error) {
	n, e := t.output.Write(p)
	if t.onWrite != nil {
		t.onWrite()
	}
	return n, e
}
func (t *terminal) Read(p []byte) (int, error) {
	if t.readErr != nil {
		return 0, t.readErr
	}
	if t.reply == "" {
		return 0, io.EOF
	}
	n := copy(p, t.reply)
	t.reply = t.reply[n:]
	return n, nil
}
func approve(t *testing.T, p *Pending) *Approved {
	t.Helper()
	tty := &terminal{reply: p.phrase + "\n"}
	a, err := p.approve(context.Background(), tty)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return a
}
func TestExplicitLocalApprovalCreatesOneCanonicalOwner(t *testing.T) {
	r := &neverRuntime{}
	p, e := prepare(config(t), r)
	if e != nil {
		t.Fatal(e)
	}
	if p.issuer.Snapshot().HasAuthority {
		t.Fatal("authority before approval")
	}
	a := approve(t, p)
	d := a.Dispatch()
	if d.Binding.Profile != agentexec.ProfileMacCurrentUser || d.Binding.Grant.Generation == 0 || d.Deadline.IsZero() || len(d.HostKey) == 0 {
		t.Fatal(d)
	}
	if r.starts.Load() != 0 {
		t.Fatal("approval started command")
	}
	if _, e = p.approve(context.Background(), &terminal{reply: p.phrase + "\n"}); !errors.Is(e, ErrConsumed) {
		t.Fatal(e)
	}
}
func TestDenialEOFAndMalformedReplyIssueNothing(t *testing.T) {
	for _, reply := range []string{"", "yes\n", "approve\n", strings.Repeat("a", 300) + "\n"} {
		t.Run(reply[:min(len(reply), 8)], func(t *testing.T) {
			p, e := prepare(config(t), &neverRuntime{})
			if e != nil {
				t.Fatal(e)
			}
			if a, e := p.approve(context.Background(), &terminal{reply: reply}); a != nil || e == nil {
				t.Fatal(a, e)
			}
			if p.issuer.Snapshot().HasAuthority {
				t.Fatal("authority on refusal")
			}
		})
	}
}
func TestExpiredWhilePromptingDoesNotExtendDeadline(t *testing.T) {
	cfg := config(t)
	p, e := prepare(cfg, &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	p.clock = func() time.Time { return cfg.Deadline.Add(time.Second) }
	if a, e := p.approve(context.Background(), &terminal{reply: p.phrase + "\n"}); a != nil || e == nil {
		t.Fatal(a, e)
	}
}
func TestCanceledConsentCreatesNoAuthority(t *testing.T) {
	p, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	ctx, c := context.WithCancel(context.Background())
	tty := &terminal{reply: p.phrase + "\n", onWrite: c}
	if a, e := p.approve(ctx, tty); a != nil || e == nil {
		t.Fatal(a, e)
	}
	if p.issuer.Snapshot().HasAuthority {
		t.Fatal("authority on cancel")
	}
}
func TestPromptBindsKeysDeadlineAndNonIsolation(t *testing.T) {
	p, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	tty := &terminal{}
	_, _ = p.approve(context.Background(), tty)
	s := tty.output.String()
	for _, want := range []string{"NO ISOLATION", "current user's resources", "/tmp/demo", p.phrase, ssh.FingerprintSHA256(p.keys[0]), p.deadline.Format(time.RFC3339Nano)} {
		if !strings.Contains(s, want) {
			t.Fatal("missing", want)
		}
	}
}
func TestDispatchCopiesAndCloseRevokes(t *testing.T) {
	p, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	a := approve(t, p)
	d := a.Dispatch()
	d.HostKey[0] ^= 255
	if bytes.Equal(d.HostKey, a.Dispatch().HostKey) {
		t.Fatal("alias")
	}
	if e := a.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-a.session.Done():
	default:
		t.Fatal("not revoked")
	}
}

func TestLocalApprovalDispatchServesAuthenticatedCaller(t *testing.T) {
	cfg := config(t)
	runtime := &neverRuntime{}
	pending, err := prepare(cfg, runtime)
	if err != nil {
		t.Fatal(err)
	}
	owner := approve(t, pending)
	clientSigner, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	d := owner.Dispatch()
	hostKey, err := ssh.ParsePublicKey(d.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	a, b := memPair()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- owner.Serve(ctx, a) }()
	ch, err := agentchannel.Open(ctx, agentchannel.ClientConfig{Binding: d.Binding, HostKey: hostKey, Signer: clientSigner, Deadline: d.Deadline, Budgets: cfg.Budgets}, b)
	if err != nil {
		t.Fatal(err)
	}
	client, err := agentwire.NewClient(ch)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = client.Start(ctx, "local-approved-request", []byte("printf DEMO_OK"), 1000)
	until := time.Now().Add(time.Second)
	for runtime.starts.Load() == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if runtime.starts.Load() != 1 {
		t.Fatal("approved caller did not reach fake runtime")
	}
	_ = client.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("host did not join")
	}
	select {
	case <-owner.session.Done():
	default:
		t.Fatal("grant survived channel")
	}
}

func TestAnotherProposalPhraseCannotApprove(t *testing.T) {
	a, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	b, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	defer a.issuer.Close()
	if a.phrase == b.phrase {
		t.Fatal("reused proposal")
	}
	if result, e := b.approve(context.Background(), &terminal{reply: a.phrase + "\n"}); result != nil || e == nil {
		t.Fatal(result, e)
	}
}

func TestClockRegressionBeforeConfirmationRefuses(t *testing.T) {
	p, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	p.clock = func() time.Time { return p.preparedAt.Add(-time.Second) }
	if a, e := p.approve(context.Background(), &terminal{reply: p.phrase + "\n"}); a != nil || e == nil {
		t.Fatal(a, e)
	}
}
func TestCallerCancellationAfterApprovalRevokes(t *testing.T) {
	p, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, e := p.approve(ctx, &terminal{reply: p.phrase + "\n"})
	if e != nil {
		cancel()
		t.Fatal(e)
	}
	select {
	case <-a.session.Done():
		t.Fatal("approval immediately revoked")
	default:
	}
	cancel()
	select {
	case <-a.session.Done():
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not revoke")
	}
	if e := a.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestBackwardStepAbovePrepareTimeCannotReplenishBudget(t *testing.T) {
	cfg := config(t)
	cfg.Deadline = time.Now().Add(20 * time.Minute)
	p, e := prepare(cfg, &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	p.preparedAt = p.preparedAt.Add(-9 * time.Minute)
	p.clock = func() time.Time { return time.Now().Add(-8 * time.Minute).Round(0) }
	a, e := p.approve(context.Background(), &terminal{reply: p.phrase + "\n"})
	if a != nil {
		_ = a.Close(context.Background())
		t.Fatal("clock rollback replenished grant budget")
	}
	if e == nil || p.issuer.Snapshot().HasAuthority {
		t.Fatal(e)
	}
}
func TestPrepareElapsedBudgetIsCarriedIntoSession(t *testing.T) {
	p, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	p.initialBudget = 200 * time.Millisecond
	a := approve(t, p)
	select {
	case <-a.session.Done():
	case <-time.After(time.Second):
		t.Fatal("Session recomputed a longer elapsed budget")
	}
}

func TestPromptHasVisibleJapaneseInputAndExpiryInstruction(t *testing.T) {
	p, e := prepare(config(t), &neverRuntime{})
	if e != nil {
		t.Fatal(e)
	}
	tty := &terminal{}
	_, _ = p.approve(context.Background(), tty)
	s := tty.output.String()
	for _, want := range []string{"Tether", "【入力待ち】", "残り", "確認文字列: " + p.phrase, "入力して Enter > "} {
		if !strings.Contains(s, want) {
			t.Errorf("missing visible instruction %q", want)
		}
	}
	if !strings.HasSuffix(s, "入力して Enter > ") {
		t.Fatal("input label is not last visible text")
	}
	if strings.Contains(s, "PRIVATE KEY") {
		t.Fatal("private material in prompt")
	}
}

func TestApprovalFailureCategoriesDoNotCreateAuthority(t *testing.T) {
	for _, mode := range []string{"expiry", "cancel", "eof", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			p, e := prepare(config(t), &neverRuntime{})
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tty := &terminal{reply: "wrong\n"}
			var want error
			switch mode {
			case "expiry":
				p.clock = func() time.Time { return p.deadline }
				want = context.DeadlineExceeded
			case "cancel":
				tty.onWrite = cancel
				want = context.Canceled
			case "eof":
				tty.reply = ""
				want = io.EOF
			}
			a, e := p.approve(ctx, tty)
			if a != nil || e == nil || p.issuer.Snapshot().HasAuthority {
				t.Fatal("authority on refusal")
			}
			if want != nil && !errors.Is(e, want) {
				t.Fatal("category missing", e)
			}
			if want == nil && (errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) || errors.Is(e, io.EOF)) {
				t.Fatal("wrong phrase misclassified")
			}
		})
	}
}
