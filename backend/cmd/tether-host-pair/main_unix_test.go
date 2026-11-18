//go:build unix

package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecutePairLockPreventsBrowserAndNetwork(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "pair.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	browser := false
	requests := 0
	d := deps{client: &http.Client{Transport: countingTransport{calls: &requests}}, location: func(context.Context) (string, string, error) {
		return home, filepath.Join(dir, "resident-policy.json"), nil
	}, openBrowser: func(string) error { browser = true; return nil }, callbackWait: time.Millisecond, pairTimeout: time.Millisecond}
	var out, stderr strings.Builder
	if code := execute(context.Background(), []string{"--gateway", "https://gateway.test", "--issuer", "https://issuer.test", "--client-id", "x"}, &out, &stderr, d); code != 1 || browser || requests != 0 || !strings.Contains(stderr.String(), diagPairRunning) {
		t.Fatalf("status=%d browser=%v requests=%d stderr=%s", code, browser, requests, stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "pair.lock" {
		t.Fatalf("lock contention changed dir: %v %v", entries, err)
	}
}

func TestExecutePairLockOpenFailureUsesUnsafeLocationDiagnostic(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(filepath.Join(dir, "pair.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	browser := false
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) {
		return home, filepath.Join(dir, "resident-policy.json"), nil
	}, openBrowser: func(string) error { browser = true; return nil }}
	var out, stderr strings.Builder
	args := []string{"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}
	if code := execute(context.Background(), args, &out, &stderr, d); code != 1 || browser || !strings.Contains(stderr.String(), diagLocationUnsafe) {
		t.Fatalf("status=%d browser=%v stderr=%q", code, browser, stderr.String())
	}
}

func TestExecutePairLockUnexpectedFlockFailureUsesUnsafeLocationDiagnostic(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	original := flockPair
	flockPair = func(int, int) error { return os.ErrPermission }
	t.Cleanup(func() { flockPair = original })
	browser := false
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) {
		return home, filepath.Join(dir, "resident-policy.json"), nil
	}, openBrowser: func(string) error { browser = true; return nil }}
	var out, stderr strings.Builder
	args := []string{"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}
	if code := execute(context.Background(), args, &out, &stderr, d); code != 1 || browser || !strings.Contains(stderr.String(), diagLocationUnsafe) || strings.Contains(stderr.String(), diagPairRunning) {
		t.Fatalf("status=%d browser=%v stderr=%q", code, browser, stderr.String())
	}
}

type countingTransport struct{ calls *int }

func (r countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*r.calls++
	return nil, nil
}

func TestExecutePolicyRenameFailureRollsBackNewKeyAndPreservesOldBytes(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	policyPath := filepath.Join(dir, "resident-policy.json")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	oldPolicy := []byte("old policy marker bytes")
	if err := os.WriteFile(policyPath, oldPolicy, 0600); err != nil {
		t.Fatal(err)
	}
	oldKey := []byte("old key marker bytes")
	if err := os.WriteFile(filepath.Join(dir, "host-key-old"), oldKey, 0600); err != nil {
		t.Fatal(err)
	}
	originalRename := renameFile
	renameFile = func(old, new string) error {
		if new == policyPath {
			return os.ErrPermission
		}
		return os.Rename(old, new)
	}
	t.Cleanup(func() { renameFile = originalRename })
	f := newFlowFixture(t, "MATCH", 200)
	code, _, stderr, _ := invokeFlowAt(t, f, "", home)
	gotPolicy, policyErr := os.ReadFile(policyPath)
	gotKey, keyErr := os.ReadFile(filepath.Join(dir, "host-key-old"))
	entries, readErr := os.ReadDir(dir)
	if code != 1 || !strings.Contains(stderr, diagWriteFailed) || policyErr != nil || keyErr != nil || readErr != nil || !bytes.Equal(gotPolicy, oldPolicy) || !bytes.Equal(gotKey, oldKey) {
		t.Fatalf("rollback status=%d policy=%q key=%q errors=%v/%v/%v stderr=%s", code, gotPolicy, gotKey, policyErr, keyErr, readErr, stderr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pair-") || strings.HasPrefix(e.Name(), "host-key-") && e.Name() != "host-key-old" {
			t.Errorf("new artifact remains: %s", e.Name())
		}
	}
}
