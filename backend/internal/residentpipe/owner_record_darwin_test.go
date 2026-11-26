//go:build darwin && cgo

package residentpipe

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/sys/unix"
)

// #477: an unconfirmed cleanup must survive the process that lost it. The
// record is the only evidence the next sidecar has; only a new Mac boot (all
// local children of the old run are gone) may clear it.

func fixedBoot(id string) func() (string, error) {
	return func() (string, error) { return id, nil }
}

// runIdleOnce is one ordinary sidecar lifetime through the shared path: start,
// first status, input EOF, confirmed close. It returns the first snapshot.
func runIdleOnce(t *testing.T, place recordPlace, policyPath string) Snapshot {
	t.Helper()
	coord, owner, record, err := startStandby(context.Background(), place, func(ctx context.Context) (*residentmac.Standby, error) {
		return loadBootstrap(ctx, explicitPolicyBootstrap, policyPath)
	})
	if err != nil {
		t.Fatal("start", err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
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
	done := make(chan error, 1)
	go func() { done <- runOwnedInput(context.Background(), coord, owner, record, pipes, jsonInput) }()
	reader := bufio.NewReader(outR)
	line, err := reader.ReadBytes('\n')
	var first statusEnvelope
	if err != nil || json.Unmarshal(line, &first) != nil || first.Type != "status" {
		t.Fatal("first status", string(line), err)
	}
	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}
	_, closed := readStatusesThroughClosed(t, reader)
	select {
	case err := <-done:
		if err != nil || closed.Outcome != "confirmed" {
			t.Fatal("ordinary close", err, closed.Outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary lifetime did not finish")
	}
	return first.Snapshot
}

func assertPrevious(t *testing.T, s Snapshot, want string) {
	t.Helper()
	if s.PreviousCleanup != want {
		t.Fatalf("previousCleanup = %q, want %q", s.PreviousCleanup, want)
	}
	// A previous unknown is a notice, not this run's cleanup: standby stays usable.
	if s.Cleanup != "confirmed" || s.Standby != "off" || !s.CanRequestOn {
		t.Fatalf("this run's idle state changed: cleanup=%q standby=%q canRequestOn=%v", s.Cleanup, s.Standby, s.CanRequestOn)
	}
}

// TestOwnerRecordAbandonHelper is a sidecar that ends without the stop
// procedure (as on SIGKILL or a crash): it opens the record and exits.
func TestOwnerRecordAbandonHelper(t *testing.T) {
	if os.Getenv("RESIDENTPIPE_TEST_ABANDON") != "1" {
		return
	}
	place := recordPlace{dir: os.Getenv("RESIDENTPIPE_TEST_RECORD_DIR"), bootSession: fixedBoot(os.Getenv("RESIDENTPIPE_TEST_BOOT"))}
	policyPath := os.Getenv("RESIDENTPIPE_TEST_POLICY")
	_, owner, _, err := startStandby(context.Background(), place, func(ctx context.Context) (*residentmac.Standby, error) {
		return loadBootstrap(ctx, explicitPolicyBootstrap, policyPath)
	})
	if err != nil || owner == nil {
		os.Exit(20)
	}
	os.Exit(21) // no Stop, no clean record
}

func abandonInChild(t *testing.T, dir, boot, policyPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwnerRecordAbandonHelper$")
	c.Env = append(os.Environ(), "RESIDENTPIPE_TEST_ABANDON=1", "RESIDENTPIPE_TEST_RECORD_DIR="+dir, "RESIDENTPIPE_TEST_BOOT="+boot, "RESIDENTPIPE_TEST_POLICY="+policyPath)
	err := c.Run()
	if c.ProcessState == nil || c.ProcessState.ExitCode() != 21 {
		t.Fatal("abandoning child did not open the record and exit", err)
	}
}

func TestOwnerRecordKeepsUnconfirmedCleanupAcrossRestart(t *testing.T) {
	policyPath := idleProtectedPolicy(t)
	dir := t.TempDir()
	abandonInChild(t, dir, "boot-A", policyPath)

	// The bug: the next sidecar started from "confirmed" and the loss vanished.
	assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-A")}, policyPath), "unknown")
	// An app restart, even after an ordinary confirmed close, is no evidence.
	assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-A")}, policyPath), "unknown")
	// A new Mac boot ends every local child of the abandoned run.
	assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-B")}, policyPath), "confirmed")
	assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-B")}, policyPath), "confirmed")
}

func TestOwnerRecordOrdinaryCloseRaisesNoFalseAlarm(t *testing.T) {
	policyPath := idleProtectedPolicy(t)
	dir := t.TempDir()
	// No record yet is a first start (decision ③), not a lost record.
	assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-A")}, policyPath), "confirmed")
	assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-A")}, policyPath), "confirmed")
	info, err := os.Lstat(filepath.Join(dir, "resident-owner.json"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("record is not a private regular file", info, err)
	}
}

func TestOwnerRecordUnreadableIsUnknownUntilReboot(t *testing.T) {
	policyPath := idleProtectedPolicy(t)
	for _, body := range []string{"{", `{"version":99}`, ""} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "resident-owner.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-A")}, policyPath), "unknown")
		assertPrevious(t, runIdleOnce(t, recordPlace{dir, fixedBoot("boot-B")}, policyPath), "confirmed")
	}
}

func TestOwnerRecordRefusesASecondLiveOwner(t *testing.T) {
	policyPath := idleProtectedPolicy(t)
	dir := t.TempDir()
	load := func(ctx context.Context) (*residentmac.Standby, error) {
		return loadBootstrap(ctx, explicitPolicyBootstrap, policyPath)
	}
	coord, owner, _, err := startStandby(context.Background(), recordPlace{dir, fixedBoot("boot-A")}, load)
	if err != nil || owner == nil {
		t.Fatal("first owner", err)
	}
	t.Cleanup(func() {
		if !coord.Stop().Stopped {
			t.Error("first owner cleanup")
		}
	})
	_, second, _, err := startStandby(context.Background(), recordPlace{dir, fixedBoot("boot-A")}, load)
	if err == nil || second != nil {
		t.Fatal("a second sidecar became an owner while the first holds the record")
	}
	if !owner.Snapshot().CanRequestOn {
		t.Fatal("the refused second start disturbed the first owner")
	}
}

func TestOwnerRecordThatCannotBeWrittenRefusesStandby(t *testing.T) {
	policyPath := idleProtectedPolicy(t)
	load := func(ctx context.Context) (*residentmac.Standby, error) {
		return loadBootstrap(ctx, explicitPolicyBootstrap, policyPath)
	}
	for name, place := range map[string]recordPlace{
		"missing-dir": {filepath.Join(t.TempDir(), "absent"), fixedBoot("boot-A")},
		"no-boot-id":  {t.TempDir(), func() (string, error) { return "", os.ErrNotExist }},
		"read-only":   {readOnlyDir(t), fixedBoot("boot-A")},
	} {
		t.Run(name, func(t *testing.T) {
			_, owner, _, err := startStandby(context.Background(), place, load)
			if err == nil || owner != nil {
				t.Fatal("standby started without a durable open record")
			}
		})
	}
}

func readOnlyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}
