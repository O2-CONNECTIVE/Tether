package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// untouched records whether the entry read or closed stdin.
type untouched struct{ read, closed bool }

func (u *untouched) Read([]byte) (int, error) {
	u.read = true
	return 0, io.EOF
}

func (u *untouched) Close() error {
	u.closed = true
	return nil
}

func TestUnconfiguredEntryRefusesBeforeAnyIO(t *testing.T) {
	dialed := false
	dial := func(context.Context) (io.ReadWriteCloser, error) {
		dialed = true
		return nil, errors.New("unexpected")
	}
	for name, tc := range map[string]struct {
		args    []string
		connect connector
	}{
		"no producer":    {nil, nil},
		"flag":           {[]string{"--endpoint=x"}, dial},
		"positional arg": {[]string{"host"}, dial},
	} {
		in, out, diag := &untouched{}, &bytes.Buffer{}, &bytes.Buffer{}
		if code := execute(context.Background(), tc.args, in, out, diag, tc.connect); code != 2 {
			t.Fatalf("%s: exit = %d", name, code)
		}
		if in.read || in.closed || out.Len() != 0 || diag.Len() == 0 || dialed {
			t.Fatalf("%s: stdin read=%v stdout=%q diagnostic=%q dialed=%v", name, in.read, out.String(), diag.String(), dialed)
		}
	}
}

func TestFailedChannelRefusesBeforeStdio(t *testing.T) {
	in, out, diag := &untouched{}, &bytes.Buffer{}, &bytes.Buffer{}
	fail := func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("no channel") }
	if code := execute(context.Background(), nil, in, out, diag, fail); code != 1 || in.read || out.Len() != 0 {
		t.Fatalf("exit=%d stdin read=%v stdout=%q", code, in.read, out.String())
	}
}

// The memory channel below stands in for the future authenticated producer;
// it is not a transport and authenticates nothing.
func TestConfiguredEntryServesStdioAndEndsTheChannel(t *testing.T) {
	local, remote := net.Pipe()
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, remote)
		close(ended)
	}()
	connect := func(context.Context) (io.ReadWriteCloser, error) { return local, nil }
	in := io.NopCloser(strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"))
	out, diag := &bytes.Buffer{}, &bytes.Buffer{}
	if code := execute(context.Background(), nil, in, out, diag, connect); code != 0 {
		t.Fatalf("exit = %d, diagnostic %q", code, diag.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"protocolVersion":"2025-06-18"`) || !strings.Contains(lines[1], `"rescue_start"`) || diag.Len() != 0 {
		t.Fatalf("stdout = %q, diagnostic = %q", out.String(), diag.String())
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent channel was not closed after stdin ended")
	}
}

// When the channel ends while stdin stays open and silent (a blocking reader
// that a Close might not wake), the entry still stops instead of answering
// later calls with a transport error: Serve closes and joins only its own pipe.
// The process-owned stdin is not closed; its pump ends at the next read. This
// uses an io.Pipe, not a real terminal or descriptor.
func TestEndedChannelStopsTheEntryWithoutWaitingForStdin(t *testing.T) {
	local, remote := net.Pipe()
	_ = remote.Close()
	inR, inW := io.Pipe()
	out, diag := &bytes.Buffer{}, &bytes.Buffer{}
	done := make(chan int, 1)
	go func() {
		done <- execute(context.Background(), nil, inR, out, diag, func(context.Context) (io.ReadWriteCloser, error) { return local, nil })
	}()
	select {
	case code := <-done:
		if code != 1 || out.Len() != 0 || !strings.Contains(diag.String(), "再開しません") {
			t.Fatalf("exit=%d stdout=%q diagnostic=%q", code, out.String(), diag.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the entry kept running after the channel ended")
	}
	// stdin is still open: the pump takes this line and then stops on its
	// closed pipe, without writing anything to stdout.
	if _, err := inW.Write([]byte("{}\n")); err != nil {
		t.Fatalf("stdin was closed by the entry: %v", err)
	}
	_ = inW.Close()
	if out.Len() != 0 {
		t.Fatalf("stdout after the end = %q", out.String())
	}
}

type heldOutput struct{ entered, release, ended chan struct{} }

func (w *heldOutput) Write(b []byte) (int, error) {
	close(w.entered)
	<-w.release
	close(w.ended)
	return len(b), nil
}
func TestBlockedStdoutCannotPreventChannelLossShutdown(t *testing.T) {
	local, remote := net.Pipe()
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}\n")
	out := &heldOutput{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- execute(context.Background(), nil, input, out, io.Discard, func(context.Context) (io.ReadWriteCloser, error) { return local, nil })
	}()
	select {
	case <-out.entered:
	case <-time.After(time.Second):
		t.Fatal("writer not reached")
	}
	_ = remote.Close()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked stdout kept MCP alive")
	}
	close(out.release)
	<-out.ended
}
func TestNilStdoutRefusesBeforeConnectingOrReading(t *testing.T) {
	in := &untouched{}
	called := false
	code := execute(context.Background(), nil, in, nil, io.Discard, func(context.Context) (io.ReadWriteCloser, error) { called = true; return nil, nil })
	if code != 2 || in.read || called {
		t.Fatal(code, in.read, called)
	}
}
