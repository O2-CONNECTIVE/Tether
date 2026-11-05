package agenttransport

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Fake in-memory transport only. HTTP404 models the bounded Worker's exact
// expired/closed-gate response; it is not a real Worker or endpoint evaluation.
func TestOuterWindowHTTP404EndsResidentConnector(t *testing.T) {
	for _, previouslyAuthenticated := range []bool{false, true} {
		name := "initial404"
		if previouslyAuthenticated {
			name = "relay4000Then404"
		}
		t.Run(name, func(t *testing.T) {
			r := connectorRig(t)
			var refuse atomic.Bool
			refuse.Store(!previouslyAuthenticated)
			real := r.m.handler
			r.m.handler = http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if refuse.Load() {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				real.ServeHTTP(w, q)
			})
			done := residentRun(r.c)
			wantTrips := int32(1)
			if previouslyAuthenticated {
				s := takeSource(t, r)
				refuse.Store(true)
				r.h.mu.Lock()
				peer := r.h.residents[residentMapKey(hostLabel, member1)]
				r.h.mu.Unlock()
				closed := make(chan error, 1)
				go func() { closed <- peer.ws.Close(4000, "synthetic outer expiry") }()
				if _, e := s.Next(); websocket.CloseStatus(e) != 4000 {
					t.Fatal("expected original relay close", e)
				}
				if e := finishSource(t, s, nil); e != nil {
					t.Fatal(e)
				}
				_ = await(t, closed, 3*time.Second)
				wantTrips = 2
			}
			if e := await(t, done, 3*time.Second); !errors.Is(e, ErrRefused) {
				t.Fatal("404 did not terminate", e)
			}
			if r.m.trips.Load() != wantTrips || r.rec.count.Load() != 0 {
				t.Fatal("unexpected retry/work", r.m.trips.Load(), r.rec.count.Load())
			}
			if _, e := r.c.Accept(context.Background()); !errors.Is(e, ErrClosed) {
				t.Fatal("finished connector still accepts", e)
			}
			if e, seen := r.c.CleanupResult(); e != nil || !seen {
				t.Fatal("original transport cleanup", e, seen)
			}
		})
	}
}

func TestExclusiveRelay4000RequiresOriginalFinishBeforeFreshGeneration(t *testing.T) {
	r := connectorRig(t)
	real := r.m.handler
	var attempts atomic.Int32
	newAttempt := make(chan struct{}, 1)
	r.m.handler = http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if attempts.Add(1) > 1 {
			select {
			case newAttempt <- struct{}{}:
			default:
			}
		}
		real.ServeHTTP(w, q)
	})
	done := residentRun(r.c)
	old := takeSource(t, r)
	first := old.Receiver().Generation()
	r.h.mu.Lock()
	peer := r.h.residents[residentMapKey(hostLabel, member1)]
	r.h.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- peer.ws.Close(4000, "synthetic physical relay limit") }()
	if _, e := old.Next(); websocket.CloseStatus(e) != 4000 {
		t.Fatal(e)
	}
	if old.completed() || r.m.trips.Load() != 1 {
		t.Fatal("source replaced before original consumer finish")
	}
	// Hold the original lease beyond the rig's 1ms retry delay. An early
	// reconnect must become observable before Finish releases the consumer.
	held := time.NewTimer(50 * time.Millisecond)
	defer held.Stop()
	select {
	case <-newAttempt:
		t.Fatal("new authentication began while original consumer was held")
	case <-done:
		t.Fatal("Run completed without original consumer collection")
	case <-held.C:
	}
	if old.completed() || r.m.trips.Load() != 1 {
		t.Fatal("new dial occurred across held original cleanup")
	}
	if e := finishSource(t, old, nil); e != nil {
		t.Fatal(e)
	}
	_ = await(t, closed, 3*time.Second)
	next := takeSource(t, r)
	if next == old || next.Receiver().Generation() == first || r.m.trips.Load() != 2 || r.rec.count.Load() != 0 {
		t.Fatal("fresh generation/re-auth/no-work contract failed")
	}
	r.c.Stop()
	if e := finishSource(t, next, nil); e != nil {
		t.Fatal(e)
	}
	if e := await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
