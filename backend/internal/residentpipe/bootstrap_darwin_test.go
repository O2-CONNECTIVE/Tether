//go:build darwin && cgo

package residentpipe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/sys/unix"
)

func TestBootstrapSelectionIsClosed(t *testing.T) {
	for _, tc := range []struct {
		kind  bootstrapKind
		path  string
		valid bool
	}{
		{enrolledBootstrap, "", true},
		{enrolledBootstrap, "/caller/policy", false},
		{explicitPolicyBootstrap, "/fixed/policy", true},
		{explicitPolicyBootstrap, "", false},
		{explicitPolicyBootstrap, "relative", false},
		{0, "", false},
		{255, "/fixed/policy", false},
	} {
		if tc.kind.valid(tc.path) != tc.valid {
			t.Fatal("bootstrap selection mismatch", tc.kind)
		}
		if !tc.valid {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			owner, err := loadBootstrap(ctx, tc.kind, tc.path)
			cancel()
			if owner != nil || !errors.Is(err, ErrProtocol) {
				t.Fatal("invalid selection reached loader", err)
			}
			// Must reject before touching the process's real stdin/stdout.
			if err := runBootstrap(context.Background(), tc.kind, tc.path, jsonInput); !errors.Is(err, ErrProtocol) {
				t.Fatal("invalid selection captured pipes", err)
			}
		}
	}
}

func TestBootstrapRequiresFiniteLiveLoadContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	for _, input := range []context.Context{nil, context.Background(), ctx} {
		for _, kind := range []bootstrapKind{explicitPolicyBootstrap, enrolledBootstrap} {
			path := ""
			if kind == explicitPolicyBootstrap {
				path = "/not-read"
			}
			owner, err := loadBootstrap(input, kind, path)
			if owner != nil || !errors.Is(err, ErrProtocol) {
				t.Fatal("invalid context reached loader", err)
			}
		}
	}
}

func TestBootstrapPreservesDirectPolicyRejection(t *testing.T) {
	path := idleProtectedPolicy(t)
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner, err := loadBootstrap(ctx, explicitPolicyBootstrap, path)
	if owner != nil || !maclaunch.IsStartupPolicyRejected(err) {
		t.Fatal("loader wrapped or lost pre-key policy refusal", err)
	}
}

// Both adapters use the production lifetime after a real accepted protected
// synthetic policy load. No ON, network, command, enrolled account or child.
func TestBootstrapSharedLifetimeClosesOriginalIdleOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect inputDialect
		cancel  bool
		expire  bool
	}{{"json-eof", jsonInput, false, false}, {"foreground-close", foregroundInput, false, false}, {"foreground-cancel", foregroundInput, true, false},
		// The development lifetime's own expiry closes the original owner like
		// a requested close; only then may the GUI's exit-0 proof be complete.
		{"foreground-expiry", foregroundInput, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			owner := idleProtectedOwner(t)
			inR, inW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer inR.Close()
			defer inW.Close()
			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer outR.Close()
			defer outW.Close()
			pipes := &ownedPipes{int(inR.Fd()), int(outW.Fd())}
			if unix.SetNonblock(pipes.in, true) != nil || unix.SetNonblock(pipes.out, true) != nil {
				t.Fatal("pipe setup")
			}
			if err := outR.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.expire {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 200*time.Millisecond)
				defer stop()
			}
			done := make(chan error, 1)
			go func() { done <- runOwnedInput(ctx, owner, pipes, tc.dialect) }()
			joined := false
			t.Cleanup(func() {
				cancel()
				if !joined {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("original lifetime not joined")
					}
				}
			})
			reader := bufio.NewReader(outR)
			line, err := reader.ReadBytes('\n')
			var status statusEnvelope
			if err != nil || json.Unmarshal(line, &status) != nil || status.Snapshot.Standby != "off" || status.Snapshot.Registration != "disconnected" {
				t.Fatal("accepted policy did not remain idle", err)
			}
			if tc.cancel {
				cancel()
			} else if tc.expire {
				// No input: the original lifetime ends the run by itself.
			} else if tc.dialect == jsonInput {
				if err := inW.Close(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := io.WriteString(inW, "close\non 1 1\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				joined = true
				if (tc.cancel && !errors.Is(err, context.Canceled)) || (!tc.cancel && err != nil) {
					t.Fatal("shared lifetime result", err)
				}
				if tc.cancel == (ExitCode(err) == 0) {
					t.Fatal("process result", ExitCode(err))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shared lifetime did not finish")
			}
			found := false
			for i := 0; i < 8; i++ {
				line, err = reader.ReadBytes('\n')
				if err != nil {
					t.Fatal(err)
				}
				var frame closedEnvelope
				if err := json.Unmarshal(line, &frame); err != nil {
					t.Fatal(err)
				}
				if frame.Type == "closed" {
					if frame.Outcome != "confirmed" || owner.Snapshot().CanRequestOn {
						t.Fatal("terminal frame preceded original Close")
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatal("terminal frame missing")
			}
			if err := outW.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatal("missing physical output EOF", err)
			}
		})
	}
}

// The GUI reads exit 0 as one of its four collection proofs. Only the original
// lifetime's own expiry, after a confirmed close whose terminal was written,
// may end that way. A signal, an unknown close, a lost terminal or any other
// serve failure stays non-zero and so remains unconfirmed in the GUI.
func TestOwnedResultExitsCleanlyOnlyAfterConfirmedExpiry(t *testing.T) {
	expired, signalled := context.DeadlineExceeded, context.Canceled
	var live error
	for _, tc := range []struct {
		name                          string
		ended                         error
		served, closeErr, terminalErr error
		want                          int
	}{
		{"expiry with confirmed close", expired, context.DeadlineExceeded, nil, nil, 0},
		{"expiry with unknown close", expired, context.DeadlineExceeded, residentmac.ErrStandbyUnknown, nil, 1},
		{"expiry with lost terminal", expired, context.DeadlineExceeded, nil, unix.EPIPE, 1},
		{"expiry then other serve failure", expired, ErrProtocol, nil, nil, 1},
		{"wrapped deadline is not the lifetime result", expired, fmt.Errorf("read: %w", context.DeadlineExceeded), nil, nil, 1},
		{"deadline without lifetime expiry", live, context.DeadlineExceeded, nil, nil, 1},
		{"signal", signalled, context.Canceled, nil, nil, 1},
		{"input EOF", live, os.ErrClosed, nil, nil, 0},
	} {
		if got := ExitCode(ownedResult(tc.ended, tc.served, tc.closeErr, tc.terminalErr)); got != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
}
