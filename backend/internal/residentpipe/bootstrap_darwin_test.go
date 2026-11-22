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

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostlifecycle"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/sys/unix"
)

type panicCloseResource struct{}

func (panicCloseResource) Close(context.Context) error { panic("synthetic close panic") }

type capturedStatus struct {
	status statusEnvelope
	line   []byte
}

func isTerminalUnknownCleanupStatus(status statusEnvelope) bool {
	return status.Type == "status" && status.Snapshot.Cleanup == "unknown" && status.Snapshot.Registration == "unknown" && !status.Snapshot.CanRequestOn && !status.Snapshot.CanRequestOff
}

func readStatusesThroughClosed(t *testing.T, reader *bufio.Reader, prefix ...capturedStatus) ([]capturedStatus, closedEnvelope) {
	t.Helper()
	statuses := append([]capturedStatus(nil), prefix...)
	for {
		line, err := reader.ReadBytes('\n')
		var header struct {
			Type string `json:"type"`
		}
		if err != nil || json.Unmarshal(line, &header) != nil {
			t.Fatal("invalid output before closed", string(line), err)
		}
		switch header.Type {
		case "status":
			var status statusEnvelope
			if json.Unmarshal(line, &status) != nil {
				t.Fatal("invalid status before closed", string(line))
			}
			statuses = append(statuses, capturedStatus{status, line})
		case "closed":
			var closed closedEnvelope
			if json.Unmarshal(line, &closed) != nil {
				t.Fatal("invalid closed frame", string(line))
			}
			return statuses, closed
		default:
			t.Fatal("unexpected frame before closed", header.Type, string(line))
		}
	}
}

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
			policyPath := idleProtectedPolicy(t)
			coord, owner, err := startStandby(context.Background(), func(ctx context.Context) (*residentmac.Standby, error) {
				return loadBootstrap(ctx, explicitPolicyBootstrap, policyPath)
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := owner.Close(ctx); err != nil {
					t.Error("same idle owner cleanup", err)
				}
			})
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
			go func() { done <- runOwnedInput(ctx, coord, owner, pipes, tc.dialect) }()
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
			} else if _, err := io.WriteString(inW, "close\non 1 1 2\n"); err != nil {
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
			if owner.Snapshot().CanRequestOn {
				t.Fatal("original Standby still accepts requests")
			}
			statuses, frame := readStatusesThroughClosed(t, reader, capturedStatus{status, line})
			for _, observed := range statuses {
				if observed.status.Snapshot.Cleanup == "unknown" {
					t.Fatal("confirmed cleanup emitted an unknown-cleanup status", string(observed.line))
				}
			}
			if frame.Type != "closed" || frame.Outcome != "confirmed" || owner.Snapshot().CanRequestOn {
				t.Fatal("confirmed cleanup lost its terminal frame", frame)
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

func TestServeLoopForcedHeartbeatBeforeEOFIsOrdinaryStatus(t *testing.T) {
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
	heartbeat := make(chan time.Time, 1)
	heartbeat <- time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	m := mapper{}
	go func() { done <- serveLoop(ctx, owner, pipes, jsonInput, heartbeat, &m) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("heartbeat control loop not joined")
			}
		}
	})
	reader := bufio.NewReader(outR)
	readStatus := func() capturedStatus {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		var status statusEnvelope
		if err != nil || json.Unmarshal(line, &status) != nil || status.Type != "status" {
			t.Fatal("invalid heartbeat status", string(line), err)
		}
		return capturedStatus{status, line}
	}
	initial := readStatus()
	heartbeatStatus := readStatus()
	if len(heartbeat) != 0 || heartbeatStatus.status.Snapshot.Revision <= initial.status.Snapshot.Revision || isTerminalUnknownCleanupStatus(heartbeatStatus.status) {
		t.Fatal("forced heartbeat was not an ordinary status before EOF", heartbeatStatus.status)
	}
	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, os.ErrClosed) {
			t.Fatal("input EOF was not observed after heartbeat", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("control loop did not observe input EOF")
	}
}

func TestBootstrapSharedLifetimeReportsCoordinatorCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect inputDialect
	}{{"json", jsonInput}, {"foreground", foregroundInput}} {
		t.Run(tc.name, func(t *testing.T) {
			policyPath := idleProtectedPolicy(t)
			coord, owner, err := startStandby(context.Background(), func(ctx context.Context) (*residentmac.Standby, error) {
				return loadBootstrap(ctx, explicitPolicyBootstrap, policyPath)
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := owner.Close(ctx); err != nil {
					t.Error("same idle owner cleanup", err)
				}
			})
			if err := coord.Start(context.Background(), hostlifecycle.Step{Name: "later", Acquire: func(context.Context) (hostlifecycle.Resource, error) {
				return panicCloseResource{}, nil
			}}); err != nil {
				t.Fatal(err)
			}
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
			go func() { done <- runOwnedInput(ctx, coord, owner, pipes, tc.dialect) }()
			reader := bufio.NewReader(outR)
			line, err := reader.ReadBytes('\n')
			var status statusEnvelope
			if err != nil || json.Unmarshal(line, &status) != nil || status.Snapshot.Standby != "off" {
				t.Fatal("accepted policy did not remain idle", err)
			}
			if err := inW.Close(); err != nil {
				t.Fatal(err)
			}
			statuses, closed := readStatusesThroughClosed(t, reader, capturedStatus{status, line})
			terminalCount := 0
			var terminal capturedStatus
			for _, observed := range statuses {
				if observed.status.Snapshot.Cleanup != "unknown" {
					continue
				}
				terminalCount++
				terminal = observed
				if !isTerminalUnknownCleanupStatus(observed.status) {
					t.Fatal("unknown cleanup status had invalid terminal fields", string(observed.line))
				}
			}
			if terminalCount != 1 {
				t.Fatalf("unknown cleanup status count = %d, want 1", terminalCount)
			}
			if len(statuses) == 0 || !isTerminalUnknownCleanupStatus(statuses[len(statuses)-1].status) {
				t.Fatal("closed was not immediately preceded by the terminal status")
			}
			if terminal.status.Snapshot.Standby != "unknown" || terminal.status.Snapshot.Work != "unknown" || terminal.status.Snapshot.Revision <= status.Snapshot.Revision {
				t.Fatal("unknown cleanup status missing or invalid before closed", string(terminal.line))
			}
			var statusJSON map[string]json.RawMessage
			var snapshotJSON map[string]json.RawMessage
			if json.Unmarshal(terminal.line, &statusJSON) != nil || json.Unmarshal(statusJSON["snapshot"], &snapshotJSON) != nil {
				t.Fatal("invalid terminal status shape", string(terminal.line))
			}
			for _, key := range []string{"standbyUntil", "standbyEndingSoon", "standbyExpired"} {
				if _, ok := snapshotJSON[key]; !ok {
					t.Fatal("terminal status missed standby field", key, string(terminal.line))
				}
			}
			if closed.Type != "closed" || closed.Outcome != "unknown" {
				t.Fatal("coordinator cleanup failure became confirmed", closed)
			}
			if owner.Snapshot().CanRequestOn {
				t.Fatal("coordinator did not close the original Standby")
			}
			if result := coord.Stop(); result.Stopped || len(result.NeedsCleanup) != 1 || result.NeedsCleanup[0] != "later" {
				t.Fatal("coordinator did not retain the failing resource", result)
			}
			select {
			case err := <-done:
				t.Fatal("unknown cleanup returned from runOwnedInput", err)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

// The GUI reads exit 0 as one of its four collection proofs. Only a requested
// close or input EOF, after a confirmed close whose terminal was written, may
// end that way. No lifetime has a deadline any more (#399 replaced the
// development host's own 15 minutes with the end of standby, which turns
// standby off without ending the process), so a deadline is never success.
// A signal, an unknown close, a lost terminal or any other serve failure stays
// non-zero and so remains unconfirmed in the GUI.
func TestOwnedResultExitsCleanlyOnlyAfterConfirmedClose(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		served, closeErr, terminalErr error
		want                          int
	}{
		{"requested close", nil, nil, nil, 0},
		{"input EOF", os.ErrClosed, nil, nil, 0},
		{"input EOF with unknown close", os.ErrClosed, residentmac.ErrStandbyUnknown, nil, 1},
		{"input EOF with lost terminal", os.ErrClosed, nil, unix.EPIPE, 1},
		{"deadline", context.DeadlineExceeded, nil, nil, 1},
		{"wrapped deadline", fmt.Errorf("read: %w", context.DeadlineExceeded), nil, nil, 1},
		{"signal", context.Canceled, nil, nil, 1},
		{"protocol", ErrProtocol, nil, nil, 1},
	} {
		if got := ExitCode(ownedResult(tc.served, tc.closeErr, tc.terminalErr)); got != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A load that cannot be confirmed as taken back (a panic may have acquired
// something) keeps this process and its coordinator, like an unconfirmed close.
// It never returns a plain failure that would end the process.
func TestStartStandbyRetainsAnUnconfirmedLoad(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, _, err := startStandby(context.Background(), func(context.Context) (*residentmac.Standby, error) { panic("load panicked") })
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatal("unconfirmed load returned", err)
	case <-time.After(300 * time.Millisecond):
	}
}

// A clean load failure has nothing to take back. It returns the loader's own
// error, so the startup-rejection receipt still sees its concrete type.
func TestStartStandbyReturnsACleanLoadFailure(t *testing.T) {
	refused := errors.New("load refused")
	_, owner, err := startStandby(context.Background(), func(context.Context) (*residentmac.Standby, error) { return nil, refused })
	if err != refused || owner != nil {
		t.Fatal(err, owner)
	}
}
