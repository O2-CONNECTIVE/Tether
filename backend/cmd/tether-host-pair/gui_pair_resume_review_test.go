//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

func createGUIResumableOperation(t *testing.T, f *guiPairFixture, seed byte) (guiPairOperation, string) {
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
	token := f.signedToken(f.gateway.URL + agentgateway.PairPath)
	operation, preflightResult, err := createGUIPairOperation(context.Background(), f.client, deps{}, f.gateway.URL, f.issuer.URL, token, signer, fingerprint)
	if err != nil || preflightResult != "" {
		t.Fatalf("create pending gateway operation: result=%q err=%v", preflightResult, err)
	}
	return operation, fingerprint
}

func marshalGUIPendingJSON(t *testing.T, gateway, operation, fingerprint string, expiresAt time.Time) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"gateway": gateway, "operation": operation, "host_key_fingerprint": fingerprint,
		"consent_url": strings.TrimSuffix(gateway, "/") + agentgateway.PairConsentPath + "?operation=" + operation,
		"expires_at":  expiresAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeGUIPendingJSON(t *testing.T, f *guiPairFixture, gateway, operation, fingerprint string) string {
	t.Helper()
	raw := marshalGUIPendingJSON(t, gateway, operation, fingerprint, time.Now().Add(10*time.Minute))
	return writeGUIPendingRecord(t, f, raw)
}

func TestGUIResumePendingReopensConsentAfterFirst202(t *testing.T) {
	// Why: after a restart, the saved consent page may still need the user's decision before confirm can succeed.
	f := newGUIPairFixture(t, nil)
	operation, fingerprint := createGUIResumableOperation(t, f, 0x41)
	writeGUIPendingJSON(t, f, f.gateway.URL, operation.ID, fingerprint)
	f.depsOverride = func(d *deps) { d.confirmInterval = time.Millisecond }
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" || f.operation != operation.ID ||
		f.operationsCalls.Load() != 1 || f.confirmCalls.Load() != 2 || f.consentLaunches.Load() != 1 || f.nativeCalls.Load() != 0 || f.authLaunches.Load() != 0 {
		t.Fatalf("resume code=%d result=%q stderr=%q operation=%q calls=%d/%d consent=%d native=%d auth=%d",
			code, guiResult(t, stdout), stderr, f.operation, f.operationsCalls.Load(), f.confirmCalls.Load(), f.consentLaunches.Load(), f.nativeCalls.Load(), f.authLaunches.Load())
	}
}

func TestGUIResumeBrowserFailureCleansPendingKey(t *testing.T) {
	// Why: if the saved consent page cannot be reopened, the resumed registration must use the same browser_failed cleanup as a fresh flow.
	f := newGUIPairFixture(t, nil)
	operation, fingerprint := createGUIResumableOperation(t, f, 0x43)
	pendingPath := writeGUIPendingJSON(t, f, f.gateway.URL, operation.ID, fingerprint)
	var opens atomic.Int32
	f.depsOverride = func(d *deps) {
		d.confirmInterval = time.Millisecond
		d.openBrowser = func(raw string) error {
			if raw != operation.ConsentURL {
				return errors.New("unexpected consent URL")
			}
			opens.Add(1)
			return errors.New("synthetic browser launch failure")
		}
	}
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultBrowserFailed || stderr != "" || opens.Load() != 1 || f.keychain.has(fingerprint) || f.operationsCalls.Load() != 1 {
		t.Fatalf("resume code=%d result=%q stderr=%q opens=%d key=%v preflights=%d", code, guiResult(t, stdout), stderr, opens.Load(), f.keychain.has(fingerprint), f.operationsCalls.Load())
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending record remains after browser failure: %v", err)
	}
}

func TestGUIResumeUsesSavedOperationExpiry(t *testing.T) {
	// Why: restarting must not extend the gateway's fixed operation lifetime by another ten minutes.
	f := newGUIPairFixture(t, nil)
	operation, fingerprint := createGUIResumableOperation(t, f, 0x42)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Minute)
	writeGUIPendingRecord(t, f, marshalGUIPendingJSON(t, f.gateway.URL, operation.ID, fingerprint, expiresAt))
	currentTime := now
	var confirms, opens atomic.Int32
	f.depsOverride = func(d *deps) {
		d.confirmInterval = 5 * time.Millisecond
		d.now = func() time.Time { return currentTime }
		d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			confirms.Add(1)
			return guiPairHTTPResponse(request, http.StatusAccepted, "application/json", `{"state":"consent_pending"}`), nil
		})}
		d.openBrowser = func(raw string) error {
			if raw != operation.ConsentURL {
				return errors.New("unexpected consent URL")
			}
			opens.Add(1)
			currentTime = expiresAt
			return nil
		}
	}
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultRejected || stderr != "" || confirms.Load() != 1 || opens.Load() != 1 || f.keychain.has(fingerprint) {
		t.Fatalf("resume code=%d result=%q stderr=%q confirms=%d opens=%d key=%v", code, guiResult(t, stdout), stderr, confirms.Load(), opens.Load(), f.keychain.has(fingerprint))
	}
}

func TestGUIResumeExpiredPendingStillConfirmsSuccessfulOperation(t *testing.T) {
	// Why: a gateway may have committed before the response was lost, so expiry must not prevent reconciling its registered operation.
	f := newGUIPairFixture(t, nil)
	operation, fingerprint := createGUIResumableOperation(t, f, 0x44)
	pendingPath := writeGUIPendingRecord(t, f, marshalGUIPendingJSON(t, f.gateway.URL, operation.ID, fingerprint, time.Now().Add(-time.Second)))
	var confirms, opens atomic.Int32
	successBody := guiConfirmSuccessBody(t)
	f.depsOverride = func(d *deps) {
		d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			confirms.Add(1)
			return guiPairHTTPResponse(request, http.StatusOK, "application/json", successBody), nil
		})}
		d.openBrowser = func(string) error {
			opens.Add(1)
			return nil
		}
	}

	code, stdout, stderr := f.run(t)
	policyRaw, policyErr := os.ReadFile(f.policy)
	policy, policyExists := readGUIPolicyIdentity(policyRaw)
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" || confirms.Load() != 1 || opens.Load() != 0 ||
		!policyExists || policy.Fingerprint != fingerprint || !f.keychain.has(fingerprint) {
		t.Fatalf("resume code=%d result=%q stderr=%q confirms=%d opens=%d policy=%+v policy-exists=%v policy-error=%v key=%v",
			code, guiResult(t, stdout), stderr, confirms.Load(), opens.Load(), policy, policyExists, policyErr, f.keychain.has(fingerprint))
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending record remains after successful reconciliation: %v", err)
	}
}

func TestGUIResumeExpiredPending202RejectsWithoutOpeningBrowser(t *testing.T) {
	// Why: after an expired operation reports consent_pending, the helper must reject without reopening consent or waiting.
	f := newGUIPairFixture(t, nil)
	operation, fingerprint := createGUIResumableOperation(t, f, 0x45)
	pendingPath := writeGUIPendingRecord(t, f, marshalGUIPendingJSON(t, f.gateway.URL, operation.ID, fingerprint, time.Now().Add(-time.Second)))
	var confirms, opens atomic.Int32
	f.depsOverride = func(d *deps) {
		d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			confirms.Add(1)
			return guiPairHTTPResponse(request, http.StatusAccepted, "application/json", `{"state":"consent_pending"}`), nil
		})}
		d.openBrowser = func(string) error {
			opens.Add(1)
			return nil
		}
	}

	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultRejected || stderr != "" || confirms.Load() != 1 || opens.Load() != 0 || f.keychain.has(fingerprint) {
		t.Fatalf("resume code=%d result=%q stderr=%q confirms=%d opens=%d key=%v",
			code, guiResult(t, stdout), stderr, confirms.Load(), opens.Load(), f.keychain.has(fingerprint))
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending record remains after expired consent_pending: %v", err)
	}
}

func TestGUIResumeTerminalResultPreservesCurrentPolicyKey(t *testing.T) {
	// Why: a terminal gateway result clears stale pending metadata but must not delete the key the current policy still uses.
	f := newGUIPairFixture(t, nil)
	fingerprint := seedGUIPendingKey(t, f, 0x46)
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(map[string]string{"host_key_store": "keychain", "host_key_fingerprint": fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.policy, policy, 0600); err != nil {
		t.Fatal(err)
	}
	pendingPath := writeGUIPendingJSON(t, f, f.gateway.URL, validGUIPendingOperation(), fingerprint)
	var confirms atomic.Int32
	f.depsOverride = func(d *deps) {
		d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			confirms.Add(1)
			return guiPairHTTPResponse(request, http.StatusNotFound, "application/json", `{"error":"operation_not_found"}`), nil
		})}
	}

	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultRejected || stderr != "" || confirms.Load() != 1 || !f.keychain.has(fingerprint) {
		t.Fatalf("resume code=%d result=%q stderr=%q confirms=%d key=%v",
			code, guiResult(t, stdout), stderr, confirms.Load(), f.keychain.has(fingerprint))
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending record remains after terminal gateway result: %v", err)
	}
}

func TestGUIPendingDirectorySyncFailureContinuesAfterRename(t *testing.T) {
	// Why: once rename has committed the recoverable pending record, a directory sync warning must not stop consent.
	f := newGUIPairFixture(t, nil)
	originalSync := syncDir
	var calls atomic.Int32
	syncDir = func(dir string) error {
		if calls.Add(1) == 1 {
			return os.ErrPermission
		}
		return originalSync(dir)
	}
	t.Cleanup(func() { syncDir = originalSync })
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" || calls.Load() < 2 || f.consentLaunches.Load() != 1 || f.keychain.count() != 1 {
		t.Fatalf("code=%d result=%q stderr=%q syncs=%d consent=%d keys=%d", code, guiResult(t, stdout), stderr, calls.Load(), f.consentLaunches.Load(), f.keychain.count())
	}
}

func TestGUIPendingGatewayTrailingSlashStillResumes(t *testing.T) {
	// Why: an equivalent HTTPS origin must not discard a key and consent operation because one record has a trailing slash.
	f := newGUIPairFixture(t, nil)
	originalRename := renameFile
	renameFile = func(old, next string) error {
		if next == f.policy {
			return os.ErrPermission
		}
		return os.Rename(old, next)
	}
	code, stdout, _ := f.run(t)
	renameFile = originalRename
	if code != 0 || guiResult(t, stdout) != guiResultLocalApplyFailed {
		t.Fatalf("seed run code=%d result=%q", code, guiResult(t, stdout))
	}
	pendingPath := filepath.Join(f.dir, guiPairingPendingName)
	raw, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	var pending map[string]json.RawMessage
	if err := json.Unmarshal(raw, &pending); err != nil {
		t.Fatal(err)
	}
	gateway, _ := json.Marshal(strings.TrimSuffix(f.gateway.URL, "/") + "/")
	pending["gateway"] = gateway
	raw, err = json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	preflights, auth, consent := f.operationsCalls.Load(), f.authLaunches.Load(), f.consentLaunches.Load()
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" || f.operationsCalls.Load() != preflights || f.authLaunches.Load() != auth || f.consentLaunches.Load() != consent || f.keychain.count() != 1 {
		t.Fatalf("resume code=%d result=%q operations=%d/%d auth=%d/%d consent=%d/%d keys=%d", code, guiResult(t, stdout), f.operationsCalls.Load(), preflights, f.authLaunches.Load(), auth, f.consentLaunches.Load(), consent, f.keychain.count())
	}
}
