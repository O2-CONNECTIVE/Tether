package agenttransport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

// Review regressions and library-behavior checks. Everything runs on memory
// conns: the real coder/websocket Dial/Accept/Reader/Write under this
// package's msgConn adapter, and in one test the
// real net/http.Server over a fake net.Listener. Nothing listens on a port,
// dials, starts a process or touches a real key.

// captureOwner hands the registered Mac stream to the test. It deliberately
// returns nil from Serve (a contract violation Run must not report as
// success). It is a transport fixture, never an approval.
type captureOwner struct {
	d      maclaunch.Dispatch
	conns  chan net.Conn
	hold   bool // Serve waits for its context before returning
	closes atomic.Int32
}

func newCaptureOwner(host ssh.Signer, launch string, hold bool) *captureOwner {
	d := fixtureDispatch(host, launch, time.Now().Add(time.Minute))
	return &captureOwner{d: maclaunch.Dispatch{Binding: d.Binding, Deadline: d.Deadline, HostKey: d.HostKey}, conns: make(chan net.Conn, 1), hold: hold}
}

func (o *captureOwner) Dispatch() maclaunch.Dispatch { return o.d }

func (o *captureOwner) Serve(ctx context.Context, conn net.Conn) error {
	o.conns <- conn
	if o.hold {
		<-ctx.Done()
	}
	_ = conn.Close()
	return nil
}

func (o *captureOwner) Close(context.Context) error { o.closes.Add(1); return nil }

type libraryPairEnds struct {
	f       *gatewayFixture
	c       *Client
	gw, mac net.Conn
	done    <-chan error
}

// libraryPair registers a real Client (websocket.Dial) with the real Handler
// (websocket.Accept) and returns the gateway and Mac streams.
func libraryPair(t *testing.T, edit func(*memTransport)) libraryPairEnds {
	t.Helper()
	p := libraryPairEnds{f: newGatewayFixture(t, nil, testBudgets())}
	owner := newCaptureOwner(p.f.host1, "launch-pair", true)
	m := newMemTransport(p.f.h)
	if edit != nil {
		edit(m)
	}
	p.c = newMemClient(t, clientConfig(p.f.host1), owner, m)
	p.done = runAsync(context.Background(), p.c)
	p.gw = p.f.rec.next(t).conn
	select {
	case p.mac = <-owner.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("the Mac owner was not served")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.c.Close(ctx)
	})
	return p
}

func roundTrip(t *testing.T, from, to net.Conn, p []byte) {
	t.Helper()
	errc := make(chan error, 1)
	go func() { _, err := from.Write(p); errc <- err }()
	got := make([]byte, len(p))
	_ = to.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(to, got); err != nil || !bytes.Equal(got, p) {
		t.Fatalf("read %d bytes: %v", len(p), err)
	}
	_ = to.SetReadDeadline(time.Time{})
	if err := <-errc; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(end) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- S1: raw close first ----

// A text message in binary mode is refused by the stream, whose owner close
// then runs while this peer never answers any close handshake. The owner's
// Close still confirms quickly: the raw connection is closed first (exactly
// once) and only then is the library joined.
func TestWrongTypeHandshakeDoesNotHoldTheRawClose(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	r := upgraded(t, f.h)
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), r.challenge(), f.host1))
	r.expectAccepted()
	conn := f.rec.next(t).conn
	readEnded := make(chan struct{})
	go func() { _, _ = drain(conn); close(readEnded) }()
	r.text([]byte(`{"type":"not-binary"}`))
	time.Sleep(200 * time.Millisecond) // let the stream see the text message and start its close
	start := time.Now()
	if err := conn.Close(); err != nil {
		t.Fatalf("stream Close = %v", err)
	}
	if elapsed := time.Since(start); elapsed > testBudgets().Cleanup {
		t.Fatalf("Close took %v", elapsed)
	}
	select {
	case <-readEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("the pending read did not end")
	}
	if n := r.server.closeCount(); n != 1 {
		t.Fatalf("raw conn closed %d times", n)
	}
}

// ---- S2: a completed close reports its result ----

type closeErrConn struct{ *memConn }

func (c closeErrConn) Close() error {
	_ = c.memConn.Close()
	return errors.New("fixture: first close failed")
}

func TestCompletedCloseReportsItsResultWithAnEndedContext(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	live, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()

	ok := newCloser(func() {})
	a, _ := memPair()
	if _, err := ok.attach(a); err != nil {
		t.Fatal(err)
	}
	ok.settle()
	if err := ok.wait(live); err != nil {
		t.Fatalf("first wait = %v", err)
	}
	failed := newCloser(func() {})
	b, _ := memPair()
	if _, err := failed.attach(closeErrConn{b}); err != nil {
		t.Fatal(err)
	}
	failed.settle()
	if err := failed.wait(live); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("failed close = %v", err)
	}
	for i := range 1000 {
		if err := ok.wait(ended); err != nil {
			t.Fatalf("iteration %d: completed close reported %v", i, err)
		}
		if err := failed.wait(ended); !errors.Is(err, ErrCleanupUnknown) || errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: failed close reported %v", i, err)
		}
	}
	if b.closeCount() != 1 {
		t.Fatal("a failed first close was re-attempted")
	}

	h, err := NewHandler(HandlerConfig{Endpoint: testEndpoint, Registrar: newRecorder(nil), Budgets: testBudgets(),
		Hosts: []EnrolledHost{{Label: hostLabel, PublicKey: fixtureSigner(t, 0x11).PublicKey(), Principals: []string{member1}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(live); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if err := h.Close(ended); err != nil {
			t.Fatalf("iteration %d: idle Handler.Close = %v", i, err)
		}
	}

	host := fixtureSigner(t, 0x11)
	c := newMemClient(t, clientConfig(host), newStubOwner(host, time.Now().Add(time.Minute)), newMemTransport(http.NotFoundHandler()))
	if err := c.Run(context.Background()); !errors.Is(err, ErrRefused) || errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Run = %v", err)
	}
	for i := range 200 {
		if err := c.Close(ended); err != nil {
			t.Fatalf("iteration %d: finished Client.Close = %v", i, err)
		}
	}
}

// ---- S3: bounded, serialized writes ----

type scriptConn struct {
	net.Conn // unused; only Write is called
	mu       sync.Mutex
	writes   [][]byte
	reply    func(p []byte) (int, error)
}

func (s *scriptConn) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes = append(s.writes, append([]byte(nil), p...))
	s.mu.Unlock()
	runtime.Gosched()
	return s.reply(p)
}

func (s *scriptConn) calls() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func TestStreamWriteRejectsBadProgress(t *testing.T) {
	payload := bytes.Repeat([]byte{1}, 2*MaxStreamMessage+10)
	for _, tc := range []struct {
		name  string
		reply func([]byte) (int, error)
		n     int
		want  error
		calls int
	}{
		{"complete", func(p []byte) (int, error) { return len(p), nil }, len(payload), nil, 3},
		{"zero progress", func([]byte) (int, error) { return 0, nil }, 0, io.ErrShortWrite, 1},
		{"partial", func(p []byte) (int, error) { return len(p) / 2, nil }, MaxStreamMessage / 2, io.ErrShortWrite, 1},
		{"negative", func([]byte) (int, error) { return -1, nil }, 0, errInvalidWrite, 1},
		{"overreported", func(p []byte) (int, error) { return len(p) + 1, nil }, 0, errInvalidWrite, 1},
		{"error with progress", func([]byte) (int, error) { return 10, io.ErrClosedPipe }, 10, io.ErrClosedPipe, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := &scriptConn{reply: tc.reply}
			s := &stream{Conn: sc}
			n, err := s.Write(payload)
			if n != tc.n || (tc.want == nil) != (err == nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("Write = %d, %v; want %d, %v", n, err, tc.n, tc.want)
			}
			if got := sc.calls(); len(got) != tc.calls || len(got[0]) > MaxStreamMessage {
				t.Fatalf("%d calls", len(got))
			}
		})
	}
}

func TestConcurrentStreamWritesDoNotInterleave(t *testing.T) {
	sc := &scriptConn{reply: func(p []byte) (int, error) {
		time.Sleep(time.Millisecond)
		return len(p), nil
	}}
	s := &stream{Conn: sc}
	var wg sync.WaitGroup
	for _, tag := range []byte{'a', 'b', 'c'} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n, err := s.Write(bytes.Repeat([]byte{tag}, 4*MaxStreamMessage)); err != nil || n != 4*MaxStreamMessage {
				t.Errorf("Write %c = %d, %v", tag, n, err)
			}
		}()
	}
	wg.Wait()
	calls := sc.calls()
	if len(calls) != 12 {
		t.Fatalf("%d chunks", len(calls))
	}
	for i := 0; i < len(calls); i += 4 {
		for _, c := range calls[i : i+4] {
			if c[0] != calls[i][0] || len(c) != MaxStreamMessage {
				t.Fatalf("chunks interleaved at %d", i)
			}
		}
	}
}

// ---- S4: Serve returning nil ----

func TestRunNeverReportsSuccessWhenServeReturnsNil(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	owner := newCaptureOwner(f.host1, "launch-nil", false)
	c := newMemClient(t, clientConfig(f.host1), owner, newMemTransport(f.h))
	err := await(t, runAsync(context.Background(), c), 5*time.Second)
	if !errors.Is(err, ErrClosed) || errors.Is(err, ErrCleanupUnknown) || owner.closes.Load() != 1 {
		t.Fatalf("Run = %v, owner closes %d", err, owner.closes.Load())
	}
	f.rec.next(t)
}

// ---- S5: a WebSocket without the observed transport ----

func TestDialWithoutObservedTransportIsRefusedAndStaysUnknown(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	host := f.host1
	owner := newStubOwner(host, time.Now().Add(time.Minute))
	m := newMemTransport(f.h)
	m.bypassTrack = true
	c := newMemClient(t, clientConfig(host), owner, m)
	err := await(t, runAsync(context.Background(), c), 5*time.Second)
	if !errors.Is(err, ErrRefused) || !errors.Is(err, errNoTransport) || !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Run = %v", err)
	}
	if owner.closeCount() != 1 || owner.serves.Load() != 0 || f.rec.count.Load() != 0 {
		t.Fatal("owner not closed exactly once, served, or registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Close claimed an unobserved transport closed: %v", err)
	}
}

// ---- S6: registrar panic ----

func TestRegistrarPanicClosesTheStreamAndKeepsAccounting(t *testing.T) {
	const value = "fixture registrar panic"
	f := newGatewayFixture(t, registrarFunc(func(string, agentgateway.Dispatch, net.Conn) error { panic(value) }), testBudgets())
	recovered := make(chan any, 1)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { recovered <- recover() }()
		f.h.ServeHTTP(w, r)
	})
	r := upgraded(t, wrapped)
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), r.challenge(), f.host1))
	r.expectAccepted()
	select {
	case v := <-recovered:
		if v != value {
			t.Fatalf("recovered %v; the panic was hidden or replaced", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return")
	}
	r.expectRefused(3 * time.Second)
	f.h.mu.Lock()
	reserved, inflight := f.h.reserved, f.h.inflight
	f.h.mu.Unlock()
	if reserved != 0 || inflight != 0 {
		t.Fatalf("reserved %d inflight %d after panic", reserved, inflight)
	}
	for range MaxPending {
		upgraded(t, f.h).challenge()
	}
}

// ---- upgrade phase ownership ----

type stallingWriter struct {
	http.ResponseWriter
	gate <-chan struct{}
}

// Hijack models net/http stalling while it flushes the 101 and hands over
// the connection.
func (w *stallingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	<-w.gate
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func stalledUpgrade(h http.Handler, gate <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&stallingWriter{ResponseWriter: w, gate: gate}, r)
	})
}

// expectUpgradeThenClosed reads the 101 that net/http sent and then requires
// the connection to end without any registration frame.
func expectUpgradeThenClosed(t *testing.T, r *rawWS, req *http.Request) {
	t.Helper()
	_ = r.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if resp, err := http.ReadResponse(r.br, req); err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade response: %v", err)
	}
	if _, _, err := r.next(3 * time.Second); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("after the late hijack: %v", err)
	}
	select {
	case <-r.served:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHTTP did not return")
	}
	if n := r.server.closeCount(); n != 1 {
		t.Fatalf("raw conn closed %d times", n)
	}
}

// The registration budget runs from before Accept: a hijack that completes
// after it is closed at once by the same owner, and nothing registers.
func TestBudgetCoversAStalledUpgrade(t *testing.T) {
	gate := make(chan struct{})
	f := newGatewayFixture(t, nil, testBudgets())
	r, req := sendUpgrade(t, stalledUpgrade(f.h, gate), nil, nil)
	time.Sleep(testBudgets().Registration + 200*time.Millisecond)
	close(gate)
	expectUpgradeThenClosed(t, r, req)
	if f.rec.count.Load() != 0 {
		t.Fatal("a stalled upgrade reached the registrar")
	}
}

// Handler.Close during a stalled upgrade reports it unknown without starting
// a waiter goroutine, and confirms it once the hijack returns.
func TestHandlerCloseDuringAStalledUpgrade(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f := newGatewayFixture(t, nil, Budgets{Registration: 10 * time.Second, Approval: 20 * time.Second, Cleanup: time.Second})
	r, req := sendUpgrade(t, stalledUpgrade(f.h, gate), nil, nil)
	eventually(t, "admission", func() bool { f.h.mu.Lock(); defer f.h.mu.Unlock(); return f.h.inflight == 1 })
	before := runtime.NumGoroutine()
	for range 3 {
		short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := f.h.Close(short)
		cancel()
		if !errors.Is(err, ErrCleanupUnknown) {
			t.Fatalf("Close during a stalled upgrade = %v", err)
		}
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("Close left waiters: %d -> %d goroutines", before, after)
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.h.Close(ctx); err != nil {
		t.Fatalf("Close after the hijack returned = %v", err)
	}
	expectUpgradeThenClosed(t, r, req)
}

// ---- MaxTracked and unknown closes ----

func gatedRefusal(t *testing.T, h http.Handler, gate chan struct{}) *rawWS {
	t.Helper()
	r, resp := upgradeWith(t, h, func(_, server *memConn) { server.setGate(gate) }, nil)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade = %d", resp.StatusCode)
	}
	r.challenge()
	_ = r.send(true, opBinary, []byte("not a registration"))
	r.expectRefused(3 * time.Second)
	return r
}

// Live streams and closes that never confirm share MaxTracked: with both at
// four the Handler refuses new upgrades until a close is confirmed.
func TestLiveAndUnknownTransportsShareTheTrackedCap(t *testing.T) {
	f := newGatewayFixture(t, nil, Budgets{Registration: 500 * time.Millisecond, Approval: 3 * time.Second, Cleanup: 100 * time.Millisecond})
	for i := range MaxTracked - MaxPending {
		r := upgraded(t, f.h)
		r.text(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch(fmt.Sprintf("launch-%d", i)), r.challenge(), f.host1))
		r.expectAccepted()
		f.rec.next(t)
	}
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	var gated []*rawWS
	for range MaxPending {
		gated = append(gated, gatedRefusal(t, f.h, gate))
	}
	if _, resp := upgrade(t, f.h, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over MaxTracked: %d", resp.StatusCode)
	}
	release()
	eventually(t, "a confirmed close freeing a slot", func() bool {
		_, resp := upgrade(t, f.h, nil)
		return resp.StatusCode == http.StatusSwitchingProtocols
	})
	for _, r := range gated {
		if n := r.server.closeCount(); n != 1 {
			t.Fatalf("raw conn closed %d times", n)
		}
	}
}

// An unconfirmed gateway close is retained; each Close waits on the same
// attempt (no second raw close) until it completes.
func TestHandlerRetriesTheSameUnknownClose(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	r := gatedRefusal(t, f.h, gate)
	for range 2 {
		short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := f.h.Close(short)
		cancel()
		if !errors.Is(err, ErrCleanupUnknown) || r.server.closeCount() != 1 {
			t.Fatalf("Close = %v, raw closes %d", err, r.server.closeCount())
		}
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.h.Close(ctx); err != nil || r.server.closeCount() != 1 {
		t.Fatalf("Close after release = %v, raw closes %d", err, r.server.closeCount())
	}
}

// ---- U1, U2: library behavior through the real reader ----

// Exactly MaxStreamMessage passes in both directions through the real
// library reader; one byte more in a single message ends the stream.
func TestExactStreamMessageBothDirectionsAndOneMoreRefused(t *testing.T) {
	for _, dir := range []string{"gateway-to-mac", "mac-to-gateway"} {
		t.Run(dir, func(t *testing.T) {
			p := libraryPair(t, nil)
			exact := bytes.Repeat([]byte{0x5a}, MaxStreamMessage)
			roundTrip(t, p.gw, p.mac, exact)
			roundTrip(t, p.mac, p.gw, exact)
			from, to := p.gw, p.mac
			if dir == "mac-to-gateway" {
				from, to = p.mac, p.gw
			}
			over := bytes.Repeat([]byte{0xa5}, MaxStreamMessage+1)
			go func() { _, _ = from.(*stream).Conn.Write(over) }() // one message, bypassing the chunking
			if n := endsWithin(t, to, 8*time.Second); n >= len(over) {
				t.Fatalf("oversized message delivered whole (%d bytes)", n)
			}
		})
	}
}

// Stream deadlines behave as agentchannel's stall logic expects (this
// replaced websocket.NetConn, which reported a pending expiry as context
// cancellation): an expired deadline fails the next call without I/O and
// without ending the stream until it is reset, an idle expiry is cleared by
// the next deadline, and a pending read that times out reports
// os.ErrDeadlineExceeded and Timeout.
func TestStreamDeadlinesMatchTheChannelStallLogic(t *testing.T) {
	p := libraryPair(t, nil)
	_ = p.mac.SetWriteDeadline(time.Now().Add(-time.Second))
	if n, err := p.mac.Write([]byte("x")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write past its deadline = %d, %v", n, err)
	}
	_ = p.gw.SetReadDeadline(time.Now().Add(-time.Second))
	if n, err := p.gw.Read(make([]byte, 1)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past its deadline = %d, %v", n, err)
	}
	_ = p.mac.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	time.Sleep(100 * time.Millisecond)
	_ = p.mac.SetWriteDeadline(time.Now().Add(3 * time.Second))
	roundTrip(t, p.mac, p.gw, []byte("after an idle write expiry"))
	_ = p.gw.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	time.Sleep(100 * time.Millisecond)
	roundTrip(t, p.mac, p.gw, []byte("after an idle read expiry"))
	_ = p.gw.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := p.gw.Read(make([]byte, 1))
	var timeout interface{ Timeout() bool }
	if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("pending read timeout = %v", err)
	}
	if _, again := p.gw.Read(make([]byte, 1)); !errors.Is(again, os.ErrDeadlineExceeded) {
		t.Fatalf("read after a pending timeout = %v", again)
	}
}

// Cancellation by the owner and loss of the peer end pending reads promptly
// and are never reported as timeouts.
func TestStreamCancellationAndLossAreNotTimeouts(t *testing.T) {
	p := libraryPair(t, nil)
	pending := func(c net.Conn) <-chan error {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		done := make(chan error, 1)
		go func() { _, err := c.Read(make([]byte, 1)); done <- err }()
		return done
	}
	gwRead, macRead := pending(p.gw), pending(p.mac)
	time.Sleep(100 * time.Millisecond)
	if err := p.gw.Close(); err != nil {
		t.Fatalf("gateway stream Close = %v", err)
	}
	for name, ch := range map[string]<-chan error{"owner cancellation": gwRead, "peer loss": macRead} {
		select {
		case err := <-ch:
			if err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("%s reported %v", name, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not end the pending read", name)
		}
	}
}

// ---- late acquisition ----

// A dial admitted during the upgrade keeps the owner unconfirmed until it
// ends; a connection it delivers after the close is closed at once, and while
// that close is in progress the owner is not clean. A late close that fails
// stays unknown for good, and a sealed owner admits no new dial.
func TestLateAcquisitionIsNeverReportedClean(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	short := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	live, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()

	c := newCloser(func() {})
	if !c.begin() {
		t.Fatal("dial not admitted during the upgrade")
	}
	c.settle()
	if err := c.wait(short()); !errors.Is(err, ErrCleanupUnknown) || c.confirmed() {
		t.Fatalf("owner with a dial in flight = %v", err)
	}
	gate := make(chan struct{})
	a, _ := memPair()
	a.setGate(gate)
	attached := make(chan error, 1)
	go func() {
		_, err := c.attach(a)
		c.end()
		attached <- err
	}()
	eventually(t, "the late close to start", a.isClosed)
	if err := c.wait(short()); !errors.Is(err, ErrCleanupUnknown) || c.confirmed() {
		t.Fatalf("owner during a late close = %v", err)
	}
	close(gate)
	if err := <-attached; !errors.Is(err, ErrRefused) {
		t.Fatalf("late attach = %v", err)
	}
	if err := c.wait(live); err != nil || !c.confirmed() || a.closeCount() != 1 {
		t.Fatalf("after the late close = %v, closes %d", err, a.closeCount())
	}

	failed := newCloser(func() {})
	failed.settle()
	if err := failed.wait(live); err != nil {
		t.Fatal(err)
	}
	if failed.begin() {
		t.Fatal("a sealed owner admitted a dial")
	}
	b, _ := memPair()
	if _, err := failed.attach(closeErrConn{b}); !errors.Is(err, ErrRefused) {
		t.Fatalf("late attach = %v", err)
	}
	for range 3 {
		if err := failed.wait(ended); !errors.Is(err, ErrCleanupUnknown) || failed.confirmed() {
			t.Fatalf("failed late close reported %v", err)
		}
	}
	if b.closeCount() != 1 {
		t.Fatal("a failed late close was re-attempted")
	}
}

// lateDialTransport abandons its round trip at once while its dial continues
// and hands the connection over later, as net/http may do after cancellation.
type lateDialTransport struct {
	own      acquirer
	release  chan struct{}
	conn     net.Conn
	attached chan error
}

func (l *lateDialTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if !l.own.begin() {
		return nil, ErrRefused
	}
	go func() {
		<-l.release
		_, err := l.own.attach(l.conn)
		l.own.end()
		l.attached <- err
	}()
	return nil, errors.New("fixture: round trip abandoned while its dial continues")
}

func TestDialOutlivingItsRoundTripKeepsTheClientUnconfirmed(t *testing.T) {
	for _, failing := range []bool{false, true} {
		t.Run(fmt.Sprintf("late close fails=%v", failing), func(t *testing.T) {
			host := fixtureSigner(t, 0x11)
			owner := newStubOwner(host, time.Now().Add(time.Minute))
			raw, _ := memPair()
			l := &lateDialTransport{release: make(chan struct{}), conn: raw, attached: make(chan error, 1)}
			if failing {
				l.conn = closeErrConn{raw}
			}
			var once sync.Once
			release := func() { once.Do(func() { close(l.release) }) }
			t.Cleanup(release)
			cfg := clientConfig(host)
			cfg.Budgets.Cleanup = 200 * time.Millisecond
			c, err := NewClient(cfg, owner)
			if err != nil {
				t.Fatal(err)
			}
			c.transport = func(own acquirer) http.RoundTripper { l.own = own; return l }
			if err := c.Run(context.Background()); !errors.Is(err, ErrRefused) || !errors.Is(err, ErrCleanupUnknown) {
				t.Fatalf("Run with a dial in flight = %v", err)
			}
			short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := c.Close(short); !errors.Is(err, ErrCleanupUnknown) {
				t.Fatalf("Close with a dial in flight = %v", err)
			}
			release()
			if err := <-l.attached; !errors.Is(err, ErrRefused) {
				t.Fatalf("late dial attach = %v", err)
			}
			ctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel2()
			err = c.Close(ctx)
			if failing != errors.Is(err, ErrCleanupUnknown) || (!failing && err != nil) {
				t.Fatalf("Close after the late dial = %v", err)
			}
			if raw.closeCount() != 1 || owner.serves.Load() != 0 {
				t.Fatalf("late conn closed %d times", raw.closeCount())
			}
		})
	}
}

// ---- U3: real net/http.Server over a fake listener ----

type memListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMemListener() *memListener {
	return &memListener{conns: make(chan net.Conn, 8), closed: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *memListener) Addr() net.Addr { return memAddr{} }

// The real http.Server sends the 101 and hijacks through its buffered
// writer; the Handler clears the server's read/write deadlines, the stream
// outlives both the request context and those timeouts, and shutdown closes
// the Handler before draining HTTP.
func TestRealHTTPServerUpgradeOverAMemoryListener(t *testing.T) {
	ln := newMemListener()
	var mux *http.ServeMux
	reqCtx := make(chan context.Context, 1)
	returned := make(chan struct{}, 1)
	timeout := 300 * time.Millisecond
	srv := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: timeout, WriteTimeout: timeout, IdleTimeout: time.Second,
		ErrorLog: log.New(io.Discard, "", 0)}
	served := make(chan error, 1)
	p := libraryPair(t, func(m *memTransport) {
		mux = http.NewServeMux()
		h := m.handler
		mux.Handle("/agent/register", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqCtx <- r.Context()
			h.ServeHTTP(w, r)
			returned <- struct{}{}
		}))
		srv.Handler = mux
		go func() { served <- srv.Serve(ln) }()
		m.accept = func(c *memConn) { ln.conns <- c }
	})
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHTTP did not return after registration")
	}
	select {
	case <-(<-reqCtx).Done():
	case <-time.After(time.Second):
		t.Fatal("request context did not end")
	}
	time.Sleep(3 * timeout)
	roundTrip(t, p.gw, p.mac, []byte("after the server timeouts, gateway to Mac"))
	roundTrip(t, p.mac, p.gw, []byte("after the server timeouts, Mac to gateway"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.f.h.Close(ctx); err != nil {
		t.Fatalf("Handler.Close = %v", err)
	}
	endsWithin(t, p.mac, 3*time.Second)
	// The Mac side learns of the end by reading; its owner (the agent channel
	// in production) then closes the stream, which ends Run.
	if err := p.mac.Close(); err != nil {
		t.Fatalf("Mac stream Close = %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve = %v", err)
	}
	if err := await(t, p.done, 5*time.Second); !errors.Is(err, ErrClosed) || errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Run = %v", err)
	}
}
