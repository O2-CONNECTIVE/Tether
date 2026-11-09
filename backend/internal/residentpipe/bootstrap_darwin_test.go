//go:build darwin && cgo

package residentpipe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
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
	}{{"json-eof", jsonInput, false}, {"foreground-close", foregroundInput, false}, {"foreground-cancel", foregroundInput, true}} {
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
