//go:build darwin && cgo

package residentpipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"testing"
	"time"
)

// These are anonymous test-owned pipes, not a genuine producer launch.
func TestOwnedPipeCodecAndEOF(t *testing.T) {
	inputR, inputW, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer inputR.Close()
	defer inputW.Close()
	outputR, outputW, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer outputR.Close()
	defer outputW.Close()
	p := &ownedPipes{int(inputR.Fd()), int(outputW.Fd())}
	if e = unix.SetNonblock(p.in, true); e != nil {
		t.Fatal(e)
	}
	if _, e = inputW.Write([]byte(goodIntent)); e != nil {
		t.Fatal(e)
	}
	var b []byte
	got, e := p.readAvailable(&b)
	if e != nil || len(got) != 1 || got[0].RequestID != 1 {
		t.Fatal(got, e)
	}
	if e = p.writeLine([]byte(goodIntent)); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, len(goodIntent))
	n, e := outputR.Read(buf)
	if e != nil || string(buf[:n]) != goodIntent {
		t.Fatal(n, e)
	}
	_ = inputW.Close()
	if _, e = p.readAvailable(&b); e == nil {
		t.Fatal("EOF not detected")
	}
}
func TestContinuousClock(t *testing.T) {
	a, e := continuousMS()
	if e != nil {
		t.Fatal(e)
	}
	b, e := continuousMS()
	if e != nil || b < a {
		t.Fatal(a, b, e)
	}
}

func TestPipeBackpressureFailsWithoutRetry(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	p := &ownedPipes{-1, int(w.Fd())}
	if e = unix.SetNonblock(p.out, true); e != nil {
		t.Fatal(e)
	}
	block := make([]byte, MaxLine)
	// Bounded test fill. No reader is started; full pipe must fail, not block.
	failed := false
	for i := 0; i < 4096; i++ {
		if e = p.writeLine(block); e != nil {
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("test pipe did not reach backpressure")
	}
}

func TestCaptureHelper(t *testing.T) {
	if os.Getenv("RESIDENTPIPE_TEST_CAPTURE") != "1" {
		return
	}
	p, e := capturePipes()
	if e != nil {
		os.Exit(10)
	}
	for _, fd := range []int{p.in, p.out} {
		flags, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if e != nil || flags&unix.FD_CLOEXEC == 0 {
			os.Exit(11)
		}
		flags, e = unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if e != nil || flags&unix.O_NONBLOCK == 0 {
			os.Exit(12)
		}
	}
	if e = p.writeLine([]byte("captured\n")); e != nil {
		os.Exit(13)
	}
	p.close()
	os.Exit(0)
}

// Only this test binary's capture helper executes. Output joins the exact
// Child Wait and its owned I/O copiers, including error paths; the helper never
// calls run, loads policy, authenticates, connects, or starts another process.
func captureFixtureCommand(t *testing.T) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCaptureHelper$")
	c.Env = append(os.Environ(), "RESIDENTPIPE_TEST_CAPTURE=1")
	t.Cleanup(func() {
		if c.Process != nil && (c.ProcessState == nil || !c.ProcessState.Exited()) {
			t.Error("original capture fixture child was not waited")
		}
	})
	return c
}
func TestCapturedDedicatedPipes(t *testing.T) {
	c := captureFixtureCommand(t)
	c.Stdin = bytes.NewReader(nil) // exec owns the anonymous input pipe.
	b, e := c.Output()
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "captured\n" {
		t.Fatalf("unexpected %q", b)
	}
}
func TestCaptureRejectsRegularInput(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "fixture")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := captureFixtureCommand(t)
	c.Stdin = f
	_, e = c.Output()
	status, ok := e.(*exec.ExitError)
	if !ok || status.ExitCode() != 10 {
		t.Fatalf("expected FIFO refusal: %v", e)
	}
}

func TestPartialEOFIsFault(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	p := &ownedPipes{int(r.Fd()), -1}
	if e = unix.SetNonblock(p.in, true); e != nil {
		t.Fatal(e)
	}
	_, _ = w.Write([]byte("{"))
	var b []byte
	if _, e = p.readAvailable(&b); e != nil {
		t.Fatal(e)
	}
	_ = w.Close()
	if _, e = p.readAvailable(&b); e != ErrProtocol {
		t.Fatalf("partial EOF became graceful: %v", e)
	}
}

func TestUnknownObservationEndsServePolicy(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	p := &ownedPipes{-1, int(w.Fd())}
	var m mapper
	o := fixtureOff()
	o.Cleanup = "unknown"
	if e = publishObservation(&m, p, o, 10); !errors.Is(e, residentmac.ErrStandbyUnknown) {
		t.Fatal(e)
	}
	b := make([]byte, MaxLine)
	n, e := r.Read(b)
	if e != nil {
		t.Fatal(e)
	}
	var frame statusEnvelope
	if e = json.Unmarshal(b[:n], &frame); e != nil {
		t.Fatal(e)
	}
	if frame.Snapshot.Standby != "unknown" || frame.Snapshot.CanRequestOn || frame.Snapshot.Cleanup != "unknown" {
		t.Fatal(frame)
	}
}
func TestUnknownWriteFaultStillEndsServePolicy(t *testing.T) {
	p := &ownedPipes{-1, -1}
	var m mapper
	o := fixtureOff()
	o.Cleanup = "unknown"
	if e := publishObservation(&m, p, o, 10); e == nil || !m.poisoned {
		t.Fatal(e)
	}
}

func TestPartialDeadlineCannotBeErasedByCompletion(t *testing.T) {
	start := uint64(100)
	for _, tc := range []struct {
		now     uint64
		expired bool
	}{{99, true}, {1099, false}, {1100, true}, {1101, true}} {
		if assemblyExpired(&start, tc.now) != tc.expired {
			t.Fatal(tc)
		}
	}
	if assemblyExpired(nil, 1101) {
		t.Fatal("no assembly")
	}
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	p := &ownedPipes{int(r.Fd()), -1}
	if e = unix.SetNonblock(p.in, true); e != nil {
		t.Fatal(e)
	}
	b := []byte(goodIntent[:1])
	_, _ = w.Write([]byte(goodIntent[1:]))
	intents, e := p.readAvailable(&b)
	if e != nil || len(intents) != 1 || len(b) != 0 {
		t.Fatal(e)
	}
	// Completion does not erase the captured pre-read assembly timestamp.
	if !assemblyExpired(&start, 1100) {
		t.Fatal("expired completion accepted")
	}
}
