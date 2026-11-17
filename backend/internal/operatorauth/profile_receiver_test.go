package operatorauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type receiverRead struct {
	data []byte
	n    uint32
	err  error
}
type receiverFixture struct {
	reads                              []receiverRead
	transfers, joined, closed, checked int
	checkFn                            func(profileReceiverBinding) error
	api                                *profileSyncFixture
}

func receiverSetup(t *testing.T, reads ...receiverRead) (context.Context, *profileReceiver, *receiverFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	f := &receiverFixture{reads: reads}
	f.api = &profileSyncFixture{transferFn: func(k profileSyncKind, h uintptr, b []byte) (uint32, error) {
		if k != profileSyncRead || h != 77 {
			t.Error("wrong pipe or operation")
		}
		if f.transfers != f.joined {
			t.Error("buffer reused before previous worker join")
		}
		if f.transfers >= len(f.reads) {
			t.Error("unexpected read")
			return 0, ErrDenied
		}
		r := f.reads[f.transfers]
		f.transfers++
		copy(b, r.data)
		return r.n, r.err
	}, closeFn: func(uintptr) error { f.joined++; return nil }}
	source := profileReceiverCheck(func(_ context.Context, b profileReceiverBinding) error {
		f.checked++
		if b.profileRead != 77 || b.launchID != strings.Repeat("a", 64) {
			t.Error("source got substituted binding")
		}
		if f.checkFn != nil {
			return f.checkFn(b)
		}
		return nil
	})
	binding := profileReceiverBinding{launchID: strings.Repeat("a", 64), profileRead: 77, child: helperBinding{pid: 1, creation: 2, tokenID: 3, childModifiedID: 4, sourceTokenID: 5, sourceModifiedID: 6, user: "S-1-5-21-3", imagePath: `C:\helper.exe`}}
	r, err := newProfileReceiver(ctx, 77, binding, source, profileReceiverOps{
		adopt: func(uintptr) error { return nil },
		start: func(c context.Context, h uintptr, b []byte) (*profileSyncOwner, error) {
			return startProfileSync(c, f.api, profileSyncRead, h, b)
		},
		close: func(uintptr) error {
			if f.transfers != f.joined {
				t.Error("endpoint closed before join")
			}
			f.closed++
			return nil
		},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, r, f
}
func receiverFrame() []byte {
	return []byte(`{"version":"1","type":"helper-profile","launchId":"` + strings.Repeat("a", 64) + `","operator":{"user":"S-1-5-21-1","authentication":"7","session":"3","integrity":"8192","kind":"2","level":"1","restricted":false,"appContainer":false},"guestUser":"S-1-5-21-2"}` + "\n")
}

func TestProfileReceiverEntryDoesNotCleanFatalSourceThread(t *testing.T) {
	ctx, r, f := receiverSetup(t)
	f.checkFn = func(profileReceiverBinding) error { return ErrFatal }
	h, _, _, _ := helperFixture()
	if err := r.run(ctx, h.ops); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	if f.closed != 0 {
		t.Fatal("fatal source triggered native cleanup")
	}
}

func TestProfileReceiverUnknownPanicNeverClaimsRecovery(t *testing.T) {
	for _, stage := range []string{"start", "close"} {
		t.Run(stage, func(t *testing.T) {
			frame := receiverFrame()
			ctx, r, _ := receiverSetup(t, receiverRead{data: frame, n: uint32(len(frame))}, receiverRead{err: errProfileSyncEOF})
			closes := 0
			if stage == "start" {
				r.ops.start = func(context.Context, uintptr, []byte) (*profileSyncOwner, error) { panic("unknown worker acquisition") }
			}
			r.ops.close = func(uintptr) error {
				closes++
				if stage == "close" {
					panic("unknown close outcome")
				}
				return nil
			}
			if _, err := r.consume(ctx); !errors.Is(err, ErrUnconfirmed) {
				t.Fatal(err)
			}
			if err := r.close(ctx); !errors.Is(err, ErrUnconfirmed) {
				t.Fatal("unknown operation claimed recovered", err)
			}
			want := 0
			if stage == "close" {
				want = 1
			}
			if closes != want || r.handle != 77 {
				t.Fatal("unknown handle reused or discarded", closes, r.handle)
			}
		})
	}
}

func TestProfileReceiverRetainsSingleBlockedClose(t *testing.T) {
	ctx, r, _ := receiverSetup(t)
	entered, finish := make(chan struct{}), make(chan struct{})
	closes := 0
	r.ops.close = func(uintptr) error { closes++; close(entered); <-finish; return nil }
	short, cancel := context.WithTimeout(ctx, 15*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- r.close(short) }()
	<-entered
	blocked := false
	select {
	case err := <-result:
		if !errors.Is(err, ErrUnconfirmed) {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		blocked = true
	}
	close(finish)
	if blocked {
		<-result
		t.Fatal("close waited beyond deadline")
	}
	if err := r.close(ctx); err != nil {
		t.Fatal(err)
	}
	if closes != 1 || r.handle != 0 {
		t.Fatal("close repeated or owner lost", closes, r.handle)
	}
}
func TestProfileReceiverConsumesOnlyAfterJoinedPipeEOFAndSourceRecheck(t *testing.T) {
	frame := receiverFrame()
	ctx, r, f := receiverSetup(t, receiverRead{data: frame[:30], n: 30}, receiverRead{data: frame[30:], n: uint32(len(frame) - 30)}, receiverRead{err: errProfileSyncEOF})
	p, err := r.consume(ctx)
	if err != nil || p.operator.authentication != 7 || f.checked != 2 || f.transfers != 3 || f.joined != 3 || f.closed != 1 {
		t.Fatalf("profile=%+v err=%v checks=%d reads=%d joins=%d closes=%d", p, err, f.checked, f.transfers, f.joined, f.closed)
	}
	if _, err = r.consume(ctx); err == nil {
		t.Fatal("profile consumed twice")
	}
}

type profileReceiverCheck func(context.Context, profileReceiverBinding) error

func (f profileReceiverCheck) check(c context.Context, b profileReceiverBinding) error {
	return f(c, b)
}

func TestProfileReceiverEntryNeverAuthenticatesBeforeBoundEOF(t *testing.T) {
	frame := receiverFrame()
	for _, tc := range []struct {
		name        string
		reads       []receiverRead
		failCheck   int
		wantSuccess bool
	}{
		{name: "valid", reads: []receiverRead{{data: frame, n: uint32(len(frame))}, {err: errProfileSyncEOF}}, wantSuccess: true},
		{name: "source before read", failCheck: 1},
		{name: "source after EOF", reads: []receiverRead{{data: frame, n: uint32(len(frame))}, {err: errProfileSyncEOF}}, failCheck: 2},
		{name: "zero is not EOF", reads: []receiverRead{{data: frame, n: uint32(len(frame))}, {}}},
		{name: "trailing bytes", reads: []receiverRead{{data: frame, n: uint32(len(frame))}, {data: []byte("x"), n: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, r, f := receiverSetup(t, tc.reads...)
			f.checkFn = func(profileReceiverBinding) error {
				if f.checked == tc.failCheck {
					return ErrDenied
				}
				return nil
			}
			h, thread, _, _ := helperFixture()
			// Match the independently framed expected operator; the observed message
			// token is supplied separately by the synthetic thread fixture.
			p, err := decodeHelperProfileFrame(frame, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			thread.projection = p.operator
			entered := 0
			ops := h.ops
			original := ops.check
			ops.check = func(c context.Context) error {
				entered++
				if f.closed != 1 || f.checked < 3 {
					t.Error("helper entered before profile EOF/source validation")
				}
				return original(c)
			}
			err = r.run(ctx, ops)
			if tc.wantSuccess {
				if err != nil || entered == 0 {
					t.Fatalf("entry=%d error=%v", entered, err)
				}
			} else if err == nil || entered != 0 {
				t.Fatalf("unverified entry=%d error=%v", entered, err)
			}
		})
	}
}
func TestProfileReceiverNativeHasNoDefaultTrustedSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := newNativeProfileReceiver(ctx, 77, profileReceiverBinding{}, nil, time.Second)
	if r != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("native unconfigured=%v %v", r, err)
	}
}

func TestProfileReceiverRejectedConsumeCannotBeRetriedForAuthentication(t *testing.T) {
	frame := receiverFrame()
	ctx, r, f := receiverSetup(t, receiverRead{data: frame, n: uint32(len(frame))}, receiverRead{err: errProfileSyncEOF})
	if _, err := r.consume(context.Background()); err == nil {
		t.Fatal("unbounded consume accepted")
	}
	if _, err := r.consume(ctx); err == nil || f.transfers != 0 {
		t.Fatalf("rejected attempt revived: err=%v reads=%d", err, f.transfers)
	}
	if err := r.close(ctx); err != nil || f.closed != 1 {
		t.Fatalf("close=%v closed=%d", err, f.closed)
	}
}

func TestProfileReceiverAdoptionFailureRetainsExactEndpointOwner(t *testing.T) {
	ctx, r, f := receiverSetup(t)
	ops := r.ops
	adoptErr := errors.New("inherit flag clear unconfirmed")
	ops.adopt = func(uintptr) error { return adoptErr }
	failed, err := newProfileReceiver(ctx, 77, r.binding, r.source, ops, time.Second)
	if failed == nil || !errors.Is(err, adoptErr) || f.closed != 0 {
		t.Fatalf("lost adopted owner=%v error=%v closes=%d", failed, err, f.closed)
	}
	if _, err = failed.consume(ctx); err == nil || f.transfers != 0 {
		t.Fatal("failed adoption authenticated")
	}
	if err = failed.close(ctx); err != nil || f.closed != 1 {
		t.Fatalf("cleanup=%v closes=%d", err, f.closed)
	}
}

func TestProfileReceiverRejectsEveryFalseEOFAndTrailingInput(t *testing.T) {
	frame := receiverFrame()
	for _, tc := range []struct {
		name string
		tail receiverRead
	}{
		{"zero successful read", receiverRead{}},
		{"generic EOF", receiverRead{err: io.EOF}},
		{"EOF joined with failure", receiverRead{err: errors.Join(errProfileSyncEOF, ErrDenied)}},
		{"broken pipe with bytes", receiverRead{data: []byte("x"), n: 1, err: errProfileSyncEOF}},
		{"other error with bytes", receiverRead{data: []byte("x"), n: 1, err: ErrDenied}},
		{"later bytes", receiverRead{data: []byte("x"), n: 1}},
		{"reported oversized count", receiverRead{n: 4097}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, r, f := receiverSetup(t, receiverRead{data: frame, n: uint32(len(frame))}, tc.tail)
			if p, err := r.consume(ctx); err == nil || p != (helperProfile{}) {
				t.Fatalf("accepted false EOF=%+v error=%v", p, err)
			}
			if f.checked != 1 || f.closed != 0 {
				t.Fatalf("false completion checks=%d closes=%d", f.checked, f.closed)
			}
			if err := r.close(ctx); err != nil || f.closed != 1 {
				t.Fatalf("close=%v closes=%d", err, f.closed)
			}
		})
	}
}

func TestProfileReceiverFrameBoundAndIndependentLaunchID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
		valid bool
	}{
		{"exact 16KiB", append(append(receiverFrame()[:len(receiverFrame())-1], bytes.Repeat([]byte(" "), 16384-len(receiverFrame()))...), '\n'), true},
		{"over 16KiB", append(append(receiverFrame()[:len(receiverFrame())-1], bytes.Repeat([]byte(" "), 16385-len(receiverFrame()))...), '\n'), false},
		{"different launch", bytes.ReplaceAll(receiverFrame(), []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("b", 64))), false},
		{"missing LF", receiverFrame()[:len(receiverFrame())-1], false},
		{"same chunk trailing bytes", append(receiverFrame(), 'x'), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads []receiverRead
			for p := tc.frame; len(p) > 0; {
				n := min(4096, len(p))
				reads = append(reads, receiverRead{data: p[:n], n: uint32(n)})
				p = p[n:]
			}
			reads = append(reads, receiverRead{err: errProfileSyncEOF})
			ctx, r, _ := receiverSetup(t, reads...)
			_, err := r.consume(ctx)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if err := r.close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProfileReceiverCancellationRetainsExactOwnerUntilCancelHandshakeJoins(t *testing.T) {
	ctx, r, f := receiverSetup(t)
	started, returnRead := make(chan struct{}), make(chan struct{})
	cancelEntered, cancelReturn := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.api.transferFn = func(profileSyncKind, uintptr, []byte) (uint32, error) {
		f.transfers++
		close(started)
		<-returnRead
		return 0, errProfileSyncEOF
	}
	f.api.cancelFn = func(uintptr) error {
		once.Do(func() { close(cancelEntered); close(returnRead) })
		<-cancelReturn
		return nil
	}
	request, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := r.consume(request); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("unjoined consume=%v", err)
	}
	exact := r.pending
	if exact == nil || r.handle != 77 || len(r.buffer) == 0 || f.closed != 0 {
		t.Fatal("pending owner or endpoint lost")
	}
	// consume may return after recording the stop but before requesting
	// cancellation (TestProfileSyncWaitExitRecordsStopBeforeReturningUnknown);
	// the bounded close of the same owner then requests it.
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	err := r.close(short)
	stop()
	select {
	case <-cancelEntered:
	case <-ctx.Done():
		t.Fatal("cancellation never requested for the pending owner")
	}
	if !errors.Is(err, ErrUnconfirmed) || r.pending != exact || r.handle != 77 || f.closed != 0 {
		t.Fatal("cancel success/return substituted for join")
	}
	close(cancelReturn)
	if err = r.close(ctx); err != nil || r.pending != nil || r.handle != 0 || r.buffer != nil || f.closed != 1 || f.joined != 1 {
		t.Fatalf("reclaim=%v closes=%d joins=%d", err, f.closed, f.joined)
	}
}
