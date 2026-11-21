package agenttransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The gateway cannot tell a live duplicate from the same Mac reconnecting
// before it saw the original end (#380). Its occupancy refusal must therefore
// be retried, but only for a bounded time, so a real duplicate still stops.
// The contract values are fixed here, apart from the implementation constants.
const (
	occupiedLimit = 60 * time.Second
	occupiedClose = websocket.StatusCode(4013)
)

// A Mac built before the occupancy refusal must still stop on it, as it did on
// the former 1008, so a real duplicate of an older Mac never retries forever.
func TestResidentOccupiedCloseStopsAMacThatPredatesIt(t *testing.T) {
	refusal := fmt.Errorf("failed to get reader: %w", websocket.CloseError{Code: occupiedClose, Reason: "resident control occupied"})
	if residentRetryable(refusal, false) || residentRetryable(refusal, true) {
		t.Fatal("the classification older Macs use retries the occupancy refusal")
	}
}

func TestResidentIdentityRefusalReportsReasonAndDoesNotRetry(t *testing.T) {
	r := newResidentRig(t, nil)
	refuse := residentRefusing(r)
	refuse.Store(int32(websocket.StatusPolicyViolation))

	err := await(t, residentRun(r.c), 5*time.Second)
	reason, hasReason := ResidentNeedsActionReason(err)
	if !errors.Is(err, ErrRefused) || !hasReason || reason != ResidentFailureIdentityRefused {
		t.Fatal("identity refusal did not expose its reason code", err)
	}
	if r.m.trips.Load() != 1 || r.c.Connected() {
		t.Fatal("identity refusal retried or remained connected", r.m.trips.Load())
	}
}

func TestResidentFailureDisposition(t *testing.T) {
	closeErr := func(code websocket.StatusCode) error {
		return fmt.Errorf("received close: %w", websocket.CloseError{Code: code, Reason: "synthetic"})
	}
	for _, tc := range []struct {
		name            string
		err             error
		authenticated   bool
		occupiedExpired bool
		wantRetry       bool
		wantReason      ResidentFailureReason
	}{
		{"1008", closeErr(websocket.StatusPolicyViolation), false, false, false, ResidentFailureIdentityRefused},
		{"4013 within 60 seconds", closeErr(occupiedClose), false, false, true, ""},
		{"4013 after 60 seconds", closeErr(occupiedClose), false, true, false, ResidentFailureConnectionOccupied},
		{"429", errResidentUnavailable, false, false, true, ""},
		{"503", errResidentUnavailable, false, false, true, ""},
		{"EOF", io.EOF, true, false, true, ""},
		{"timeout", &net.DNSError{IsTimeout: true, Err: "timeout", Name: "gateway"}, false, false, true, ""},
		{"normal close", closeErr(websocket.StatusNormalClosure), true, false, true, ""},
		{"going away", closeErr(websocket.StatusGoingAway), true, false, true, ""},
		{"internal error", closeErr(websocket.StatusInternalError), true, false, true, ""},
		{"resident relay expiry", closeErr(websocket.StatusCode(4000)), true, false, true, ""},
		{"unknown close code", closeErr(websocket.StatusCode(4001)), false, false, false, ResidentFailureGatewayRefused},
		{"local refusal without 1008", ErrRefused, false, false, false, ResidentFailureGatewayRefused},
		{"other HTTP response", errors.New("websocket dial returned HTTP 403"), false, false, false, ResidentFailureGatewayRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retry, reason := residentFailureDisposition(tc.err, tc.authenticated, tc.occupiedExpired)
			if retry != tc.wantRetry || reason != tc.wantReason {
				t.Fatalf("residentFailureDisposition() = (%v, %q), want (%v, %q)", retry, reason, tc.wantRetry, tc.wantReason)
			}
		})
	}
}

func TestResidentHTTPRetryStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusBadRequest, false},
		{http.StatusForbidden, false},
		{http.StatusInternalServerError, false},
	} {
		if got := residentHTTPRetryable(tc.status); got != tc.want {
			t.Errorf("residentHTTPRetryable(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestResidentOccupiedLimitUsesStrictlyMoreThanSixtySeconds(t *testing.T) {
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	if residentOccupiedExpired(start, start.Add(60*time.Second)) || !residentOccupiedExpired(start, start.Add(60*time.Second+time.Nanosecond)) {
		t.Fatal("occupancy limit must expire only after 60 seconds")
	}
}

func TestResidentJitterStaysWithinTwentyPercentBelowBase(t *testing.T) {
	for _, tc := range []struct {
		value float64
		want  time.Duration
	}{
		{0, 4 * time.Second},
		{0.5, 4500 * time.Millisecond},
		{1, 5 * time.Second},
		{-1, 4 * time.Second},
		{2, 5 * time.Second},
	} {
		if got := residentJitteredDelay(5*time.Second, func() float64 { return tc.value }); got != tc.want {
			t.Errorf("residentJitteredDelay(%v) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestResidentRetryBackoffPreservesFiveSecondDoublingAndThirtySecondCap(t *testing.T) {
	for _, tc := range []struct{ current, want time.Duration }{
		{5 * time.Second, 10 * time.Second},
		{10 * time.Second, 20 * time.Second},
		{20 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second},
	} {
		if got := nextResidentRetryDelay(tc.current); got != tc.want {
			t.Errorf("nextResidentRetryDelay(%v) = %v, want %v", tc.current, got, tc.want)
		}
	}
}

// residentSameIdentity returns a second client with the rig's identity on its
// own transport, as the same Mac reconnecting or a duplicate would present.
func residentSameIdentity(t *testing.T, r *residentRig) *ResidentClient {
	t.Helper()
	c, e := NewResidentClient(r.c.cfg)
	if e != nil {
		t.Fatal(e)
	}
	m := newMemTransport(r.h)
	c.retryDelay = time.Millisecond
	c.transport = func(own acquirer) http.RoundTripper { m.own = own; m.track = own.attach; return m }
	return c
}

// residentHold connects a same-identity holder that keeps the gateway's slot
// until the returned release is called (also at cleanup, before the rig's).
func residentHold(t *testing.T, r *residentRig) (release func()) {
	t.Helper()
	holder := residentSameIdentity(t, r)
	done := residentRun(holder)
	verticalEventually(t, func() bool { return holder.Connected() && r.h.ResidentConnected(hostLabel, member1) })
	var once atomic.Bool
	release = func() {
		if once.Swap(true) {
			return
		}
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = holder.Close(ctx)
		_ = await(t, done, 3*time.Second)
	}
	t.Cleanup(release)
	return release
}

// residentRefusing answers the rig client's handshakes with a gateway-style
// refusal close after its hello while the returned code is non-zero.
func residentRefusing(r *residentRig) *atomic.Int32 {
	code := &atomic.Int32{}
	real := r.m.handler
	r.m.handler = http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		refusal := websocket.StatusCode(code.Load())
		if refusal == 0 {
			real.ServeHTTP(w, q)
			return
		}
		ws, e := websocket.Accept(w, q, &websocket.AcceptOptions{Subprotocols: []string{ResidentSubprotocol}, CompressionMode: websocket.CompressionDisabled})
		if e != nil {
			return
		}
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if _, _, e = ws.Read(ctx); e == nil {
			_ = ws.Close(refusal, "synthetic gateway refusal")
		}
		_ = ws.CloseNow()
	})
	return code
}

// residentAwaitAlive waits for cond while Run keeps going; Run ending first fails.
func residentAwaitAlive(t *testing.T, done <-chan error, what string, cond func() bool) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for !cond() {
		select {
		case e := <-done:
			t.Fatalf("%s: resident ended: %v", what, e)
		case <-timeout.C:
			t.Fatalf("%s: not observed", what)
		case <-time.After(time.Millisecond):
		}
	}
}

func residentConnectedNew(r *residentRig, old string) func() bool {
	return func() bool {
		return r.c.Connected() && r.h.ResidentConnected(hostLabel, member1) && residentGeneration(r.h) != old
	}
}

func residentStop(t *testing.T, r *residentRig, done <-chan error) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e := r.c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	_ = await(t, done, 3*time.Second)
}

// After the original's context ended the slot may still be unreleased: its
// finish outlasts the registration wait, or the slot stays held afterwards
// (here by the original itself, whose cleanup was never confirmed).
func TestResidentGatewayRefusesAnUnreleasedSlotAsOccupied(t *testing.T) {
	for _, pending := range []bool{true, false} {
		t.Run(fmt.Sprintf("finish-pending=%v", pending), func(t *testing.T) {
			r := newResidentRig(t, nil)
			ended, cancel := context.WithCancel(context.Background())
			cancel()
			old := &residentPeer{ctx: ended, finished: make(chan struct{})}
			if !pending {
				close(old.finished)
			}
			k := residentMapKey(hostLabel, member1)
			r.h.mu.Lock()
			r.h.residents[k] = old
			r.h.mu.Unlock()
			c := residentSameIdentity(t, r)
			// Outlast the gateway's wait so its close, not this client's own
			// authentication deadline, ends the attempt.
			c.cfg.Budgets.Registration = 2 * time.Second
			e, clean, authenticated := c.attempt(context.Background())
			r.h.mu.Lock()
			kept := r.h.residents[k] == old
			delete(r.h.residents, k)
			r.h.mu.Unlock()
			if pending {
				close(old.finished)
			}
			if websocket.CloseStatus(e) != occupiedClose || clean != nil || authenticated || !kept {
				t.Fatal("unreleased slot was not refused as occupied", e, clean, authenticated, kept)
			}
		})
	}
}

// enteredContext reports the first time a waiter selects on it.
type enteredContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *enteredContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

// Handler.Close marks the handler closed before ending contexts; a waiting
// registration must then be refused as closed (1008), not as occupied.
func TestResidentReservationWaitingAtCloseIsRefusedAsClosed(t *testing.T) {
	r := newResidentRig(t, nil)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	old := &residentPeer{ctx: ended, finished: make(chan struct{})}
	defer close(old.finished)
	k := residentMapKey(hostLabel, member1)
	r.h.mu.Lock()
	r.h.residents[k] = old
	r.h.mu.Unlock()
	wait, stop := context.WithCancel(context.Background())
	probe := &enteredContext{Context: wait, entered: make(chan struct{})}
	got := make(chan error, 1)
	go func() {
		got <- r.h.reserveResident(probe, &residentPeer{binding: residentBinding{host: hostLabel, principal: member1}})
	}()
	<-probe.entered
	r.h.mu.Lock()
	r.h.closed = true
	r.h.mu.Unlock()
	stop()
	e := <-got
	r.h.mu.Lock()
	delete(r.h.residents, k)
	r.h.mu.Unlock()
	if !errors.Is(e, ErrRefused) || errors.Is(e, errResidentOccupied) {
		t.Fatal("registration waiting at close was not refused as closed", e)
	}
}

// Only an authentication restarts the limit; other failures between occupancy
// refusals must not, or a duplicate meeting them would never stop.
func TestResidentOccupiedLimitIsNotRestartedByOtherFailures(t *testing.T) {
	r := newResidentRig(t, nil)
	refuse := residentRefusing(r)
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	var offset atomic.Int64
	r.c.clock = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	refuse.Store(int32(occupiedClose))
	done := residentRun(r.c)
	residentAwaitAlive(t, done, "occupied refusals", func() bool { return r.m.trips.Load() >= 2 })
	refuse.Store(int32(websocket.StatusInternalError))
	at := r.m.trips.Load()
	residentAwaitAlive(t, done, "transient refusals", func() bool { return r.m.trips.Load() >= at+2 })
	offset.Store(int64(occupiedLimit + time.Second))
	refuse.Store(int32(occupiedClose))
	e := await(t, done, 5*time.Second)
	if !errors.Is(e, ErrRefused) || websocket.CloseStatus(e) != occupiedClose {
		t.Fatal("occupied refusals past the limit kept retrying across other failures", e)
	}
}

func TestResidentOccupiedLimitStartsAtTheFirstOccupiedRefusal(t *testing.T) {
	r := newResidentRig(t, nil)
	refuse := residentRefusing(r)
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	var offset atomic.Int64
	r.c.clock = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	// Transient relay loss first: retried, but not an occupancy refusal.
	refuse.Store(int32(websocket.StatusInternalError))
	done := residentRun(r.c)
	residentAwaitAlive(t, done, "transient refusals", func() bool { return r.m.trips.Load() >= 2 })
	offset.Store(int64(occupiedLimit + time.Second))
	refuse.Store(int32(occupiedClose))
	at := r.m.trips.Load()
	residentAwaitAlive(t, done, "first occupied refusals", func() bool { return r.m.trips.Load() >= at+2 })
	refuse.Store(0)
	residentAwaitAlive(t, done, "reconnect", residentConnectedNew(r, ""))
	residentStop(t, r, done)
}

func TestResidentOccupiedRefusalReconnectsOnceTheSlotFrees(t *testing.T) {
	r := newResidentRig(t, nil)
	release := residentHold(t, r)
	held := residentGeneration(r.h)
	done := residentRun(r.c)
	residentAwaitAlive(t, done, "occupied refusals", func() bool { return r.m.trips.Load() >= 2 })
	if r.c.Connected() || residentGeneration(r.h) != held {
		t.Fatal("refused client replaced the live holder")
	}
	release()
	residentAwaitAlive(t, done, "reconnect after the slot freed", residentConnectedNew(r, held))
	if r.rec.count.Load() != 0 {
		t.Fatal("occupied retry resumed work", r.rec.count.Load())
	}
	residentStop(t, r, done)
}

func TestResidentOccupiedRefusalStopsAfterTheLimit(t *testing.T) {
	r := newResidentRig(t, nil)
	residentHold(t, r)
	held := residentGeneration(r.h)
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	var offset atomic.Int64
	r.c.clock = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	done := residentRun(r.c)
	residentAwaitAlive(t, done, "occupied refusals", func() bool { return r.m.trips.Load() >= 2 })
	// Exactly the limit after the first refusal is not past it yet. Two more
	// trips prove at least one refusal was judged at this time and retried.
	offset.Store(int64(occupiedLimit))
	at := r.m.trips.Load()
	residentAwaitAlive(t, done, "refusal at the limit", func() bool { return r.m.trips.Load() >= at+2 })
	offset.Store(int64(occupiedLimit + time.Millisecond))
	e := await(t, done, 5*time.Second)
	if !errors.Is(e, ErrRefused) || websocket.CloseStatus(e) != occupiedClose {
		t.Fatal("occupied refusal past the limit did not end as refused", e)
	}
	if residentGeneration(r.h) != held || r.rec.count.Load() != 0 {
		t.Fatal("stopped duplicate disturbed the holder or work")
	}
}

func TestResidentAuthenticationRestartsTheOccupiedLimit(t *testing.T) {
	r := newResidentRig(t, nil)
	refuse := residentRefusing(r)
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	var offset atomic.Int64
	r.c.clock = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	refuse.Store(int32(occupiedClose))
	done := residentRun(r.c)
	residentAwaitAlive(t, done, "occupied refusals", func() bool { return r.m.trips.Load() >= 2 })
	refuse.Store(0)
	residentAwaitAlive(t, done, "authentication", residentConnectedNew(r, ""))
	first := residentGeneration(r.h)
	// Past the limit counted from the refusals before the authentication: only
	// a restarted origin keeps the resident retrying the next refusals.
	offset.Store(int64(occupiedLimit + time.Second))
	refuse.Store(int32(occupiedClose))
	at := r.m.trips.Load()
	r.h.mu.Lock()
	peer := r.h.residents[residentMapKey(hostLabel, member1)]
	r.h.mu.Unlock()
	_ = peer.ws.Close(4000, "synthetic relay limit")
	residentAwaitAlive(t, done, "occupied refusals after authentication", func() bool { return r.m.trips.Load() >= at+2 })
	refuse.Store(0)
	residentAwaitAlive(t, done, "reconnect", residentConnectedNew(r, first))
	if r.rec.count.Load() != 0 {
		t.Fatal("occupied retry resumed work", r.rec.count.Load())
	}
	residentStop(t, r, done)
}

func TestResidentIdentityRefusalIsNotRetried(t *testing.T) {
	r := newResidentRig(t, nil)
	refuse := residentRefusing(r)
	refuse.Store(int32(websocket.StatusPolicyViolation))
	e := await(t, residentRun(r.c), 5*time.Second)
	if !errors.Is(e, ErrRefused) || websocket.CloseStatus(e) != websocket.StatusPolicyViolation || r.m.trips.Load() != 1 {
		t.Fatal("identity refusal was retried or misreported", e, r.m.trips.Load())
	}
}
