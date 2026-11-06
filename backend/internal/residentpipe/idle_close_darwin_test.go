//go:build darwin && cgo

package residentpipe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// Load only self-created fixed-seed policy/key files through the real protected
// reader. No run/CLI/producer child, ON, handshake, work, network, or OS setting.
func idleProtectedOwner(t *testing.T) *residentmac.Standby {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cwd, ".idle-close-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Error("owned policy fixture remains", err)
		}
	})
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x59}, ed25519.SeedSize))
	defer clear(key)
	host, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5a}, ed25519.SeedSize)).Public())
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "public fixed-seed test vector")
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := pem.EncodeToMemory(block)
	defer clear(keyBytes)
	keyPath, policyPath := filepath.Join(root, "fixture-key"), filepath.Join(root, "fixture-policy.json")
	if err = os.WriteFile(keyPath, keyBytes, 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(map[string]any{
		"protocol": "rescue-mac-resident-policy/1", "policy_digest": agentexec.MacCurrentUserPolicyDigest(),
		"registration_endpoint": "wss://resident.example.test/control", "host_label": "host-one", "principal_label": "member-one",
		"principal_public_key": base64.StdEncoding.EncodeToString(principal.Marshal()), "host_key_path": keyPath,
		"host_key_fingerprint": ssh.FingerprintSHA256(host.PublicKey()), "work_dir": root, "home": root, "cleanup_seconds": 10,
		"operations": []any{map[string]any{"id": maclaunch.ResidentFixedRequestID, "command": maclaunch.ResidentFixedCommand, "timeout_ms": 15000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(policy)
	if err = os.WriteFile(policyPath, policy, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	owner, err := residentmac.LoadStandby(ctx, policyPath)
	if err != nil {
		t.Fatal("self-created protected policy refused", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := owner.Close(ctx); err != nil {
			t.Error("same idle owner cleanup", err)
		}
	})
	return owner
}

// This in-process component test never calls run or starts a producer. It joins
// the actual serve loop on real input EOF, then closes the SAME idle Standby
// before writing its terminal result. No authenticated work is exercised.
func TestIdleStandbyClosePrecedesTerminalPipeFrame(t *testing.T) {
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
	p := &ownedPipes{-1, -1}
	t.Cleanup(p.close)
	for _, spec := range []struct {
		file *os.File
		dest *int
	}{{inR, &p.in}, {outW, &p.out}} {
		fd, err := unix.FcntlInt(spec.file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			t.Fatal(err)
		}
		*spec.dest = fd
		if err := unix.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := inR.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outR.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(outR)
	done := make(chan struct{})
	var served error
	go func() { served = safeServe(owner, p); close(done) }()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("original serve loop not joined")
		}
	})
	read := func(dest any) {
		t.Helper()
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(line, dest); err != nil {
			t.Fatal(err)
		}
	}
	var status statusEnvelope
	read(&status)
	if status.Type != "status" || status.Snapshot.Standby != "off" || status.Snapshot.Registration != "disconnected" {
		t.Fatal(status)
	}
	// OFF while already idle is a rejected intent, never an ON/connection.
	intent, err := Encode(intentEnvelope{1, "intent", Intent{1, 1, status.Snapshot.Revision, "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inW.Write(intent); err != nil {
		t.Fatal(err)
	}
	var receipt receiptEnvelope
	read(&receipt)
	if receipt.Type != "receipt" || receipt.Receipt.Disposition != "rejected" {
		t.Fatal(receipt)
	}
	read(&status)
	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("input EOF did not join original serve")
	}
	if !errors.Is(served, os.ErrClosed) {
		t.Fatal(served)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := closeOnce(owner, ctx)
	if result != nil {
		t.Fatal(result)
	}
	if err := writeClosed(p, result); err != nil {
		t.Fatal(err)
	}
	var closed closedEnvelope
	read(&closed)
	if closed.Type != "closed" || closed.Outcome != "confirmed" || closed.SourceContinuousMS < status.SourceContinuousMS {
		t.Fatal(closed)
	}
	in, out := p.in, p.out
	p.close()
	for _, fd := range []int{in, out} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatal("owned descriptor remains", err)
		}
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatal("terminal frame was not followed by actual output EOF", err)
	}
	if owner.Snapshot().CanRequestOn {
		t.Fatal("closed original owner reopened admission")
	}
}

func TestUnconfirmedCloseCannotWriteConfirmedFrame(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := closeOnce(nil, ctx)
	if result == nil {
		t.Fatal("missing owner became confirmed")
	}
	if err := writeClosed(&ownedPipes{-1, int(w.Fd())}, result); err != nil {
		t.Fatal(err)
	}
	if err := r.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var closed closedEnvelope
	if err := json.Unmarshal(line, &closed); err != nil || closed.Outcome != "unknown" {
		t.Fatal(closed, err)
	}
	if err := writeClosed(&ownedPipes{-1, -1}, result); err == nil {
		t.Fatal("failed terminal write became success")
	}
}
