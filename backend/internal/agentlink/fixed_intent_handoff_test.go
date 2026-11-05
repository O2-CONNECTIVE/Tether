package agentlink_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

type fixedIntentRuntime struct {
	*macRuntime
	contexts chan context.Context
}

func (r *fixedIntentRuntime) StartShell(ctx context.Context, binding supervisor.Binding, input []byte) (agentexec.GuestProcess, error) {
	r.contexts <- ctx
	return r.macRuntime.StartShell(ctx, binding, input)
}

func TestMacFixedIntentWirePreservesCanonicalOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hostKey, peer := fixtureSigner(t, 111), fixtureSigner(t, 112)
	keys := []ssh.PublicKey{peer.PublicKey()}
	digest, err := runbackend.KeySetDigest(keys)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ephemeral.NewCurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close() })
	challenge, err := issuer.Prepare(ephemeral.ApprovedPolicy{PolicyDigest: agentexec.MacCurrentUserPolicyDigest(), KeySetDigest: digest, GuestBoundary: supervisor.CurrentUserNoIsolation, HardDeadline: time.Now().Add(10 * time.Second), BackendLeaseCeiling: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(ctx, challenge, time.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fixedIntentRuntime{macRuntime: &macRuntime{procs: make(chan *fakeProc, 1)}, contexts: make(chan context.Context, 1)}
	fixed := agentexec.FixedStartConstraint{RequestID: "fixed-memory-once", Command: "memory-fixture-only", TimeoutMillis: 2000}
	session, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: time.Now, CleanupTimeout: time.Second, FixedStart: &fixed})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := session.Close(cleanup); err != nil {
			t.Error("same Session cleanup", err)
		}
	})
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: keys, HostKey: hostKey, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	raw, remote := memPair()
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx, raw) }()
	t.Cleanup(func() {
		cancel()
		_ = remote.Close()
		_ = raw.Close()
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := host.Close(cleanup); err != nil {
			t.Error("same Host cleanup", err)
		}
		select {
		case err := <-done:
			if errors.Is(err, agentexec.ErrCleanupUnknown) || errors.Is(err, agentchannel.ErrCleanupUnknown) {
				t.Error("Serve cleanup unknown", err)
			}
		case <-cleanup.Done():
			t.Error("same Host Serve did not finish")
		}
		if err, observed := host.CleanupResult(); !observed || err != nil {
			t.Error("original Serve cleanup not observed", err, observed)
		}
	})
	dispatch := host.ChannelDispatch()
	channel, err := agentchannel.Open(ctx, agentchannel.ClientConfig{Binding: dispatch.Binding, HostKey: hostKey.PublicKey(), Signer: peer, Deadline: dispatch.Deadline, Budgets: budgets()}, remote)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	client, err := agentwire.NewClient(channel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for _, change := range []string{"request-id", "command", "timeout"} {
		id, command, timeout := fixed.RequestID, fixed.Command, fixed.TimeoutMillis
		switch change {
		case "request-id":
			id = "foreign-fixed-id"
		case "command":
			command = "different-memory-input"
		case "timeout":
			timeout++
		}
		if _, err := client.Start(ctx, id, []byte(command), timeout); !errors.Is(err, agentexec.ErrPolicy) || runtime.startCount() != 0 {
			t.Fatalf("altered fixed intent reached runtime: %s %v", change, err)
		}
	}
	started, err := client.Start(ctx, fixed.RequestID, []byte(fixed.Command), fixed.TimeoutMillis)
	if err != nil {
		t.Fatalf("approved fixed intent refused on canonical wire path: %v", err)
	}
	select {
	case runtimeContext := <-runtime.contexts:
		deadline, finite := runtimeContext.Deadline()
		if !finite || deadline.After(dispatch.Deadline) || !time.Now().Before(deadline) {
			t.Fatalf("runtime lost finite original bound: finite=%t deadline=%v", finite, deadline)
		}
	case <-ctx.Done():
		t.Fatal("same memory runtime not entered")
	}
	if started.Profile != agentexec.ProfileMacCurrentUser || runtime.startCount() != 1 {
		t.Fatal("wrong profile or duplicate memory execution")
	}
	direct := agentexec.StartRequest{Version: agentexec.Version, Binding: session.Binding(), Mode: session.Mode(), RequestID: fixed.RequestID, Command: []byte(fixed.Command), TimeoutMillis: fixed.TimeoutMillis}
	if _, err := session.Start(direct); !errors.Is(err, agentexec.ErrBusy) {
		t.Fatal("direct owner adopted active wire execution", err)
	}
	if _, err := client.Cancel(ctx, started.CommandID); err != nil {
		t.Fatal(err)
	}
	for {
		status, err := client.Status(ctx, started.CommandID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Terminal {
			if status.Cleanup != agentexec.CleanupConfirmed {
				t.Fatal("original memory command cleanup unknown")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("original command did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := session.Start(direct); !errors.Is(err, agentexec.ErrExhausted) {
		t.Fatal("direct owner adopted completed wire execution", err)
	}
	if replay, err := client.Start(ctx, fixed.RequestID, []byte(fixed.Command), fixed.TimeoutMillis); err != nil || replay.CommandID != started.CommandID || !replay.Terminal || runtime.startCount() != 1 {
		t.Fatal("wire lost its own replay or restarted execution", replay, err)
	}
}
