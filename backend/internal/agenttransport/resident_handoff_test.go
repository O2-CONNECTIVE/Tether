package agenttransport

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func connectorRig(t *testing.T) *residentRig {
	t.Helper()
	r := newResidentRig(t, nil)
	c, e := NewResidentConnector(r.c.cfg, 3*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	c.transport = r.c.transport
	c.retryDelay = time.Millisecond
	r.c = c
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return r
}
func takeSource(t *testing.T, r *residentRig) *ResidentSourceLease {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	source, e := r.c.Accept(ctx)
	if e != nil {
		t.Fatal(e)
	}
	return source
}
func finishSource(t *testing.T, s *ResidentSourceLease, err error) error {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	return s.Finish(ctx, err)
}
func TestResidentExclusiveHandoffMintsActualFiniteIntentAndPendingOnly(t *testing.T) {
	r := connectorRig(t)
	done := residentRun(r.c)
	s := takeSource(t, r)
	if s.Receiver() == nil || s.Context() == nil || s.Receiver().HostKeyFingerprint() == "" || s.Receiver().PrincipalKeyFingerprint() == "" {
		t.Fatal("missing actual identity context")
	}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if _, e := r.c.Accept(ctx); !errors.Is(e, ErrUsed) {
		t.Fatal("source ownership transferred twice", e)
	}
	result := make(chan error, 1)
	id := fmt.Sprintf("%032x", 1)
	go func() {
		result <- r.h.ProposeResidentFixed(ctx, hostLabel, member1, id, time.Now().Add(2*time.Second).Truncate(time.Millisecond))
	}()
	i, e := s.Next()
	if e != nil {
		t.Fatal(e)
	}
	if i.Receiver() != s.Receiver() || i.OriginalContext() == nil || i.OriginalContext().Err() != nil || i.HostKeyFingerprint() != s.Receiver().HostKeyFingerprint() || i.Deadline() == i.Deadline().Round(0) {
		t.Fatal("intent lost observed original binding or monotonic lifetime")
	}
	if e = s.Pending(i); e != nil {
		t.Fatal(e)
	}
	if e = await(t, result, 3*time.Second); !errors.Is(e, ErrWorkPending) {
		t.Fatal("admission receipt became execution success", e)
	}
	if e = s.Refuse(i); !errors.Is(e, ErrRefused) {
		t.Fatal("duplicate response allowed", e)
	}
	s.ReleaseWork(i)
	if i.OriginalContext().Err() == nil {
		t.Fatal("work not released")
	}
	r.c.Stop()
	if e = finishSource(t, s, nil); e != nil {
		t.Fatal(e)
	}
	if e = await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e, seen := r.c.CleanupResult(); e != nil || !seen {
		t.Fatal(e, seen)
	}
}
func TestResidentExclusiveNextAndFinishJoinOriginalReader(t *testing.T) {
	r := connectorRig(t)
	done := residentRun(r.c)
	s := takeSource(t, r)
	reading := make(chan error, 1)
	go func() { _, e := s.Next(); reading <- e }()
	verticalEventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reading != nil })
	if _, e := s.Next(); !errors.Is(e, ErrUsed) {
		t.Fatal("dual reader", e)
	}
	r.c.Stop()
	if e := finishSource(t, s, nil); e != nil {
		t.Fatal(e)
	}
	_ = await(t, reading, 3*time.Second)
	_ = await(t, done, 3*time.Second)
	if !s.completed() {
		t.Fatal("original reader not joined")
	}
	if _, e := s.Next(); !errors.Is(e, ErrUsed) {
		t.Fatal("finished source resumed", e)
	}
}
func TestResidentConsumerUnknownBlocksReconnectAndCannotBeReplaced(t *testing.T) {
	r := connectorRig(t)
	done := residentRun(r.c)
	s := takeSource(t, r)
	sentinel := errors.New("synthetic original native cleanup failure")
	if e := finishSource(t, s, sentinel); !errors.Is(e, ErrCleanupUnknown) || !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	if e := await(t, done, 3*time.Second); !errors.Is(e, ErrCleanupUnknown) {
		t.Fatal(e)
	}
	if r.m.trips.Load() != 1 {
		t.Fatal("unknown reconnected", r.m.trips.Load())
	}
	if e := finishSource(t, s, nil); !errors.Is(e, sentinel) {
		t.Fatal("Finish retried or replaced original result", e)
	}
	if e, seen := r.c.CleanupResult(); !seen || !errors.Is(e, ErrCleanupUnknown) {
		t.Fatal(e, seen)
	}
}
func TestResidentStopBeforeRunSealsConnector(t *testing.T) {
	r := connectorRig(t)
	r.c.Stop()
	if e := r.c.Run(context.Background()); !errors.Is(e, ErrUsed) {
		t.Fatal(e)
	}
	if r.m.trips.Load() != 0 {
		t.Fatal("sealed connector dialed")
	}
	if _, e := r.c.Accept(context.Background()); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
}

func TestResidentHandedOffTerminalCauseSurvivesCleanCollection(t *testing.T) {
	for _, kind := range []string{"malformed", "policy1008"} {
		t.Run(kind, func(t *testing.T) {
			r := connectorRig(t)
			done := residentRun(r.c)
			s := takeSource(t, r)
			r.h.mu.Lock()
			peer := r.h.residents[residentMapKey(hostLabel, member1)]
			r.h.mu.Unlock()
			written := make(chan error, 1)
			go func() {
				if kind == "malformed" {
					written <- residentWrite(peer.ctx, peer.ws, []byte(`{"type":"resident_fixed","approved":true}`))
				} else {
					written <- peer.ws.Close(1008, "refused")
				}
			}()
			_, e := s.Next()
			if e == nil {
				t.Fatal("bad frame accepted")
			}
			if e = finishSource(t, s, nil); e != nil {
				t.Fatal("clean native collection became unknown", e)
			}
			if e = await(t, done, 3*time.Second); !errors.Is(e, ErrRefused) {
				t.Fatal("terminal refusal lost", e)
			}
			_ = await(t, written, 3*time.Second)
			if r.m.trips.Load() != 1 {
				t.Fatal("terminal source reconnected", r.m.trips.Load())
			}
		})
	}
}
