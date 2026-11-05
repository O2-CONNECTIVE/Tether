package agentwire_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

const foreignID = "0123456789abcdef0123456789abcdef"

func TestStartOutputStatusCancelThroughCore(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	st, err := r.client.Start(ctx, "req-1", []byte("Get-Date\n"), 60_000)
	if err != nil || !agentwire.ValidCommandID(st.CommandID) || st.Cleanup != agentexec.CleanupPending || st.Terminal {
		t.Fatalf("Start = %+v, %v", st, err)
	}
	p := r.proc()
	r.runtime.mu.Lock()
	stdin := r.runtime.stdin[0]
	r.runtime.mu.Unlock()
	if string(stdin) != "Get-Date\n" {
		t.Fatalf("guest stdin = %q", stdin)
	}
	raw := []byte{0xff, 0x00, '\n'}
	send(t, p, agentexec.Stdout, []byte("out1"))
	send(t, p, agentexec.Stderr, raw)
	var chunks []agentexec.Chunk
	for cursor, deadline := uint64(0), time.Now().Add(2*time.Second); cursor < 7; {
		out, err := r.client.Output(ctx, st.CommandID, cursor, 1024)
		if err != nil {
			t.Fatalf("Output: %v", err)
		}
		chunks, cursor = append(chunks, out.Chunks...), out.Next
		if time.Now().After(deadline) {
			t.Fatalf("output stalled at %d", cursor)
		}
	}
	if len(chunks) != 2 || chunks[0].Stream != agentexec.Stdout || string(chunks[0].Data) != "out1" ||
		chunks[1].Stream != agentexec.Stderr || chunks[1].Offset != 4 || !bytes.Equal(chunks[1].Data, raw) {
		t.Fatalf("chunks = %+v", chunks)
	}
	running, err := r.client.Status(ctx, st.CommandID)
	if err != nil || running.Phase != agentexec.PhaseRunning || running.OutputEnd != 7 || running.Terminal {
		t.Fatalf("Status = %+v, %v", running, err)
	}
	canceled, err := r.client.Cancel(ctx, st.CommandID)
	if err != nil || canceled.Stop != agentexec.StopCanceled {
		t.Fatalf("Cancel = %+v, %v", canceled, err)
	}
	final := r.awaitTerminal(st.CommandID)
	// A forced stop reports Stop and leaves the natural exit unknown.
	if final.Phase != agentexec.PhaseCanceled || final.Cleanup != agentexec.CleanupConfirmed || final.Exit.Known {
		t.Fatalf("final = %+v", final)
	}
	if got, err := r.client.Status(ctx, st.CommandID); err != nil || got.Status != final || got.Profile != agentexec.ProfileWindowsGuest {
		t.Fatalf("wire Status = %+v, %v; owner saw %+v", got, err, final)
	}
	// Command cancel keeps the grant; cleanup confirmation is the owner's Close.
	if !r.issuer.Snapshot().HasAuthority {
		t.Fatal("command cancel revoked the grant")
	}
	if err := r.session.Close(ctx); err != nil {
		t.Fatalf("owner Close = %v", err)
	}
}

func TestSameRequestReplayHasNoSecondSideEffect(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	first, err := r.client.Start(ctx, "req-1", []byte("whoami\n"), 60_000)
	if err != nil {
		t.Fatal(err)
	}
	r.proc()
	again, err := r.client.Start(ctx, "req-1", []byte("whoami\n"), 60_000)
	if err != nil || again.CommandID != first.CommandID {
		t.Fatalf("replay = %+v, %v; first %s", again, err, first.CommandID)
	}
	if _, err := r.client.Start(ctx, "req-1", []byte("hostname\n"), 60_000); !errors.Is(err, agentexec.ErrRequestMismatch) {
		t.Fatalf("mismatched replay = %v", err)
	}
	if _, err := r.client.Start(ctx, "req-2", []byte("hostname\n"), 60_000); !errors.Is(err, agentexec.ErrBusy) {
		t.Fatalf("second command = %v", err)
	}
	if n := r.runtime.startCount(); n != 1 {
		t.Fatalf("guest starts = %d", n)
	}
}

func TestCommandsNotStartedOnThisStreamAreNotFound(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	ref := agentexec.StatusRequest{Version: agentexec.Version, Binding: r.session.Binding()}
	owner, err := r.session.Start(agentexec.StartRequest{
		Version: agentexec.Version, Binding: r.session.Binding(), RequestID: "req-1", Mode: agentexec.ModeFull,
		Command: []byte("whoami\n"), TimeoutMillis: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.proc()
	for _, id := range []string{owner.CommandID, foreignID} {
		if _, err := r.client.Status(ctx, id); !errors.Is(err, agentexec.ErrNotFound) {
			t.Fatalf("Status(%s) = %v", id, err)
		}
		if _, err := r.client.Cancel(ctx, id); !errors.Is(err, agentexec.ErrNotFound) {
			t.Fatalf("Cancel(%s) = %v", id, err)
		}
		if _, err := r.client.Output(ctx, id, 0, 16); !errors.Is(err, agentexec.ErrNotFound) {
			t.Fatalf("Output(%s) = %v", id, err)
		}
	}
	ref.CommandID = owner.CommandID
	if st, _ := r.session.Status(ref); st.Stop != agentexec.StopNone {
		t.Fatalf("foreign cancel reached the owner's command: %+v", st)
	}
	// The same caller-chosen request ID is namespaced per stream, so it cannot
	// replay into and adopt the owner's command.
	if _, err := r.client.Start(ctx, "req-1", []byte("whoami\n"), 60_000); !errors.Is(err, agentexec.ErrBusy) {
		t.Fatalf("Start with the owner's request ID = %v", err)
	}
	if n := r.runtime.startCount(); n != 1 {
		t.Fatalf("guest starts = %d", n)
	}
	if _, err := r.session.Cancel(ref); err != nil {
		t.Fatal(err)
	}
}

func TestNotSentAndUncertainRequestsAreNeverResent(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.client.Start(gone, "req-1", []byte("whoami\n"), 60_000); !errors.Is(err, agentwire.ErrNotSent) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Start = %v", err)
	}
	if n := r.counted.writes.Load(); n != 0 || r.runtime.startCount() != 0 {
		t.Fatalf("a not-sent request reached the stream: writes=%d", n)
	}

	r.gate.Hold()
	short, done := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer done()
	if _, err := r.client.Start(short, "req-1", []byte("whoami\n"), 60_000); !errors.Is(err, agentwire.ErrUncertain) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start without reply = %v", err)
	}
	r.proc()
	time.Sleep(50 * time.Millisecond)
	if n := r.counted.writes.Load(); n != 1 || r.runtime.startCount() != 1 {
		t.Fatalf("uncertain Start was resent: writes=%d starts=%d", n, r.runtime.startCount())
	}
	r.gate.Release()
	// The caller explicitly reuses the request ID to learn the outcome.
	again, err := r.client.Start(finite(t), "req-1", []byte("whoami\n"), 60_000)
	if err != nil || !agentwire.ValidCommandID(again.CommandID) {
		t.Fatalf("explicit replay = %+v, %v", again, err)
	}
	if n := r.runtime.startCount(); n != 1 {
		t.Fatalf("explicit replay started the guest again: %d", n)
	}
}

func frame(body string) []byte {
	b := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(body)))
	copy(b[4:], body)
	return b
}

func TestMalformedFramesEndTheStreamAndRevokeTheGrant(t *testing.T) {
	status := func(rest string) string {
		return `{"v":1,"id":1,"op":"status","commandId":"` + foreignID + `"` + rest + `}`
	}
	oversize := make([]byte, 4)
	binary.BigEndian.PutUint32(oversize, agentwire.MaxFrame+1)
	cases := map[string][]byte{
		"zero length":       {0, 0, 0, 0},
		"oversize length":   oversize,
		"not json":          frame("{"),
		"trailing data":     frame(status("") + "x"),
		"duplicate member":  frame(status(`,"commandId":"` + foreignID + `"`)),
		"escaped duplicate": frame(status(`,"commandId":"` + foreignID + `"`)),
		"case variant":      frame(`{"v":1,"id":1,"op":"status","CommandId":"` + foreignID + `"}`),
		"binding member":    frame(status(`,"binding":"x"`)),
		"null member":       frame(`{"v":1,"id":1,"op":"status","commandId":null}`),
		"other version":     frame(`{"v":2,"id":1,"op":"status","commandId":"` + foreignID + `"}`),
		"zero id":           frame(`{"v":1,"id":0,"op":"status","commandId":"` + foreignID + `"}`),
		"cleanup op":        frame(`{"v":1,"id":1,"op":"cleanup","commandId":"` + foreignID + `"}`),
		"invalid utf8":      frame("{\"v\":1,\"id\":1,\"op\":\"status\",\"commandId\":\"\xff\"}"),
		"lone surrogate":    frame(`{"v":1,"id":1,"op":"status","commandId":"\ud800"}`),
		"mode member":       frame(`{"v":1,"id":1,"op":"start","requestId":"r","command":"eA==","timeoutMillis":1,"mode":"full"}`),
		"noncanonical b64":  frame(`{"v":1,"id":1,"op":"start","requestId":"r","command":"Zm9=","timeoutMillis":1}`),
		"fraction":          frame(`{"v":1,"id":1,"op":"output","commandId":"` + foreignID + `","cursor":0,"maxBytes":1.5}`),
		"missing member":    frame(`{"v":1,"id":1,"op":"output","commandId":"` + foreignID + `","cursor":0}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			r := newServed(t, agentexec.ProfileWindowsGuest)
			go func() { _, _ = r.peer.Write(raw) }()
			if err := r.waitServed(); !errors.Is(err, agentwire.ErrProtocol) {
				t.Fatalf("Serve = %v", err)
			}
			if r.issuer.Snapshot().HasAuthority || r.runtime.startCount() != 0 {
				t.Fatal("protocol violation left the grant live or started the guest")
			}
		})
	}
}

func TestTransportLossRevokesBeforeTerminateWithoutResume(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	st, err := r.client.Start(ctx, "req-1", []byte("whoami\n"), 60_000)
	if err != nil {
		t.Fatal(err)
	}
	p := r.proc()
	var mu sync.Mutex
	var atTerminate supervisor.State
	terminated := make(chan struct{})
	p.setTerminateHook(func() {
		mu.Lock()
		defer mu.Unlock()
		atTerminate = r.authority.Snapshot(r.clock.Now()).State
		close(terminated)
	})
	_ = r.client.Close()
	if err := r.waitServed(); err != nil {
		t.Fatalf("Serve after peer close = %v", err)
	}
	select {
	case <-terminated:
	case <-ctx.Done():
		t.Fatalf("Terminate hook was not observed: %v", ctx.Err())
	}
	mu.Lock()
	state := atTerminate
	mu.Unlock()
	if state != supervisor.Revoked {
		t.Fatalf("authority at Terminate = %q", state)
	}
	if err := r.session.Close(ctx); err != nil {
		t.Fatalf("owner Close = %v", err)
	}
	final := r.awaitTerminal(st.CommandID)
	if final.Stop != agentexec.StopTransportLost || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	a, b := net.Pipe()
	defer b.Close()
	if err := r.server.Serve(ctx, a); !errors.Is(err, agentwire.ErrUsed) {
		t.Fatalf("second Serve = %v", err)
	}
	if _, err := r.client.Status(ctx, st.CommandID); !errors.Is(err, agentwire.ErrNotSent) || !errors.Is(err, agentwire.ErrClosed) {
		t.Fatalf("Status after close = %v", err)
	}
	if n := r.runtime.startCount(); n != 1 {
		t.Fatalf("guest starts = %d", n)
	}
}

func TestEndOfStreamAndOwnerCancelRevokeTheGrant(t *testing.T) {
	closed := newServed(t, agentexec.ProfileWindowsGuest)
	_ = closed.peer.Close()
	if err := closed.waitServed(); err != nil {
		t.Fatalf("Serve after end of stream = %v", err)
	}
	cancelled := newServed(t, agentexec.ProfileWindowsGuest)
	cancelled.cancelServe()
	if err := cancelled.waitServed(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve after owner cancel = %v", err)
	}
	for _, r := range []*rig{closed, cancelled} {
		if r.issuer.Snapshot().HasAuthority {
			t.Fatal("the stream ended but the grant stayed live")
		}
	}
}

func TestRevokeAndExpiryDenyNewWork(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	st, err := r.client.Start(ctx, "req-1", []byte("whoami\n"), 60_000)
	if err != nil {
		t.Fatal(err)
	}
	r.proc()
	r.session.Revoke()
	if _, err := r.client.Start(ctx, "req-2", []byte("whoami\n"), 60_000); !errors.Is(err, agentexec.ErrDenied) {
		t.Fatalf("Start after revoke = %v", err)
	}
	if _, err := r.client.Output(ctx, st.CommandID, 0, 16); !errors.Is(err, agentexec.ErrDenied) {
		t.Fatalf("Output after revoke = %v", err)
	}
	r.awaitTerminal(st.CommandID)
	got, err := r.client.Status(ctx, st.CommandID)
	if err != nil || got.Stop != agentexec.StopRevoked || got.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("Status after revoke = %+v, %v", got, err)
	}

	expired := newRig(t, agentexec.ProfileWindowsGuest)
	expired.clock.Advance(2 * time.Hour)
	if _, err := expired.client.Start(ctx, "req-1", []byte("whoami\n"), 60_000); !errors.Is(err, agentexec.ErrDenied) {
		t.Fatalf("Start after expiry = %v", err)
	}
	if expired.issuer.Snapshot().HasAuthority || expired.runtime.startCount() != 0 {
		t.Fatal("expired grant stayed live or started the guest")
	}
}

// The Mac current-user profile is reported read-only from the Session; the
// wire has no member that could select or change it.
func TestMacCurrentUserProfileIsReportedReadOnly(t *testing.T) {
	r := newRig(t, agentexec.ProfileMacCurrentUser)
	ctx := finite(t)
	st, err := r.client.Start(ctx, "req-1", []byte("id -un\n"), 60_000)
	if err != nil || st.Profile != agentexec.ProfileMacCurrentUser {
		t.Fatalf("Start = %+v, %v", st, err)
	}
	r.proc()
	if got, err := r.client.Status(ctx, st.CommandID); err != nil || got.Profile != agentexec.ProfileMacCurrentUser {
		t.Fatalf("Status = %+v, %v", got, err)
	}
	if _, err := r.client.Cancel(ctx, st.CommandID); err != nil {
		t.Fatal(err)
	}
}

func TestOutputRepliesStayBoundedAndContiguous(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	st, err := r.client.Start(ctx, "req-1", []byte("noise\n"), 60_000)
	if err != nil {
		t.Fatal(err)
	}
	p := r.proc()
	const n = 3000
	var want []byte
	for i := range n {
		stream := agentexec.Stdout
		if i%2 == 1 {
			stream = agentexec.Stderr
		}
		b := []byte{byte('a' + i%26)}
		send(t, p, stream, b)
		want = append(want, b...)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		if st, err := r.client.Status(ctx, st.CommandID); err == nil && st.OutputEnd == n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("output was not retained")
		}
	}
	var got []byte
	replies := 0
	for cursor := uint64(0); cursor < n; replies++ {
		out, err := r.client.Output(ctx, st.CommandID, cursor, agentwire.MaxOutputBytes)
		if err != nil || len(out.Chunks) == 0 || out.Complete {
			t.Fatalf("Output(%d) = %d chunks, complete=%v, %v", cursor, len(out.Chunks), out.Complete, err)
		}
		for _, ch := range out.Chunks {
			if (ch.Offset%2 == 0) != (ch.Stream == agentexec.Stdout) {
				t.Fatalf("chunk at %d has stream %d", ch.Offset, ch.Stream)
			}
			got = append(got, ch.Data...)
		}
		cursor = out.Next
	}
	if replies < 2 || !bytes.Equal(got, want) {
		t.Fatalf("replies=%d, got %d bytes, equal=%v", replies, len(got), bytes.Equal(got, want))
	}
}

func TestClientRefusesOutOfRangeRequestsLocally(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	checks := []error{}
	_, err := r.client.Output(ctx, foreignID, 0, agentwire.MaxOutputBytes+1)
	checks = append(checks, err)
	_, err = r.client.Start(ctx, "has space", []byte("x"), 1)
	checks = append(checks, err)
	_, err = r.client.Start(ctx, strings.Repeat("r", agentwire.MaxRequestID+1), []byte("x"), 1)
	checks = append(checks, err)
	_, err = r.client.Start(ctx, "req-1", make([]byte, agentwire.MaxCommandBytes+1), 1)
	checks = append(checks, err)
	_, err = r.client.Status(ctx, strings.ToUpper(foreignID))
	checks = append(checks, err)
	for i, err := range checks {
		if !errors.Is(err, agentwire.ErrNotSent) || !errors.Is(err, agentexec.ErrInvalid) {
			t.Fatalf("check %d = %v", i, err)
		}
	}
	if n := r.counted.writes.Load(); n != 0 {
		t.Fatalf("locally invalid requests were written: %d", n)
	}
}

// The peer key is set only by local composition. It must be in an approved
// set whose canonical runbackend.KeySetDigest equals the grant's digest.
// Every refusal leaves the Session claim intact; the first accepted Server
// takes it and a second Server is refused.
func TestServerRequiresAnApprovedPeerAndTakesTheOnlyClaim(t *testing.T) {
	r := newSessionRig(t, agentexec.ProfileWindowsGuest)
	keys := approvedKeys(t)
	with := func(change func(*agentwire.ServerConfig)) agentwire.ServerConfig {
		cfg := r.config()
		change(&cfg)
		return cfg
	}
	for name, tc := range map[string]struct {
		cfg  agentwire.ServerConfig
		want error
	}{
		"no session":        {with(func(c *agentwire.ServerConfig) { c.Session = nil }), agentwire.ErrUnconfigured},
		"bare session":      {with(func(c *agentwire.ServerConfig) { c.Session = &agentexec.Session{} }), agentwire.ErrUnconfigured},
		"no peer":           {with(func(c *agentwire.ServerConfig) { c.Peer = nil }), agentwire.ErrUnconfigured},
		"no liveness":       {with(func(c *agentwire.ServerConfig) { c.Liveness = nil }), agentwire.ErrUnconfigured},
		"peer not approved": {with(func(c *agentwire.ServerConfig) { c.Peer = fixtureKey(t, 9) }), agentwire.ErrPeer},
		"no approved keys":  {with(func(c *agentwire.ServerConfig) { c.ApprovedKeys = nil }), agentwire.ErrPeer},
		"other key set":     {with(func(c *agentwire.ServerConfig) { c.ApprovedKeys = keys[:1] }), agentwire.ErrPeer},
		"extra key in set":  {with(func(c *agentwire.ServerConfig) { c.ApprovedKeys = append(approvedKeys(t), fixtureKey(t, 9)) }), agentwire.ErrPeer},
	} {
		if s, err := agentwire.NewServer(tc.cfg); s != nil || !errors.Is(err, tc.want) {
			t.Fatalf("%s: NewServer = %v", name, err)
		}
	}
	// The canonical rule ignores order and duplicates, exactly as runbackend.
	cfg := with(func(c *agentwire.ServerConfig) {
		c.Peer, c.ApprovedKeys = keys[1], []ssh.PublicKey{keys[1], keys[0], keys[1]}
	})
	if _, err := agentwire.NewServer(cfg); err != nil {
		t.Fatalf("approved peer refused: %v", err)
	}
	if _, err := agentwire.NewServer(r.config()); !errors.Is(err, agentexec.ErrSessionClaimed) {
		t.Fatalf("second Server = %v", err)
	}
	if _, err := agentwire.NewClient(nil); !errors.Is(err, agentwire.ErrUnconfigured) {
		t.Fatalf("NewClient(nil) = %v", err)
	}
}

// Liveness loss of a stalled stream revokes the grant before the stream is
// closed, and only that close unblocks the stalled Write. Serve has no idle
// timer: the signal is the owner's.
func TestLivenessLossRevokesBeforeClosingAStalledStream(t *testing.T) {
	r := newRig(t, agentexec.ProfileWindowsGuest)
	ctx := finite(t)
	st, err := r.client.Start(ctx, "req-1", []byte("sleep 60\n"), 60_000)
	if err != nil {
		t.Fatal(err)
	}
	r.proc()
	var state atomic.Value
	hook := func() { state.Store(r.authority.Snapshot(r.clock.Now()).State) }
	r.gate.onClose.Store(&hook)
	r.gate.Hold()
	stalled := make(chan error, 1)
	go func() {
		_, err := r.client.Status(ctx, st.CommandID)
		stalled <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if !r.issuer.Snapshot().HasAuthority {
		t.Fatal("a stalled reply alone revoked the grant")
	}
	close(r.lost)
	if err := r.waitServed(); !errors.Is(err, agentwire.ErrPeerLost) {
		t.Fatalf("Serve = %v", err)
	}
	if got := state.Load(); got != supervisor.Revoked {
		t.Fatalf("authority when the stream closed = %v", got)
	}
	if final := r.awaitTerminal(st.CommandID); final.Stop != agentexec.StopTransportLost {
		t.Fatalf("final = %+v", final)
	}
	select {
	case err := <-stalled:
		if !errors.Is(err, agentwire.ErrUncertain) {
			t.Fatalf("stalled Status = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stalled call was not released")
	}
}

// An early refusal with a held Session revokes the grant before closing the
// given stream; a later Serve of the same Server only closes its own stream.
func TestServeEarlyExitRevokesTheHeldSession(t *testing.T) {
	r := newSessionRig(t, agentexec.ProfileWindowsGuest)
	srv, err := agentwire.NewServer(r.config())
	if err != nil {
		t.Fatal(err)
	}
	var state atomic.Value
	a, b := net.Pipe()
	defer b.Close()
	g := &gateConn{Conn: a, closed: make(chan struct{})}
	hook := func() { state.Store(r.authority.Snapshot(r.clock.Now()).State) }
	g.onClose.Store(&hook)
	var noContext context.Context
	if err := srv.Serve(noContext, g); !errors.Is(err, agentwire.ErrUnconfigured) {
		t.Fatalf("Serve(nil ctx) = %v", err)
	}
	if got := state.Load(); got != supervisor.Revoked || r.issuer.Snapshot().HasAuthority {
		t.Fatalf("authority when the stream closed = %v", got)
	}
	c, d := net.Pipe()
	defer d.Close()
	if err := srv.Serve(finite(t), c); !errors.Is(err, agentwire.ErrUsed) {
		t.Fatalf("second Serve = %v", err)
	}
}

func TestEveryCoreValueHasAWireName(t *testing.T) {
	seen := map[string]bool{}
	check := func(kind, name string) {
		if name == "" || seen[kind+name] {
			t.Fatalf("%s value has no unique wire name: %q", kind, name)
		}
		seen[kind+name] = true
	}
	for p := agentexec.PhaseStarting; p <= agentexec.PhaseStartUnknown; p++ {
		check("phase", agentwire.PhaseName(p))
	}
	for s := agentexec.StopNone; s <= agentexec.StopRuntimeFault; s++ {
		check("stop", agentwire.StopName(s))
	}
	for c := agentexec.CleanupPending; c <= agentexec.CleanupNotRequired; c++ {
		check("cleanup", agentwire.CleanupName(c))
	}
	for _, s := range []agentexec.Stream{agentexec.Stdout, agentexec.Stderr} {
		check("stream", agentwire.StreamName(s))
	}
	for _, p := range []agentexec.Profile{agentexec.ProfileWindowsGuest, agentexec.ProfileMacCurrentUser} {
		check("profile", agentwire.ProfileName(p))
	}
	for _, err := range []error{
		agentexec.ErrUnconfigured, agentexec.ErrVersion, agentexec.ErrInvalid, agentexec.ErrBinding, agentexec.ErrDenied,
		agentexec.ErrRestrictedUndefined, agentexec.ErrRequestMismatch, agentexec.ErrBusy, agentexec.ErrCleanupUnknown,
		agentexec.ErrNotFound, agentexec.ErrCursor, agentexec.ErrExhausted, agentexec.ErrPolicy, agentexec.ErrProfile,
		agentexec.ErrIdentifier, agentexec.ErrSessionClaimed,
	} {
		check("error", agentwire.CoreCode(err))
	}
	if agentwire.PhaseName(agentexec.PhaseStartUnknown+1) != "" || agentwire.ProfileName(0) != "" {
		t.Fatal("an undefined value got a name")
	}
}

func TestCheckJSONStrictSubset(t *testing.T) {
	deep := strings.Repeat("[", 33) + strings.Repeat("]", 33)
	for _, ok := range []string{`{}`, ` {"a":[1,-0.5e+3,true,false,"😀é"]} `, `"x"`, strings.Repeat("[", 32) + strings.Repeat("]", 32)} {
		if err := agentwire.CheckJSON([]byte(ok), false); err != nil {
			t.Fatalf("CheckJSON(%s) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		``, `{`, `{"a":1,"a":2}`, `{"a":1,"a":2}`, `{"a":{"b":1,"b":1}}`, `"\ud800"`, `"\udc00"`, `"\ud800A"`,
		`"\x"`, "\"\x01\"", `[1,]`, `01`, `1.`, `-`, `{"a" 1}`, `tru`, `null`, `{"a":null}`, "\"\xff\"", deep, `1 2`,
	} {
		if err := agentwire.CheckJSON([]byte(bad), false); !errors.Is(err, agentwire.ErrJSON) {
			t.Fatalf("CheckJSON(%q) = %v", bad, err)
		}
	}
	if err := agentwire.CheckJSON([]byte(`{"a":null}`), true); err != nil {
		t.Fatalf("allowed null = %v", err)
	}
	if _, err := agentwire.Fields([]byte(`{"Op":1}`), "op"); !errors.Is(err, agentwire.ErrJSON) {
		t.Fatalf("case-folded member = %v", err)
	}
}
