//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostkeychain"
	"golang.org/x/crypto/ssh"
)

func TestGUIPendingFingerprintLoadErrorStartsFreshPair(t *testing.T) {
	// Why: hostkeychain.ErrFingerprint means the pending account no longer owns a usable key and should recover like ErrNotFound.
	f := newGUIPairFixture(t, nil)
	fingerprint := seedGUIPendingKey(t, f, 0x51)
	writeGUIPendingJSON(t, f, f.gateway.URL, validGUIPendingOperation(), fingerprint)
	f.depsOverride = func(d *deps) {
		loadHostKey := d.loadHostKey
		d.loadHostKey = func(got string) (ssh.Signer, error) {
			if got == fingerprint {
				return nil, hostkeychain.ErrFingerprint
			}
			return loadHostKey(got)
		}
	}
	code, stdout, stderr := f.run(t)
	assertGUIFreshRegistration(t, f, code, stdout, stderr, fingerprint)
}

func TestGUIPendingDiscardPreservesKeyUsedByCurrentPolicy(t *testing.T) {
	// Why: a stale pending record may name the key already installed by the current policy, which must remain available.
	f := newGUIPairFixture(t, nil)
	fingerprint := seedGUIPendingKey(t, f, 0x52)
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(map[string]string{"host_key_fingerprint": fingerprint, "principal_label": "member", "host_label": "host"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.policy, policy, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"gateway": f.gateway.URL, "operation": validGUIPendingOperation(), "host_key_fingerprint": fingerprint, "unknown": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := writeGUIPendingRecord(t, f, raw)
	f.depsOverride = func(d *deps) {
		d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("synthetic discovery failure")
		})}
	}
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultUnavailable || !f.keychain.has(fingerprint) {
		t.Fatalf("code=%d result=%q current policy key kept=%v", code, guiResult(t, stdout), f.keychain.has(fingerprint))
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("invalid pending file was not removed: %v", err)
	}
}

func TestGUIOAuthCallbackTimeoutIsCancelled(t *testing.T) {
	// Why: the user may leave the OAuth callback untouched; that wait is cancellation rather than a gateway rejection.
	f := newGUIPairFixture(t, nil)
	f.depsOverride = func(d *deps) {
		d.callbackWait = 20 * time.Millisecond
		d.openBrowser = func(string) error { return nil }
	}
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultCancelled || stderr != "" || f.keychain.count() != 0 {
		t.Fatalf("code=%d result=%q stderr=%q keys=%d", code, guiResult(t, stdout), stderr, f.keychain.count())
	}
}

func TestGUICancelledContextCleansNewKeyDuringOAuth(t *testing.T) {
	// Why: an OS signal propagated into the GUI context must remove a new key when no gateway operation has started.
	f := newGUIPairFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	f.depsOverride = func(d *deps) {
		d.openBrowser = func(string) error {
			cancel()
			return nil
		}
	}
	code, stdout, stderr := f.runWithContext(t, ctx)
	if code != 0 || guiResult(t, stdout) != guiResultCancelled || stderr != "" || f.keychain.count() != 0 || f.operationsCalls.Load() != 0 {
		t.Fatalf("code=%d result=%q stderr=%q keys=%d operations=%d", code, guiResult(t, stdout), stderr, f.keychain.count(), f.operationsCalls.Load())
	}
}

func TestGUINativePromptExpiryIsRejected(t *testing.T) {
	// Why: a native confirmation left open through the operation expiry is a terminal rejection and its new key is removed.
	f := newGUIPairFixture(t, nil)
	var dialogCalls atomic.Int32
	f.depsOverride = func(d *deps) {
		base := f.client.Transport
		d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			response, err := base.RoundTrip(request)
			if err != nil || request.URL.Path != agentgateway.PairOperationsPath || response.StatusCode != http.StatusOK {
				return response, err
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			var operation map[string]json.RawMessage
			if err := json.Unmarshal(body, &operation); err != nil {
				return nil, err
			}
			expiresAt, _ := json.Marshal(time.Now().Add(250 * time.Millisecond).UTC().Format(time.RFC3339Nano))
			operation["expires_at"] = expiresAt
			body, _ = json.Marshal(operation)
			response.Body = io.NopCloser(strings.NewReader(string(body)))
			response.ContentLength = int64(len(body))
			return response, nil
		})}
		d.runAppleScript = func(ctx context.Context, _ string, _ ...string) (string, error) {
			dialogCalls.Add(1)
			<-ctx.Done()
			return "", ctx.Err()
		}
	}
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultRejected || stderr != "" || dialogCalls.Load() != 1 || f.keychain.count() != 0 || f.consentLaunches.Load() != 0 {
		t.Fatalf("code=%d result=%q stderr=%q native=%d keys=%d consent=%d", code, guiResult(t, stdout), stderr, dialogCalls.Load(), f.keychain.count(), f.consentLaunches.Load())
	}
}

func TestGUISuccessIgnoresOnlyPendingRemovalFailure(t *testing.T) {
	// Why: after policy commit, a stale pending file can safely reconcile the same gateway 200 on the next launch.
	f := newGUIPairFixture(t, nil)
	originalSync := syncDir
	var syncs atomic.Int32
	syncDir = func(dir string) error {
		if syncs.Add(1) == 2 {
			if err := originalSync(dir); err != nil {
				return err
			}
			return os.Chmod(dir, 0500)
		}
		return originalSync(dir)
	}
	t.Cleanup(func() {
		syncDir = originalSync
		_ = os.Chmod(f.dir, 0700)
	})
	code, stdout, stderr := f.run(t)
	_ = os.Chmod(f.dir, 0700)
	firstOperation, firstPreflights, firstConfirms := f.operation, f.operationsCalls.Load(), f.confirmCalls.Load()
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" || firstOperation == "" || f.keychain.count() != 1 {
		t.Fatalf("first run code=%d result=%q stderr=%q operation=%q keys=%d", code, guiResult(t, stdout), stderr, firstOperation, f.keychain.count())
	}
	if _, err := os.Stat(filepath.Join(f.dir, guiPairingPendingName)); err != nil {
		t.Fatalf("pending record was not retained after removal failure: %v", err)
	}
	syncDir = originalSync
	code, stdout, stderr = f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" || f.operationsCalls.Load() != firstPreflights || f.confirmCalls.Load() != firstConfirms+1 || f.operation != firstOperation {
		t.Fatalf("reconcile code=%d result=%q stderr=%q operations=%d/%d confirms=%d/%d operation=%q/%q", code, guiResult(t, stdout), stderr, f.operationsCalls.Load(), firstPreflights, f.confirmCalls.Load(), firstConfirms+1, f.operation, firstOperation)
	}
}

func TestGUIPreflightFingerprintMismatchDeletesNewKey(t *testing.T) {
	// Why: a gateway preview for a different public key cannot authorize the locally generated Keychain key.
	f := newGUIPairFixture(t, nil)
	otherSigner := mustSignerFromSeed(t, 0x53)
	otherFingerprint := ssh.FingerprintSHA256(otherSigner.PublicKey())
	f.depsOverride = func(d *deps) {
		base := f.client.Transport
		d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			response, err := base.RoundTrip(request)
			if err != nil || request.URL.Path != agentgateway.PairOperationsPath || response.StatusCode != http.StatusOK {
				return response, err
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			var operation map[string]json.RawMessage
			if err := json.Unmarshal(body, &operation); err != nil {
				return nil, err
			}
			operation["new_key_fingerprint"], _ = json.Marshal(otherFingerprint)
			body, err = json.Marshal(operation)
			if err != nil {
				return nil, err
			}
			response.Body = io.NopCloser(strings.NewReader(string(body)))
			response.ContentLength = int64(len(body))
			return response, nil
		})}
	}
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultUnavailable || stderr != "" || f.keychain.count() != 0 || f.nativeCalls.Load() != 0 || f.consentLaunches.Load() != 0 {
		t.Fatalf("code=%d result=%q stderr=%q keys=%d native=%d consent=%d", code, guiResult(t, stdout), stderr, f.keychain.count(), f.nativeCalls.Load(), f.consentLaunches.Load())
	}
}

func TestGUIBusyWhenAnotherHelperOwnsTheLock(t *testing.T) {
	// Why: a second helper must stop before creating a key or starting OAuth while the per-policy lock is held.
	f := newGUIPairFixture(t, nil)
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := lockPair(f.dir); err != nil {
		t.Fatal(err)
	}
	defer unlockPair()
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultBusy || stderr != "" || f.keychain.count() != 0 || f.operationsCalls.Load() != 0 || f.authLaunches.Load() != 0 {
		t.Fatalf("code=%d result=%q stderr=%q keys=%d operations=%d auth=%d", code, guiResult(t, stdout), stderr, f.keychain.count(), f.operationsCalls.Load(), f.authLaunches.Load())
	}
}
