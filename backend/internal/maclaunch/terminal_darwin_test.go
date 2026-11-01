//go:build darwin

package maclaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const fakeFD = 7

type fakeStep struct {
	ready bool
	n     int // bytes accepted by write; -1 accepts all
	data  string
	err   error
}

// fakeTTY scripts the private syscall seam. Exhausted scripts repeat their last
// step. Any syscall after close is a test failure.
type fakeTTY struct {
	t                  *testing.T
	mu                 sync.Mutex
	onWait             func(write bool, timeout time.Duration)
	waits, reads, outs []fakeStep
	waitN, readN, outN int
	timeouts           []time.Duration
	out                []byte
	closes             int
	closeErr           error
	closed, inWait     bool
}

func step(s []fakeStep, i int) fakeStep {
	if len(s) == 0 {
		return fakeStep{ready: true}
	}
	return s[min(i, len(s)-1)]
}
func (f *fakeTTY) live(fd int) {
	if fd != fakeFD || f.closed {
		f.t.Error("syscall on closed or foreign descriptor")
	}
}
func (f *fakeTTY) wait(fd int, write bool, timeout time.Duration) (bool, error) {
	f.mu.Lock()
	f.live(fd)
	f.timeouts = append(f.timeouts, timeout)
	s := step(f.waits, f.waitN)
	f.waitN++
	f.inWait = true
	f.mu.Unlock()
	if f.onWait != nil {
		f.onWait(write, timeout)
	}
	f.mu.Lock()
	f.inWait = false
	f.mu.Unlock()
	return s.ready, s.err
}
func (f *fakeTTY) read(fd int, p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live(fd)
	s := step(f.reads, f.readN)
	f.readN++
	if s.err != nil {
		return -1, s.err
	}
	if s.n != 0 {
		return s.n, nil
	}
	return copy(p, s.data), nil
}
func (f *fakeTTY) write(fd int, p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live(fd)
	s := step(f.outs, f.outN)
	f.outN++
	if s.err != nil {
		return -1, s.err
	}
	n := s.n
	if n < 0 {
		n = len(p)
	}
	if n > 0 && n <= len(p) {
		f.out = append(f.out, p[:n]...)
	}
	return n, nil
}
func (f *fakeTTY) close(fd int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live(fd)
	if f.inWait {
		f.t.Error("close raced an in-flight wait")
	}
	f.closed = true
	f.closes++
	return f.closeErr
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newFake(t *testing.T, ctx context.Context, deadline time.Duration) (*fakeTTY, *fakeClock, *localTerminal) {
	t.Helper()
	f := &fakeTTY{t: t}
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	tty := newLocalTerminal(ctx, clock.now.Add(deadline), fakeFD, ttySys{wait: f.wait, read: f.read, write: f.write, close: f.close})
	tty.now = clock.Now
	return f, clock, tty
}

func TestTerminalCancelWithoutLineConsumesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, _, tty := newFake(t, ctx, time.Minute)
	f.waits = []fakeStep{{ready: false}}
	f.onWait = func(bool, time.Duration) {
		if f.waitN == 3 {
			cancel()
		}
	}
	if _, err := tty.Read(make([]byte, 32)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.readN != 0 || f.waitN != 3 {
		t.Fatal(f.readN, f.waitN)
	}
	for _, d := range f.timeouts {
		if d <= 0 || d > terminalPoll {
			t.Fatal("unbounded wait slice", d)
		}
	}
}

func TestTerminalExpiryWhileWaitingUsesFiniteSlices(t *testing.T) {
	f, clock, tty := newFake(t, context.Background(), 250*time.Millisecond)
	f.waits = []fakeStep{{ready: false}}
	f.onWait = func(_ bool, d time.Duration) { clock.Add(d) }
	if _, err := tty.Read(make([]byte, 1)); !errors.Is(err, ErrDenied) || f.readN != 0 {
		t.Fatal(err, f.readN)
	}
	want := []time.Duration{terminalPoll, terminalPoll, 50 * time.Millisecond}
	if len(f.timeouts) != len(want) {
		t.Fatal(f.timeouts)
	}
	for i := range want {
		if f.timeouts[i] != want[i] {
			t.Fatal(f.timeouts)
		}
	}
}

func TestTerminalRefusesBeforeWaitWhenAlreadyEnded(t *testing.T) {
	f, _, tty := newFake(t, context.Background(), -time.Second)
	if _, err := tty.Read(make([]byte, 1)); !errors.Is(err, ErrDenied) || f.waitN != 0 {
		t.Fatal(err, f.waitN)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, _, tty = newFake(t, ctx, time.Minute)
	if n, err := tty.Write([]byte("prompt")); n != 0 || !errors.Is(err, context.Canceled) || f.waitN != 0 {
		t.Fatal(n, err, f.waitN)
	}
}

func TestTerminalCancelOrExpiryAfterReadinessConsumesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, _, tty := newFake(t, ctx, time.Minute)
	f.onWait = func(bool, time.Duration) { cancel() }
	if _, err := tty.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) || f.readN != 0 {
		t.Fatal(err, f.readN)
	}
	if n, err := tty.Write([]byte("prompt")); n != 0 || !errors.Is(err, context.Canceled) || f.outN != 0 {
		t.Fatal(n, err, f.outN)
	}

	f, clock, tty := newFake(t, context.Background(), time.Minute)
	f.onWait = func(bool, time.Duration) { clock.Add(time.Minute) }
	if _, err := tty.Read(make([]byte, 8)); !errors.Is(err, ErrDenied) || f.readN != 0 {
		t.Fatal(err, f.readN)
	}
}

func TestTerminalInterruptedAndSpuriousReadinessRetry(t *testing.T) {
	f, _, tty := newFake(t, context.Background(), time.Minute)
	f.waits = []fakeStep{{err: unix.EINTR}, {err: unix.EAGAIN}, {ready: true, err: unix.EINTR}, {ready: false}, {ready: true}}
	f.reads = []fakeStep{{err: unix.EINTR}, {err: unix.EAGAIN}, {data: "ok\n"}}
	p := make([]byte, 8)
	n, err := tty.Read(p)
	if err != nil || string(p[:n]) != "ok\n" {
		t.Fatal(n, err)
	}
	if f.waitN != 7 || f.readN != 3 {
		t.Fatal(f.waitN, f.readN)
	}
}

func TestTerminalPartialWritesCompleteInOrder(t *testing.T) {
	f, _, tty := newFake(t, context.Background(), time.Minute)
	f.outs = []fakeStep{{n: 3}, {err: unix.EAGAIN}, {n: 1}, {err: unix.EINTR}, {n: -1}}
	msg := []byte("Type exactly approve 0011 then Enter\n")
	if n, err := tty.Write(msg); n != len(msg) || err != nil {
		t.Fatal(n, err)
	}
	if string(f.out) != string(msg) || f.outN != 5 {
		t.Fatal(string(f.out), f.outN)
	}
}

func TestTerminalWriteStopsMidwayOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, _, tty := newFake(t, ctx, time.Minute)
	f.outs = []fakeStep{{n: 4}}
	f.onWait = func(bool, time.Duration) {
		if f.waitN == 2 {
			cancel()
		}
	}
	if n, err := tty.Write([]byte("prompt")); n != 4 || !errors.Is(err, context.Canceled) || string(f.out) != "prom" {
		t.Fatal(n, err, string(f.out))
	}
}

func TestTerminalEOFErrorsAndShortWrites(t *testing.T) {
	for name, tc := range map[string]struct {
		waits, reads []fakeStep
		want         error
	}{
		"eof":         {reads: []fakeStep{{}}, want: io.EOF},
		"read error":  {reads: []fakeStep{{err: unix.EIO}}, want: unix.EIO},
		"oversized":   {reads: []fakeStep{{n: 99}}, want: ErrDenied},
		"wait closed": {waits: []fakeStep{{err: unix.EBADF}}, want: unix.EBADF},
	} {
		t.Run(name, func(t *testing.T) {
			f, _, tty := newFake(t, context.Background(), time.Minute)
			f.waits, f.reads = tc.waits, tc.reads
			if n, err := tty.Read(make([]byte, 4)); n != 0 || !errors.Is(err, tc.want) {
				t.Fatal(n, err)
			}
		})
	}
	for name, tc := range map[string]struct {
		outs []fakeStep
		want error
	}{
		"zero":       {outs: []fakeStep{{n: 2}, {n: 0}}, want: io.ErrShortWrite},
		"oversized":  {outs: []fakeStep{{n: 2}, {n: 99}}, want: io.ErrShortWrite},
		"hangup EIO": {outs: []fakeStep{{n: 2}, {err: unix.EIO}}, want: unix.EIO},
	} {
		t.Run(name, func(t *testing.T) {
			f, _, tty := newFake(t, context.Background(), time.Minute)
			f.outs = tc.outs
			if n, err := tty.Write([]byte("abcd")); n != 2 || !errors.Is(err, tc.want) {
				t.Fatal(n, err)
			}
		})
	}
}

func TestTerminalCloseIsIdempotentAndEndsIO(t *testing.T) {
	f, _, tty := newFake(t, context.Background(), time.Minute)
	f.closeErr = unix.EINTR
	if err := tty.Close(); !errors.Is(err, unix.EINTR) {
		t.Fatal(err)
	}
	if err := tty.Close(); !errors.Is(err, unix.EINTR) || f.closes != 1 {
		t.Fatal("close retried", err, f.closes)
	}
	if _, err := tty.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := tty.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if f.waitN != 0 || f.readN != 0 || f.outN != 0 {
		t.Fatal(f.waitN, f.readN, f.outN)
	}
}

func TestTerminalConcurrentCloseWaitsForInflightIO(t *testing.T) {
	f, _, tty := newFake(t, context.Background(), time.Minute)
	f.waits = []fakeStep{{ready: false}}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	f.onWait = func(bool, time.Duration) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := tty.Read(make([]byte, 1))
		readErr <- err
	}()
	<-entered
	closeErr := make(chan error, 2)
	for range 2 {
		go func() { closeErr <- tty.Close() }()
	}
	for !tty.closing.Load() {
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-readErr; !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-closeErr; err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closes != 1 || f.readN != 0 {
		t.Fatal(f.closes, f.readN)
	}
}

func TestSelectTTYBoundsAndPipeReadiness(t *testing.T) {
	for _, fd := range []int{-1, selectLimit} {
		if ready, err := selectTTY(fd, false, time.Millisecond); ready || !errors.Is(err, ErrDenied) {
			t.Fatal(fd, ready, err)
		}
	}
	// An owned pipe exercises the fd_set and timeval glue without any terminal.
	var p [2]int
	if err := unix.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	if ready, err := selectTTY(p[0], false, -time.Millisecond); ready || !errors.Is(err, ErrDenied) {
		t.Fatal(ready, err)
	}
	if ready, err := selectTTY(p[0], false, 10*time.Millisecond); ready || err != nil {
		t.Fatal("empty pipe readable", err)
	}
	if ready, err := selectTTY(p[1], true, 10*time.Millisecond); !ready || err != nil {
		t.Fatal("pipe not writable", err)
	}
	if _, err := unix.Write(p[1], []byte("x")); err != nil {
		t.Fatal(err)
	}
	if ready, err := selectTTY(p[0], false, 10*time.Millisecond); !ready || err != nil {
		t.Fatal("pipe not readable", err)
	}
}

func TestOpenTerminalRefusesEndedContextBeforeOpening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if tty, err := openTerminal(ctx, time.Now().Add(time.Minute)); tty != nil || !errors.Is(err, ErrDenied) {
		t.Fatal(tty, err)
	}
}

func TestTerminalRepeatedRetryableSyscallsRefuseWithoutClockProgress(t *testing.T) {
	for _, mode := range []string{"wait", "read", "write"} {
		for _, errno := range []error{unix.EAGAIN, unix.EINTR} {
			t.Run(mode+errno.Error(), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				f, _, tty := newFake(t, ctx, time.Minute)
				if mode == "wait" {
					f.waits = []fakeStep{{err: errno}}
				} else if mode == "read" {
					f.reads = []fakeStep{{err: errno}}
				} else {
					f.outs = []fakeStep{{err: errno}}
				}
				var err error
				if mode == "write" {
					_, err = tty.Write([]byte("prompt"))
				} else {
					_, err = tty.Read(make([]byte, 1))
				}
				if !errors.Is(err, ErrDenied) || f.waitN > 8 || f.readN > 8 || f.outN > 8 {
					t.Fatalf("unbounded retry: err=%v wait=%d read=%d write=%d", err, f.waitN, f.readN, f.outN)
				}
			})
		}
	}
}
