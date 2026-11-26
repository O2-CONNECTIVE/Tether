package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostkeychain"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

func TestProductionDepsProvidesGUIDependencies(t *testing.T) {
	// Why: the shipped zero-argument GUI path must have the same required dependencies as the tested fixture.
	d := productionDeps()
	for _, dependency := range []struct {
		name    string
		present bool
	}{
		{"client", d.client != nil}, {"guiClient", d.guiClient != nil}, {"location", d.location != nil},
		{"readProtected", d.readProtected != nil}, {"openBrowser", d.openBrowser != nil},
		{"storeHostKey", d.storeHostKey != nil}, {"loadHostKey", d.loadHostKey != nil},
		{"deleteHostKey", d.deleteHostKey != nil}, {"runAppleScript", d.runAppleScript != nil},
	} {
		if !dependency.present {
			t.Errorf("production GUI dependency %s is nil", dependency.name)
		}
	}
	if d.storeHostKey == nil || d.loadHostKey == nil || d.deleteHostKey == nil {
		t.FailNow()
	}
	for _, binding := range []struct {
		name string
		got  any
		want any
	}{
		{"storeHostKey", d.storeHostKey, hostkeychain.Store},
		{"loadHostKey", d.loadHostKey, hostkeychain.Load},
		{"deleteHostKey", d.deleteHostKey, hostkeychain.Delete},
	} {
		if reflect.ValueOf(binding.got).Pointer() != reflect.ValueOf(binding.want).Pointer() {
			t.Errorf("%s is not wired to hostkeychain", binding.name)
		}
	}
	if !guiPairUsesRealKeychain() {
		private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x67}, ed25519.SeedSize))
		signer, err := ssh.NewSignerFromKey(private)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.storeHostKey(private, ssh.FingerprintSHA256(signer.PublicKey())); err != hostkeychain.ErrUnsupported {
			t.Fatalf("non-Darwin Keychain store error = %v, want ErrUnsupported", err)
		}
	}
}

func TestGUINativePromptMapsUserCancelToCancelResult(t *testing.T) {
	// Why: osascript exits with error -128 for the cancel button and Escape, before a normal button return is produced.
	for _, fragment := range []string{"try", "on error number -128", `return "cancel"`, "end try"} {
		if !strings.Contains(guiNativePromptScript, fragment) {
			t.Errorf("fixed AppleScript does not handle user cancellation: missing %q", fragment)
		}
	}
}

func TestGUISuccessKeepsNewPolicyWhenOldKeyDeletionFails(t *testing.T) {
	// Why: the gateway has committed the new key, so failure to retire the old key cannot undo the new policy.
	dir := t.TempDir()
	home := t.TempDir()
	policyPath := filepath.Join(dir, "resident-policy.json")
	oldSigner := mustSignerFromSeed(t, 0x68)
	newSigner := mustSignerFromSeed(t, 0x69)
	oldFingerprint := ssh.FingerprintSHA256(oldSigner.PublicKey())
	newFingerprint := ssh.FingerprintSHA256(newSigner.PublicKey())
	oldPolicy, err := json.Marshal(map[string]string{
		"host_key_store": "keychain", "host_key_fingerprint": oldFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(dir, guiPairingPendingName)
	pending, err := json.Marshal(guiPairPending{
		Gateway: "https://gateway.example.test", Operation: strings.Repeat("A", 43), HostKeyFingerprint: newFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingPath, pending, 0600); err != nil {
		t.Fatal(err)
	}
	deleteCalls := 0
	result := completeGUIResult(guiResultSucceeded, pairResult{
		RegistrationEndpoint: "wss://gateway.example.test/register",
		HostLabel:            "new-host",
		PrincipalLabel:       "new-member",
		PrincipalPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(newSigner.PublicKey()))),
	}, guiPairPending{HostKeyFingerprint: newFingerprint}, pendingPath, dir, home, policyPath, oldPolicy, newSigner, deps{
		deleteHostKey: func(fingerprint string) error {
			deleteCalls++
			if fingerprint != oldFingerprint {
				t.Fatalf("deleted fingerprint %q, want old key %q", fingerprint, oldFingerprint)
			}
			return errors.New("synthetic Keychain cleanup failure")
		},
	})
	if result != guiResultSucceeded || deleteCalls != 1 {
		t.Fatalf("result=%q old-key delete calls=%d, want succeeded and one cleanup attempt", result, deleteCalls)
	}
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := maclaunch.ValidateFixedResidentPolicy(raw, policyPath); err != nil {
		t.Fatalf("new policy is invalid: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["host_key_store"] != "keychain" || got["host_key_fingerprint"] != newFingerprint || got["host_key_path"] != nil {
		t.Fatalf("policy does not point at the new Keychain key: %v", got)
	}
	if _, err := os.Lstat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending record was not removed: %v", err)
	}
}

func seedGUIPendingKey(t *testing.T, f *guiPairFixture, seed byte) string {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	if err := f.keychain.store(private, fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func validGUIPendingOperation() string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))
}

func writeGUIPendingRecord(t *testing.T, f *guiPairFixture, raw []byte) string {
	t.Helper()
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dir, guiPairingPendingName)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertGUIFreshRegistration(t *testing.T, f *guiPairFixture, code int, stdout, stderr, staleFingerprint string) {
	t.Helper()
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" ||
		f.authLaunches.Load() != 1 || f.operationsCalls.Load() != 1 || f.nativeCalls.Load() != 1 ||
		f.consentLaunches.Load() != 1 || f.keychain.count() != 1 {
		t.Fatalf("fresh registration did not complete: code=%d stdout=%q stderr=%q auth=%d operations=%d native=%d consent=%d keys=%d",
			code, stdout, stderr, f.authLaunches.Load(), f.operationsCalls.Load(), f.nativeCalls.Load(), f.consentLaunches.Load(), f.keychain.count())
	}
	if staleFingerprint != "" && f.keychain.has(staleFingerprint) {
		t.Fatalf("stale pending Keychain item remains: %s", staleFingerprint)
	}
	if _, err := os.Lstat(filepath.Join(f.dir, guiPairingPendingName)); !os.IsNotExist(err) {
		t.Fatalf("pending record remains: %v", err)
	}
	raw, err := os.ReadFile(f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := maclaunch.ValidateFixedResidentPolicy(raw, f.policy); err != nil {
		t.Fatalf("fresh policy is invalid: %v", err)
	}
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if policy["host_key_store"] != "keychain" || policy["host_key_fingerprint"] == "" || policy["host_key_fingerprint"] == staleFingerprint {
		t.Fatalf("fresh policy does not use the new Keychain key: %v", policy)
	}
}

func TestGUIPendingRecordsThatCannotMatchStartFreshPair(t *testing.T) {
	// Why: stale or malformed pending metadata must not permanently block a new, explicitly confirmed registration.
	cases := []struct {
		name               string
		includeFingerprint bool
		mutate             func(map[string]string)
	}{
		{name: "invalid operation", includeFingerprint: true, mutate: func(record map[string]string) { record["operation"] = "invalid" }},
		{name: "missing operation", includeFingerprint: true, mutate: func(record map[string]string) { delete(record, "operation") }},
		{name: "different gateway", includeFingerprint: true, mutate: func(record map[string]string) { record["gateway"] = "https://different.example.test" }},
		{name: "missing fingerprint", mutate: func(record map[string]string) { delete(record, "host_key_fingerprint") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGUIPairFixture(t, nil)
			record := map[string]string{"gateway": f.gateway.URL, "operation": validGUIPendingOperation()}
			staleFingerprint := ""
			if tc.includeFingerprint {
				staleFingerprint = seedGUIPendingKey(t, f, 0x71)
				record["host_key_fingerprint"] = staleFingerprint
			}
			tc.mutate(record)
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			writeGUIPendingRecord(t, f, raw)
			code, stdout, stderr := f.run(t)
			assertGUIFreshRegistration(t, f, code, stdout, stderr, staleFingerprint)
		})
	}
}

func TestGUIPendingMissingKeyRestartsFreshPair(t *testing.T) {
	// Why: an expired or externally removed Keychain item must be recoverable through a fresh consent flow.
	f := newGUIPairFixture(t, nil)
	missingKey := mustSignerFromSeed(t, 0x72)
	fingerprint := ssh.FingerprintSHA256(missingKey.PublicKey())
	raw, err := json.Marshal(guiPairPending{Gateway: f.gateway.URL, Operation: validGUIPendingOperation(), HostKeyFingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	writeGUIPendingRecord(t, f, raw)
	deleteCalls := 0
	f.depsOverride = func(d *deps) {
		deleteHostKey := d.deleteHostKey
		d.deleteHostKey = func(got string) error {
			if got == fingerprint {
				deleteCalls++
				return hostkeychain.ErrNotFound
			}
			return deleteHostKey(got)
		}
	}
	code, stdout, stderr := f.run(t)
	assertGUIFreshRegistration(t, f, code, stdout, stderr, fingerprint)
	if deleteCalls != 1 {
		t.Fatalf("missing pending key cleanup calls=%d, want 1", deleteCalls)
	}
}

func TestGUIPendingFingerprintMismatchStartsFreshPair(t *testing.T) {
	// Why: a Keychain signer that does not match its pending account cannot safely resume that gateway operation.
	f := newGUIPairFixture(t, nil)
	fingerprint := seedGUIPendingKey(t, f, 0x73)
	raw, err := json.Marshal(guiPairPending{Gateway: f.gateway.URL, Operation: validGUIPendingOperation(), HostKeyFingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	writeGUIPendingRecord(t, f, raw)
	mismatchedSigner := mustSignerFromSeed(t, 0x74)
	f.depsOverride = func(d *deps) {
		loadHostKey := d.loadHostKey
		d.loadHostKey = func(got string) (ssh.Signer, error) {
			if got == fingerprint {
				return mismatchedSigner, nil
			}
			return loadHostKey(got)
		}
	}
	code, stdout, stderr := f.run(t)
	assertGUIFreshRegistration(t, f, code, stdout, stderr, fingerprint)
}

func TestGUIPendingKeychainFailureKeepsRecord(t *testing.T) {
	// Why: a temporary Keychain error must remain unavailable without discarding recoverable pending state.
	f := newGUIPairFixture(t, nil)
	fingerprint := seedGUIPendingKey(t, f, 0x75)
	raw, err := json.Marshal(guiPairPending{Gateway: f.gateway.URL, Operation: validGUIPendingOperation(), HostKeyFingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := writeGUIPendingRecord(t, f, raw)
	deleteCalls := 0
	f.depsOverride = func(d *deps) {
		d.loadHostKey = func(string) (ssh.Signer, error) { return nil, hostkeychain.ErrUnavailable }
		deleteHostKey := d.deleteHostKey
		d.deleteHostKey = func(got string) error {
			deleteCalls++
			return deleteHostKey(got)
		}
	}
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultUnavailable || stderr != "" || deleteCalls != 0 ||
		!f.keychain.has(fingerprint) || f.authLaunches.Load() != 0 {
		t.Fatalf("transient Keychain failure discarded state or started work: code=%d stdout=%q stderr=%q deletes=%d key=%v auth=%d",
			code, stdout, stderr, deleteCalls, f.keychain.has(fingerprint), f.authLaunches.Load())
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("pending record was not preserved: %v", err)
	}
}

func TestGUIPendingProtectedReadFailureKeepsRecord(t *testing.T) {
	// Why: a protected-file read failure can be an ownership or permission problem and must never trigger recovery deletion.
	f := newGUIPairFixture(t, nil)
	fingerprint := seedGUIPendingKey(t, f, 0x76)
	raw, err := json.Marshal(guiPairPending{Gateway: f.gateway.URL, Operation: validGUIPendingOperation(), HostKeyFingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := writeGUIPendingRecord(t, f, raw)
	deleteCalls := 0
	f.depsOverride = func(d *deps) {
		readProtected := d.readProtected
		d.readProtected = func(ctx context.Context, path string) ([]byte, error) {
			if path == pendingPath {
				return nil, errors.New("synthetic protected read failure")
			}
			return readProtected(ctx, path)
		}
		deleteHostKey := d.deleteHostKey
		d.deleteHostKey = func(got string) error {
			deleteCalls++
			return deleteHostKey(got)
		}
	}
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultUnavailable || stderr != "" || deleteCalls != 0 ||
		!f.keychain.has(fingerprint) || f.authLaunches.Load() != 0 {
		t.Fatalf("protected read failure discarded state or started work: code=%d stdout=%q stderr=%q deletes=%d key=%v auth=%d",
			code, stdout, stderr, deleteCalls, f.keychain.has(fingerprint), f.authLaunches.Load())
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("pending record was not preserved: %v", err)
	}
}
