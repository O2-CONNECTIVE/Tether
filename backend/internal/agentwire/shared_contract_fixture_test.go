package agentwire_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// The shared contract runs through real issuer, Session and wire code, but only
// memory streams and fake runtime processes. The profile changes policy and
// grant boundary; it does NOT model Windows isolation or Mac process cleanup.
type contractProcess struct {
	startEntered, startRelease     chan struct{}
	releaseEntered, releaseAllowed chan struct{}
	events                         chan event
	terminated                     chan struct{}
	terminateOnce, releaseOnce     sync.Once
	startOnce, allowOnce           sync.Once
	releaseFails                   atomic.Bool
	releases                       atomic.Int32
}

func newContractProcess() *contractProcess {
	return &contractProcess{
		startEntered: make(chan struct{}), startRelease: make(chan struct{}),
		releaseEntered: make(chan struct{}), releaseAllowed: make(chan struct{}),
		events: make(chan event, 2), terminated: make(chan struct{}),
	}
}
func (p *contractProcess) allowStart()   { p.startOnce.Do(func() { close(p.startRelease) }) }
func (p *contractProcess) allowRelease() { p.allowOnce.Do(func() { close(p.releaseAllowed) }) }
func (p *contractProcess) Next(ctx context.Context) (agentexec.Stream, []byte, error) {
	select {
	case e, ok := <-p.events:
		if !ok {
			return 0, nil, io.EOF
		}
		return e.stream, e.data, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-p.terminated:
		return 0, nil, io.ErrClosedPipe
	}
}
func (p *contractProcess) Wait(context.Context) (agentexec.ExitStatus, error) {
	return agentexec.ExitStatus{Code: 7}, nil
}
func (p *contractProcess) Terminate(context.Context) error {
	p.terminateOnce.Do(func() { close(p.terminated) })
	return nil
}
func (p *contractProcess) Release(ctx context.Context) error {
	p.releases.Add(1)
	p.releaseOnce.Do(func() { close(p.releaseEntered) })
	select {
	case <-p.releaseAllowed:
		if p.releaseFails.Load() {
			return errors.New("fixture release unconfirmed")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type contractRuntime struct {
	profile agentexec.Profile
	plans   chan *contractProcess
	starts  atomic.Int32
}

func (r *contractRuntime) AgentProfile() agentexec.Profile { return r.profile }
func (r *contractRuntime) StartShell(ctx context.Context, _ supervisor.Binding, _ []byte) (agentexec.GuestProcess, error) {
	r.starts.Add(1)
	select {
	case p := <-r.plans:
		close(p.startEntered)
		select {
		case <-p.startRelease:
		case <-ctx.Done():
		}
		// A late partial acquisition stays owned, even after cancellation.
		return p, nil
	default:
		return nil, agentexec.ErrNotStarted
	}
}

type contractRig struct {
	*rig
	runtime *contractRuntime
}

func newContractRig(t *testing.T, profile agentexec.Profile) *contractRig {
	t.Helper()
	r := &contractRig{rig: &rig{t: t, clock: &testClock{now: time.Now()}, lost: make(chan struct{})}, runtime: &contractRuntime{profile: profile, plans: make(chan *contractProcess, 4)}}
	r.issuer, r.authority = newGrant(t, r.clock, profile)
	var err error
	r.session, err = agentexec.New(agentexec.Config{Issuer: r.issuer, Authority: r.authority, Runtime: r.runtime, Clock: r.clock.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r.server, err = agentwire.NewServer(r.config())
	if err != nil {
		t.Fatal(err)
	}
	server, peer := net.Pipe()
	r.peer = peer
	r.client, err = agentwire.NewClient(peer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancelServe, r.served = cancel, make(chan error, 1)
	go func() { r.served <- r.server.Serve(ctx, server) }()
	t.Cleanup(func() {
		_ = r.client.Close()
		cancel()
		if err := r.waitServed(); errors.Is(err, errServeHung) {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := r.session.Close(ctx); err != nil {
			t.Errorf("fixture owner cleanup: %v", err)
		}
	})
	return r
}
func (r *contractRig) prepare() *contractProcess {
	p := newContractProcess()
	r.runtime.plans <- p
	// Failure paths also release the original fixture; this is test teardown,
	// not a product retry or proof of native cleanup.
	r.t.Cleanup(func() { p.releaseFails.Store(false); p.allowStart(); p.allowRelease() })
	return p
}
func contractAwait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("contract fixture did not reach barrier")
	}
}
func (r *contractRig) status(id string, ready func(agentwire.Status) bool) agentwire.Status {
	r.t.Helper()
	ctx := finite(r.t)
	for {
		st, err := r.client.Status(ctx, id)
		if err != nil {
			r.t.Fatal(err)
		}
		if ready(st) {
			return st
		}
		select {
		case <-ctx.Done():
			r.t.Fatalf("status never reached contract boundary: %+v", st)
		case <-time.After(time.Millisecond):
		}
	}
}
func (r *contractRig) assertViews(st agentwire.Status, starting, running, stopping int, cleanup agentexec.Cleanup, revoked bool) {
	r.t.Helper()
	core, err := r.session.Status(agentexec.StatusRequest{Version: agentexec.Version, Binding: r.session.Binding(), CommandID: st.CommandID})
	if err != nil || core != st.Status || st.Profile != r.runtime.profile {
		r.t.Fatalf("wire/core disagreement: wire=%+v core=%+v err=%v", st, core, err)
	}
	ctx := finite(r.t)
	for {
		v, ok := r.session.LocalSummary()
		if ok {
			if v.Starting != starting || v.Running != running || v.Stopping != stopping || v.Cleanup != cleanup || v.Revoked != revoked || v.Reason != st.Stop || !v.Deadline.Equal(r.session.Grant().HardDeadline) {
				r.t.Fatalf("local/wire disagreement: local=%+v wire=%+v", v, st)
			}
			return
		}
		select {
		case <-ctx.Done():
			r.t.Fatal("local summary stayed contended")
		case <-time.After(time.Millisecond):
		}
	}
}
