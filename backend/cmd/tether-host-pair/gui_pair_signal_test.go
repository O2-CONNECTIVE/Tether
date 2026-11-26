//go:build unix

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGUISignalContextStopsOnInterrupt(t *testing.T) {
	// Why: SIGINT must cancel the GUI work context so the helper can run its owned Keychain cleanup.
	if os.Getenv("TETHER_GUI_SIGNAL_TEST_CHILD") == "1" {
		ctx, stop := guiSignalContext()
		defer stop()
		fmt.Fprintln(os.Stdout, "ready")
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stdout, "cancelled")
		case <-time.After(4 * time.Second):
			t.Fatal("GUI context did not observe SIGINT")
		}
		return
	}

	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGUISignalContextStopsOnInterrupt$")
			command.Env = append(os.Environ(), "TETHER_GUI_SIGNAL_TEST_CHILD=1")
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(stdout)
			if line, err := reader.ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatalf("child readiness = %q, err=%v", line, err)
			}
			if err := command.Process.Signal(signal); err != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatal(err)
			}
			if line, err := reader.ReadString('\n'); err != nil || strings.TrimSpace(line) != "cancelled" {
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatalf("child cancellation = %q, err=%v", line, err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("signal child failed: %v", err)
			}
		})
	}
}
