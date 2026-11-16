package guestaccount

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

type sidRunReadFunc func([]byte) (int, error)

func (f sidRunReadFunc) Read(b []byte) (int, error) { return f(b) }
func (f sidRunReadFunc) Close() error               { return nil }

type sidRunWriteFuncs struct {
	write func([]byte) (int, error)
	close func() error
}

func (w sidRunWriteFuncs) Write(b []byte) (int, error) { return w.write(b) }
func (w sidRunWriteFuncs) Close() error                { return w.close() }

type sidRunChildFuncs struct{ wait, close func(context.Context) error }

func (c sidRunChildFuncs) Wait(ctx context.Context) error  { return c.wait(ctx) }
func (c sidRunChildFuncs) Close(ctx context.Context) error { return c.close(ctx) }

type sidRunEOFReader struct{ *bytes.Reader }

func (r sidRunEOFReader) Read(b []byte) (int, error) {
	n, e := r.Reader.Read(b)
	if r.Len() == 0 {
		return n, io.EOF
	}
	return n, e
}
func (r sidRunEOFReader) Close() error { return nil }

func TestSIDRunRejectsIncompleteEOFAndCancellationDoesNotBecomeEOF(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			o, p, _ := sidRunFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			first := true
			reader := sidRunReadFunc(func(b []byte) (int, error) {
				if first {
					first = false
					return copy(b, "valid-prefix"), nil
				}
				once.Do(func() { close(entered) })
				<-release
				return 0, io.EOF
			})
			if stream == "stdout" {
				p.output = reader
			} else {
				p.stderr = reader
			}
			p.closeHook = func(cleanup context.Context) error {
				if !finite(cleanup) {
					t.Error("cancelled request context reused as cleanup budget")
				}
				close(release)
				return nil
			}
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			done := make(chan error, 1)
			go func() {
				out, e := o.execute(ctx, []byte("request\n"))
				if out != nil {
					t.Error("missing EOF returned partial stdout")
				}
				done <- e
			}()
			<-entered
			select {
			case <-done:
				t.Fatal("valid prefix counted as EOF")
			default:
			}
			cancel()
			if e := <-done; !errors.Is(e, ErrUnknown) {
				t.Fatal("cancelled synthetic EOF accepted", e)
			}
		})
	}
}

func TestSIDRunBoundsBothStreamsAndRequiresNonemptyStdout(t *testing.T) {
	for _, kind := range []string{"stdout-overflow", "stderr-overflow", "empty-stdout", "zero-progress"} {
		t.Run(kind, func(t *testing.T) {
			o, p, _ := sidRunFixture(t)
			switch kind {
			case "stdout-overflow":
				p.output = io.NopCloser(bytes.NewReader(make([]byte, sidRunByteLimit+1)))
			case "stderr-overflow":
				p.stderr = io.NopCloser(bytes.NewReader(make([]byte, sidRunByteLimit+1)))
			case "empty-stdout":
				p.output = io.NopCloser(bytes.NewReader(nil))
			case "zero-progress":
				p.output = sidRunReadFunc(func([]byte) (int, error) { return 0, nil })
			}
			if out, e := o.execute(testContext(t), []byte("request\n")); e == nil || out != nil {
				t.Fatal("invalid stream became success", e)
			}
		})
	}
}

func TestSIDRunKeepsFinalBytesWithEOFAndDoesNotStripTrailingOutput(t *testing.T) {
	o, p, _ := sidRunFixture(t)
	b := testPlan(t).binding
	b.sid = b.machine + "-1002"
	request := sidRequest(b, disabled)
	response := append(sidResult(request), []byte("trailing")...)
	p.output = sidRunEOFReader{bytes.NewReader(response)}
	out, e := o.execute(testContext(t), request)
	if e != nil || !bytes.Equal(out, response) {
		t.Fatal("EOF bytes or trailing output were lost", e)
	}
	if validSIDResult(request, out) {
		t.Fatal("trailing output became canonical response")
	}
}

func TestSIDRunShortWriteNeverSendsNormalEOFBeforeChildIsStopped(t *testing.T) {
	for _, kind := range []string{"zero", "short", "short-error", "full-error"} {
		t.Run(kind, func(t *testing.T) {
			o, p, c := sidRunFixture(t)
			closedInput := 0
			p.inputOverride = sidRunWriteFuncs{write: func(b []byte) (int, error) {
				switch kind {
				case "zero":
					return 0, nil
				case "short":
					return len(b) - 1, nil
				case "short-error":
					return len(b) - 1, io.ErrShortWrite
				}
				return len(b), ErrUnknown
			}, close: func() error { closedInput++; return nil }}
			c.closeErr = ErrUnknown
			if out, e := o.execute(testContext(t), []byte("request\n")); e == nil || out != nil {
				t.Fatal("failed write succeeded")
			}
			if closedInput != 0 || p.closed != 0 || o.pipes != p || o.child != c {
				t.Fatal("unconfirmed child received cleanup EOF or owner refs dropped", closedInput, p.closed)
			}
			// execute 内の Close が期限切れで残した child stop 試行は、故障を外す前の値を読み得て、
			// 次の Close に join される。故障を読み終えるまで待ち、fixture と同じ c.mu の下で外す。
			o.mu.Lock()
			inFlight := o.stop
			o.mu.Unlock()
			if inFlight != nil && !sidRunJoin(testContext(t), inFlight) {
				t.Fatal("child stop attempt did not finish", kind)
			}
			c.mu.Lock()
			c.closeErr = nil
			c.mu.Unlock()
			if e := o.Close(testContext(t)); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestSIDRunRetainsPartialConstructorsAndReleaseFailure(t *testing.T) {
	for _, stage := range []string{"open", "start", "release"} {
		t.Run(stage, func(t *testing.T) {
			o, p, c := sidRunFixture(t)
			starts := 0
			original := o.ops.start
			o.ops.start = func(ctx context.Context, s childjob.Spec) (sidRunChild, error) {
				starts++
				child, e := original(ctx, s)
				if stage == "start" {
					e = ErrUnknown
				}
				return child, e
			}
			if stage == "open" {
				o.ops.open = func(context.Context, childstdio.Setup) (sidRunPipes, error) { return p, ErrUnknown }
			}
			if stage == "release" {
				p.releaseErr = ErrUnknown
			}
			if out, e := o.execute(testContext(t), []byte("request\n")); e == nil || out != nil {
				t.Fatal("partial construction succeeded")
			}
			if o.pipes != p || p.closed != 1 || stage == "open" && starts != 0 || stage != "open" && (o.child != c || c.closed != 1) {
				t.Fatal("actual returned owner lost", starts, p.closed, c.closed)
			}
		})
	}
}

func TestSIDRunLateConstructorCannotAcknowledgeCloseOrContinueAcquiring(t *testing.T) {
	for _, stage := range []string{"open", "start"} {
		t.Run(stage, func(t *testing.T) {
			o, p, c := sidRunFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			starts := atomic.Int32{}
			if stage == "open" {
				o.ops.open = func(context.Context, childstdio.Setup) (sidRunPipes, error) { close(entered); <-release; return p, nil }
			}
			o.ops.start = func(context.Context, childjob.Spec) (sidRunChild, error) {
				starts.Add(1)
				if stage == "start" {
					close(entered)
					<-release
				}
				return c, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := o.execute(ctx, []byte("request\n")); done <- e }()
			<-entered
			if e := <-done; !errors.Is(e, ErrCleanup) {
				close(release)
				t.Fatal("late constructor was acknowledged", e)
			}
			if p.closed != 0 || c.closed != 0 {
				close(release)
				t.Fatal("unreturned constructor was cleaned as completed")
			}
			close(release)
			if e := o.Close(testContext(t)); e != nil {
				t.Fatal(e)
			}
			if stage == "open" && starts.Load() != 0 || p.closed != 1 || p.released != 0 {
				t.Fatal("cancelled constructor continued startup", starts.Load(), p.closed, p.released)
			}
		})
	}
}

func TestSIDRunConstructorAndWorkerPanicsNeverReturnOutput(t *testing.T) {
	for _, stage := range []string{"constructor", "read", "write", "wait"} {
		t.Run(stage, func(t *testing.T) {
			o, p, c := sidRunFixture(t)
			switch stage {
			case "constructor":
				o.ops.start = func(context.Context, childjob.Spec) (sidRunChild, error) { panic("unknown constructor owner") }
			case "read":
				p.output = sidRunReadFunc(func([]byte) (int, error) { panic("read") })
			case "write":
				p.inputOverride = sidRunWriteFuncs{write: func([]byte) (int, error) { panic("write") }, close: func() error { return nil }}
			case "wait":
				o.ops.start = func(context.Context, childjob.Spec) (sidRunChild, error) {
					return sidRunChildFuncs{wait: func(context.Context) error { panic("wait") }, close: c.Close}, nil
				}
			}
			if out, e := o.execute(testContext(t), []byte("request\n")); e == nil || out != nil {
				t.Fatal("panic returned output")
			}
			if stage == "constructor" {
				if e := o.Close(testContext(t)); !errors.Is(e, ErrCleanup) || o.pipes != p {
					t.Fatal("unknown constructor owner was acknowledged", e)
				}
			}
		})
	}
}

func TestSIDRunWaitFailureNeverTurnsPrefixIntoSuccess(t *testing.T) {
	for _, e := range []error{childjob.ErrExit, childjob.ErrUnconfirmed} {
		o, _, c := sidRunFixture(t)
		c.waitErr = e
		if out, e := o.execute(testContext(t), []byte("request\n")); e == nil || out != nil {
			t.Fatal("exit/job failure returned output")
		}
	}
}

func TestSIDRunCloseWaitsForCleanupActualReturnAndAllReaderJoins(t *testing.T) {
	for _, stage := range []string{"child-close", "pipes-close", "reader"} {
		t.Run(stage, func(t *testing.T) {
			o, p, c := sidRunFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			calls := atomic.Int32{}
			if stage == "child-close" {
				o.ops.start = func(context.Context, childjob.Spec) (sidRunChild, error) {
					return sidRunChildFuncs{wait: func(ctx context.Context) error { <-ctx.Done(); return ErrUnknown }, close: func(context.Context) error { calls.Add(1); close(entered); <-release; return nil }}, nil
				}
			}
			if stage == "pipes-close" {
				p.closeHook = func(context.Context) error { calls.Add(1); close(entered); <-release; return nil }
			}
			if stage == "reader" {
				p.output = sidRunReadFunc(func([]byte) (int, error) { calls.Add(1); close(entered); <-release; return 0, io.EOF })
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := o.execute(ctx, []byte("request\n")); done <- e }()
			<-entered
			if e := <-done; !errors.Is(e, ErrCleanup) {
				close(release)
				t.Fatal("nonreturned worker was acknowledged", e)
			}
			short, end := context.WithTimeout(context.Background(), 5*time.Millisecond)
			e := o.Close(short)
			end()
			if !errors.Is(e, ErrCleanup) {
				close(release)
				t.Fatal("live worker Close succeeded", e)
			}
			if stage == "child-close" && p.closed != 0 {
				close(release)
				t.Fatal("pipe EOF sent before physical child cleanup")
			}
			close(release)
			if e = o.Close(testContext(t)); e != nil {
				t.Fatal(e)
			}
			if calls.Load() != 1 || o.pipes != p || o.child == nil || stage == "pipes-close" && p.closed != 1 {
				t.Fatal("worker duplicated or owner references dropped", calls.Load(), p.closed, c.closed)
			}
		})
	}
}

func TestSIDRunPureConfigAndInputBoundsRefuseBeforeAcquisition(t *testing.T) {
	for _, field := range []string{"image", "invalid-utf8-image", "hash", "token", "command", "bootstrap", "handles", "cleanup", "stdio-cleanup", "user", "service"} {
		o, _, _ := sidRunFixture(t)
		config := o.config
		switch field {
		case "image":
			config.spec.Artifact.Path = `\\server\powershell.exe`
		case "invalid-utf8-image":
			config.spec.Artifact.Path = "C:\\Protected\xff\\powershell.exe"
		case "hash":
			config.spec.Artifact.SHA256 = [32]byte{}
		case "token":
			config.spec.PrimaryToken = ^uintptr(0)
		case "command":
			config.spec.Command = nil
		case "bootstrap":
			config.spec.Bootstrap = &childjob.Bootstrap{}
		case "handles":
			config.spec.Handles.Stdin = 31
		case "cleanup":
			config.spec.CleanupTimeout = 0
		case "stdio-cleanup":
			config.stdio.CleanupTimeout = 0
		case "user":
			config.stdio.UserSID = "user"
		case "service":
			config.stdio.ServiceSID = "service"
		}
		calls := 0
		ops := sidRunOps{open: func(context.Context, childstdio.Setup) (sidRunPipes, error) { calls++; return nil, ErrUnknown }, start: o.ops.start}
		if owner, e := newSIDRunOwner(config, ops); e == nil || owner != nil || calls != 0 {
			t.Fatalf("%s config was accepted or acquired resources", field)
		}
	}
	for _, input := range [][]byte{nil, make([]byte, sidRunByteLimit+1)} {
		o, _, _ := sidRunFixture(t)
		calls := 0
		o.ops.open = func(context.Context, childstdio.Setup) (sidRunPipes, error) { calls++; return nil, ErrUnknown }
		if out, e := o.execute(testContext(t), input); e == nil || out != nil || calls != 0 {
			t.Fatal("invalid input acquired resources")
		}
	}
}

func TestSIDRunInputCloseAndCleanupFailureKeepOperationUnknown(t *testing.T) {
	for _, kind := range []string{"input-close", "pipes-close", "pipes-panic"} {
		t.Run(kind, func(t *testing.T) {
			o, p, _ := sidRunFixture(t)
			if kind == "input-close" {
				p.inputOverride = sidRunWriteFuncs{write: p.input.Write, close: func() error { _ = p.input.Close(); return ErrUnknown }}
			}
			if kind == "pipes-close" {
				p.closeErr = ErrUnknown
			}
			if kind == "pipes-panic" {
				p.closeHook = func(context.Context) error { panic("unknown cleanup outcome") }
			}
			if out, e := o.execute(testContext(t), []byte("request\n")); e == nil || out != nil || o.pipes != p {
				t.Fatal("close failure returned output or dropped owner")
			}
			// execute 内の Close が期限切れで残した pipe close 試行は、故障を外す前の値を読み得て、
			// 次の Close に join される。故障を読み終えるまで待ち、fixture と同じ p.mu の下で外す。
			o.mu.Lock()
			inFlight := o.pipeClose
			o.mu.Unlock()
			if inFlight != nil && !sidRunJoin(testContext(t), inFlight) {
				t.Fatal("pipe close attempt did not finish", kind)
			}
			p.mu.Lock()
			p.closeErr = nil
			p.closeHook = nil
			p.mu.Unlock()
			if e := o.Close(testContext(t)); kind == "pipes-panic" && !errors.Is(e, ErrCleanup) || kind != "pipes-panic" && e != nil {
				t.Fatal("cleanup confirmation mismatch", kind, e)
			}
		})
	}
}

func TestSIDRunExactStreamLimitStillRequiresAndAcceptsActualEOF(t *testing.T) {
	o, p, _ := sidRunFixture(t)
	response := make([]byte, sidRunByteLimit)
	p.output = io.NopCloser(bytes.NewReader(response))
	out, e := o.execute(testContext(t), make([]byte, sidRunByteLimit))
	if e != nil || !bytes.Equal(out, response) {
		t.Fatal("exact bound with EOF failed", e)
	}
}
