//go:build darwin

package main

import (
	"bytes"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The helper never calls execute/main/nativeDeps: no agent, keys, grant or commands.
func TestStatusPipeChild(t *testing.T) {
	mode := os.Getenv("TETHER_STATUS_PIPE_FIXTURE")
	if mode == "" {
		return
	}
	sink, err := openStatusSink()
	if err != nil {
		os.Exit(31)
	}
	p := sink.(*pipeStatusSink)
	flags, err := unix.FcntlInt(uintptr(p.fd), unix.F_GETFD, 0)
	if err != nil || p.fd < 3 || flags&unix.FD_CLOEXEC == 0 {
		os.Exit(32)
	}
	var ready [1]byte
	if _, err := io.ReadFull(os.Stdin, ready[:]); err != nil {
		os.Exit(33)
	}
	refused := false
	for i := 0; i < 65536; i++ {
		if !sink.writeFrame(bytes.Repeat([]byte("x"), 2048)) {
			refused = true
			break
		}
		if mode == "closed" {
			break
		}
	}
	sink.close()
	if !refused {
		os.Exit(34)
	}
	os.Stderr.WriteString("SYNTHETIC_CLEANUP_REACHED\n")
	os.Exit(0)
}
func TestStatusOwnedPipeReaderLossAndFull(t *testing.T) {
	for _, mode := range []string{"closed", "full"} {
		t.Run(mode, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			rd, wr, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			inputRead, inputWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestStatusPipeChild$")
			cmd.Env = []string{"TETHER_STATUS_PIPE_FIXTURE=" + mode}
			cmd.Stdout = wr
			cmd.Stdin = inputRead
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if err := cmd.Start(); err != nil {
				rd.Close()
				wr.Close()
				inputRead.Close()
				inputWrite.Close()
				t.Fatal(err)
			}
			wr.Close()
			inputRead.Close()
			if mode == "closed" {
				rd.Close()
			}
			inputWrite.Write([]byte{1})
			inputWrite.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Error("synthetic child exceeded observation budget; retaining owner until exit")
				err = <-done
			}
			rd.Close()
			if err != nil || diagnostic.String() != "SYNTHETIC_CLEANUP_REACHED\n" {
				t.Fatalf("fixture failure %v marker=%t", err, diagnostic.String() == "SYNTHETIC_CLEANUP_REACHED\n")
			}
		})
	}
}
