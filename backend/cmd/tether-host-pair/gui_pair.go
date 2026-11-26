package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostkeychain"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostpairoauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

const (
	guiResultSucceeded            = "succeeded"
	guiResultCancelled            = "cancelled"
	guiResultRejected             = "rejected"
	guiResultRegistrationRequired = "re_registration_required"
	guiResultBlocked              = "blocked"
	guiResultUnknown              = "unknown"
	guiResultLocalApplyFailed     = "local_apply_failed"
	guiResultBrowserFailed        = "browser_failed"
	guiResultBusy                 = "busy"
	guiResultUnavailable          = "unavailable"
	guiConfirmInterval            = 2 * time.Second
	guiPairingPendingName         = "pairing-pending.json"
	guiNativeContinue             = "continue"
	guiNativeCancel               = "cancel"
	guiNativePromptScript         = `on run argv
try
set accountValue to item 1 of argv
set slotValue to item 2 of argv
set replacingValue to item 3 of argv
set fingerprintValue to item 4 of argv
set promptText to "アカウント: " & accountValue & return & "登録枠: " & slotValue & return & "置き換え: " & replacingValue & return & "新しい鍵: " & fingerprintValue
set selectedButton to button returned of (display dialog promptText with title "Tether ホスト登録" buttons {"取り消す", "ブラウザで確認する"} default button "ブラウザで確認する" cancel button "取り消す")
if selectedButton is "ブラウザで確認する" then return "continue"
return "cancel"
on error number -128
return "cancel"
end try
end run`
)

type guiPairPending struct {
	Gateway            string    `json:"gateway"`
	Operation          string    `json:"operation"`
	HostKeyFingerprint string    `json:"host_key_fingerprint"`
	ConsentURL         string    `json:"consent_url"`
	ExpiresAt          time.Time `json:"expires_at"`
}

type guiPolicyIdentity struct {
	Fingerprint    string `json:"host_key_fingerprint"`
	PrincipalLabel string `json:"principal_label"`
	HostLabel      string `json:"host_label"`
}

var errInvalidGUIPendingRecord = errors.New("invalid pending pair record")

type guiPairOperation struct {
	ID      string `json:"operation"`
	Account struct {
		Issuer         string `json:"issuer"`
		Subject        string `json:"subject"`
		PrincipalLabel string `json:"principal_label"`
	} `json:"account"`
	HostLabel string `json:"host_label"`
	Replacing *struct {
		Fingerprint  string `json:"fingerprint"`
		RegisteredAt string `json:"registered_at"`
	} `json:"replacing"`
	NewKeyFingerprint string    `json:"new_key_fingerprint"`
	ConsentURL        string    `json:"consent_url"`
	ExpiresAt         time.Time `json:"expires_at"`
}

func guiHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Timeout: 60 * time.Second, CheckRedirect: noPairRedirect}
	}
	copy := transport.Clone()
	copy.Proxy = nil
	return &http.Client{Transport: copy, Timeout: 60 * time.Second, CheckRedirect: noPairRedirect}
}

func noPairRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func executeGUI(ctx context.Context, stdout io.Writer, d deps) int {
	result := executeGUIResult(ctx, d)
	raw, err := json.Marshal(struct {
		ProtocolVersion int    `json:"protocolVersion"`
		Result          string `json:"result"`
	}{ProtocolVersion: 1, Result: result})
	if err != nil {
		return 1
	}
	raw = append(raw, '\n')
	n, err := stdout.Write(raw)
	if err != nil || n != len(raw) {
		return 1
	}
	return 0
}

func executeGUIResult(ctx context.Context, d deps) string {
	gateway, issuer, clientID := guiGateway, guiIssuer, guiClientID
	if !validGatewayForm(gateway) || !hostpairoauth.ValidClient(issuer, clientID) {
		return guiResultUnavailable
	}
	client := d.guiClient
	if client == nil {
		client = d.client
	}
	if client == nil || d.location == nil || d.openBrowser == nil || d.runAppleScript == nil || d.storeHostKey == nil || d.loadHostKey == nil || d.deleteHostKey == nil {
		return guiResultUnavailable
	}

	locationCtx, cancelLocation := context.WithTimeout(ctx, 10*time.Second)
	home, policyPath, err := d.location(locationCtx)
	cancelLocation()
	if err != nil || !filepath.IsAbs(home) || filepath.Clean(home) != home || !filepath.IsAbs(policyPath) || filepath.Clean(policyPath) != policyPath {
		return guiResultUnavailable
	}
	dir := filepath.Dir(policyPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return guiResultUnavailable
	}
	if err := lockPair(dir); err != nil {
		if errors.Is(err, errPairBusy) {
			return guiResultBusy
		}
		return guiResultUnavailable
	}
	defer unlockPair()

	if d.readProtected == nil {
		d.readProtected = agentkeys.ReadTrustConfig
	}
	protectCtx, cancelProtect := context.WithTimeout(ctx, 10*time.Second)
	_, err = d.readProtected(protectCtx, filepath.Join(dir, "pair.lock"))
	cancelProtect()
	if err != nil {
		return guiResultUnavailable
	}
	var oldPolicy []byte
	if _, statErr := os.Lstat(policyPath); statErr == nil {
		protectCtx, cancelProtect = context.WithTimeout(ctx, 10*time.Second)
		oldPolicy, err = d.readProtected(protectCtx, policyPath)
		cancelProtect()
		if err != nil {
			return guiResultUnavailable
		}
	} else if !os.IsNotExist(statErr) {
		return guiResultUnavailable
	}
	probe, err := writeTemp(dir, ".pair-probe-", []byte("pair probe\n"))
	if err != nil {
		return guiResultUnavailable
	}
	protectCtx, cancelProtect = context.WithTimeout(ctx, 10*time.Second)
	_, probeErr := d.readProtected(protectCtx, probe)
	cancelProtect()
	removeErr := os.Remove(probe)
	if probeErr != nil || removeErr != nil {
		return guiResultUnavailable
	}

	pendingPath := filepath.Join(dir, guiPairingPendingName)
	pending, hasPending, err := readGUIWaiting(ctx, pendingPath, gateway, d.readProtected)
	if err != nil {
		if !errors.Is(err, errInvalidGUIPendingRecord) || discardGUIPending(dir, pendingPath, pending, oldPolicy, d.deleteHostKey) != nil {
			return guiResultUnavailable
		}
	}
	if hasPending {
		signer, err := d.loadHostKey(pending.HostKeyFingerprint)
		if errors.Is(err, hostkeychain.ErrNotFound) || errors.Is(err, hostkeychain.ErrFingerprint) || err == nil && (signer == nil || ssh.FingerprintSHA256(signer.PublicKey()) != pending.HostKeyFingerprint) {
			if discardGUIPending(dir, pendingPath, pending, oldPolicy, d.deleteHostKey) != nil {
				return guiResultUnavailable
			}
			hasPending = false
		} else if err != nil {
			return guiResultUnavailable
		}
		if hasPending {
			result, registration := confirmGUIPair(ctx, client, d, gateway, pending.Operation, pending.HostKeyFingerprint, signer, pending.ExpiresAt, pending.ConsentURL, true)
			return completeGUIResult(result, registration, pending, pendingPath, dir, home, policyPath, oldPolicy, signer, d)
		}
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return guiResultUnavailable
	}
	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		clear(private)
		return guiResultUnavailable
	}
	fingerprint := ssh.FingerprintSHA256(publicKey)
	if err := d.storeHostKey(private, fingerprint); err != nil {
		clear(private)
		_ = d.deleteHostKey(fingerprint)
		return guiResultUnavailable
	}
	clear(private)
	signer, err := d.loadHostKey(fingerprint)
	if err != nil || signer == nil || ssh.FingerprintSHA256(signer.PublicKey()) != fingerprint {
		_ = d.deleteHostKey(fingerprint)
		return guiResultUnavailable
	}

	token, err := hostpairoauth.AccessToken(ctx, hostpairoauth.Config{
		Client: client, Issuer: issuer, ClientID: clientID,
		CallbackAddress: d.callbackAddress, CallbackWait: d.callbackWait,
		OpenBrowser: func(openCtx context.Context, authURL string) error {
			if openCtx.Err() != nil {
				return openCtx.Err()
			}
			return d.openBrowser(authURL)
		},
	})
	if err != nil {
		if errors.Is(err, hostpairoauth.ErrBrowserOpen) {
			if d.deleteHostKey(fingerprint) != nil {
				return guiResultUnavailable
			}
			return guiResultBrowserFailed
		}
		if d.deleteHostKey(fingerprint) != nil {
			return guiResultUnavailable
		}
		if errors.Is(err, hostpairoauth.ErrCallbackTimeout) || errors.Is(err, context.Canceled) || err.Error() == "cancelled" {
			return guiResultCancelled
		}
		return guiResultUnavailable
	}

	operation, preflightResult, err := createGUIPairOperation(ctx, client, d, gateway, token, signer, fingerprint)
	if err != nil {
		_ = d.deleteHostKey(fingerprint)
		return guiResultUnavailable
	}
	if preflightResult != "" {
		if d.deleteHostKey(fingerprint) != nil {
			return guiResultUnavailable
		}
		return preflightResult
	}
	if time.Until(operation.ExpiresAt) <= 0 {
		_ = d.deleteHostKey(fingerprint)
		return guiResultRejected
	}

	replacing := "新規"
	replacingFingerprint := ""
	if operation.Replacing != nil {
		replacing = operation.Replacing.Fingerprint + " / " + operation.Replacing.RegisteredAt
		replacingFingerprint = operation.Replacing.Fingerprint
	}
	currentPolicy, hasCurrentPolicy := readGUIPolicyIdentity(oldPolicy)
	if hasCurrentPolicy && currentPolicy.Fingerprint != replacingFingerprint {
		replacing += " / この Mac の今の登録（" + currentPolicy.PrincipalLabel + " / " + currentPolicy.HostLabel + "）は使えなくなります"
	}
	nativeCtx, cancelNative := context.WithDeadline(ctx, operation.ExpiresAt)
	nativeResult, nativeErr := d.runAppleScript(nativeCtx, guiNativePromptScript,
		operation.Account.Subject, operation.HostLabel, replacing, operation.NewKeyFingerprint)
	nativeContextErr := nativeCtx.Err()
	cancelNative()
	if nativeErr != nil {
		if d.deleteHostKey(fingerprint) != nil {
			return guiResultUnavailable
		}
		if !time.Now().Before(operation.ExpiresAt) {
			return guiResultRejected
		}
		if errors.Is(nativeContextErr, context.Canceled) {
			return guiResultCancelled
		}
		return guiResultUnavailable
	}
	switch strings.TrimSpace(nativeResult) {
	case guiNativeCancel:
		if d.deleteHostKey(fingerprint) != nil {
			return guiResultUnavailable
		}
		return guiResultCancelled
	case guiNativeContinue:
	default:
		if d.deleteHostKey(fingerprint) != nil {
			return guiResultUnavailable
		}
		return guiResultUnavailable
	}

	pending = guiPairPending{Gateway: gateway, Operation: operation.ID, HostKeyFingerprint: fingerprint, ConsentURL: operation.ConsentURL, ExpiresAt: operation.ExpiresAt}
	committed, err := writeGUIWaiting(dir, pendingPath, pending)
	if err != nil && !committed {
		_ = d.deleteHostKey(fingerprint)
		return guiResultUnavailable
	}
	if err := d.openBrowser(operation.ConsentURL); err != nil {
		return cleanupGUIResult(guiResultBrowserFailed, pending, pendingPath, dir, d)
	}
	result, registration := confirmGUIPair(ctx, client, d, gateway, pending.Operation, fingerprint, signer, operation.ExpiresAt, "", false)
	return completeGUIResult(result, registration, pending, pendingPath, dir, home, policyPath, oldPolicy, signer, d)
}

func readGUIWaiting(ctx context.Context, path, gateway string, readProtected func(context.Context, string) ([]byte, error)) (guiPairPending, bool, error) {
	var pending guiPairPending
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return pending, false, nil
	} else if err != nil {
		return pending, false, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	raw, err := readProtected(readCtx, path)
	cancel()
	if err != nil {
		return pending, false, err
	}
	unmarshalErr := json.Unmarshal(raw, &pending)
	fields, fieldsErr := agentwire.Fields(raw, "gateway", "operation", "host_key_fingerprint", "consent_url", "expires_at")
	if unmarshalErr != nil || fieldsErr != nil || len(fields) != 5 || !sameGUIGateway(pending.Gateway, gateway) || !agentgateway.ValidPairOperationID(pending.Operation) || !validGUIPendingFingerprint(pending.HostKeyFingerprint) || !validGUIConsentURL(pending.Gateway, pending.Operation, pending.ConsentURL) || pending.ExpiresAt.IsZero() {
		return pending, false, errInvalidGUIPendingRecord
	}
	return pending, true, nil
}

func sameGUIGateway(left, right string) bool {
	if !validGatewayForm(left) || !validGatewayForm(right) {
		return false
	}
	a, _ := url.Parse(left)
	b, _ := url.Parse(right)
	return a.Scheme == b.Scheme && a.Host == b.Host
}

func readGUIPolicyIdentity(raw []byte) (guiPolicyIdentity, bool) {
	var identity guiPolicyIdentity
	if agentwire.CheckJSON(raw, false) != nil || json.Unmarshal(raw, &identity) != nil {
		return guiPolicyIdentity{}, false
	}
	return identity, true
}

func validGUIPendingFingerprint(fingerprint string) bool {
	const prefix = "SHA256:"
	if !strings.HasPrefix(fingerprint, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(fingerprint, prefix)
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == ed25519.PublicKeySize && base64.RawStdEncoding.EncodeToString(decoded) == encoded
}

func discardGUIPending(dir, path string, pending guiPairPending, oldPolicy []byte, deleteHostKey func(string) error) error {
	policy, _ := readGUIPolicyIdentity(oldPolicy)
	if validGUIPendingFingerprint(pending.HostKeyFingerprint) && pending.HostKeyFingerprint != policy.Fingerprint {
		if err := deleteHostKey(pending.HostKeyFingerprint); err != nil && !errors.Is(err, hostkeychain.ErrNotFound) {
			return err
		}
	}
	return removeGUIWaiting(dir, path)
}

func writeGUIWaiting(dir, path string, pending guiPairPending) (bool, error) {
	raw, err := json.Marshal(pending)
	if err != nil {
		return false, err
	}
	temp, err := writeTemp(dir, ".pair-pending-", raw)
	if err != nil {
		return false, err
	}
	defer os.Remove(temp)
	if err := renameFile(temp, path); err != nil {
		return false, err
	}
	if err := syncDir(dir); err != nil {
		return true, err
	}
	return true, nil
}

func removeGUIWaiting(dir, path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			return err
		}
	}
	_ = syncDir(dir)
	return nil
}

func createGUIPairOperation(ctx context.Context, client *http.Client, d deps, gateway, token string, signer ssh.Signer, fingerprint string) (guiPairOperation, string, error) {
	var operation guiPairOperation
	u, err := url.Parse(gateway)
	if err != nil {
		return operation, "", err
	}
	endpoint := u.Scheme + "://" + u.Host + agentgateway.PairOperationsPath
	tokenHash := sha256.Sum256([]byte(token))
	signature, err := signer.Sign(rand.Reader, agentgateway.PairOperationTranscript(endpoint, signer.PublicKey().Marshal(), tokenHash))
	if err != nil {
		return operation, "", err
	}
	publicKey := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
	body, err := json.Marshal(map[string]string{"host_public_key": publicKey, "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(signature))})
	if err != nil {
		return operation, "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, pairRequestTimeout(d))
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return operation, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return operation, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
		if readErr != nil || len(raw) > 16*1024 {
			return operation, guiResultUnavailable, nil
		}
		if response.StatusCode == http.StatusConflict && pairHTTPError(raw) == "replace_blocked" {
			return operation, guiResultBlocked, nil
		}
		if response.StatusCode >= 400 && response.StatusCode < 500 || response.StatusCode == http.StatusServiceUnavailable {
			return operation, guiResultRejected, nil
		}
		return operation, guiResultUnavailable, nil
	}
	if response.Header.Get("Content-Type") == "" {
		return operation, "", errors.New("pair operation response invalid")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
	if mediaErr != nil || mediaType != "application/json" || readErr != nil || len(raw) > 16*1024 {
		return operation, "", errors.New("pair operation response invalid")
	}
	fields, err := agentwire.Fields(raw, "operation", "account", "host_label", "replacing", "new_key_fingerprint", "consent_url", "expires_at")
	if err != nil || len(fields) != 7 || json.Unmarshal(raw, &operation) != nil {
		return operation, "", errors.New("pair operation response invalid")
	}
	accountFields, err := agentwire.Fields(fields["account"], "issuer", "subject", "principal_label")
	if err != nil || len(accountFields) != 3 || operation.ID == "" || !agentgateway.ValidPairOperationID(operation.ID) || operation.Account.Issuer != guiIssuer || operation.Account.Subject == "" || operation.Account.PrincipalLabel == "" || operation.HostLabel == "" || operation.NewKeyFingerprint != fingerprint {
		return operation, "", errors.New("pair operation response invalid")
	}
	if strings.TrimSpace(string(fields["replacing"])) != "null" {
		replacementFields, fieldErr := agentwire.Fields(fields["replacing"], "fingerprint", "registered_at")
		if fieldErr != nil || len(replacementFields) != 2 || operation.Replacing == nil || operation.Replacing.Fingerprint == "" || operation.Replacing.RegisteredAt == "" {
			return operation, "", errors.New("pair operation response invalid")
		}
		if _, parseErr := time.Parse(time.RFC3339, operation.Replacing.RegisteredAt); parseErr != nil {
			return operation, "", errors.New("pair operation response invalid")
		}
	}
	if !validGUIConsentURL(gateway, operation.ID, operation.ConsentURL) || operation.ExpiresAt.IsZero() || !time.Now().Before(operation.ExpiresAt) {
		return operation, "", errors.New("pair operation response invalid")
	}
	return operation, "", nil
}

func validGUIConsentURL(gateway, operation, raw string) bool {
	base, baseErr := url.Parse(gateway)
	u, err := url.Parse(raw)
	if baseErr != nil || err != nil || u.Scheme != "https" || u.Host != base.Host || u.User != nil || u.Path != agentgateway.PairConsentPath || u.Fragment != "" {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	return err == nil && len(query) == 1 && len(query["operation"]) == 1 && query.Get("operation") == operation
}

func pairRequestTimeout(d deps) time.Duration {
	if d.pairTimeout > 0 {
		return d.pairTimeout
	}
	return 60 * time.Second
}

func confirmGUIPair(ctx context.Context, client *http.Client, d deps, gateway, operation, fingerprint string, signer ssh.Signer, deadline time.Time, consentURL string, reopenConsent bool) (string, pairResult) {
	var empty pairResult
	u, err := url.Parse(gateway)
	if err != nil {
		return guiResultUnknown, empty
	}
	endpoint := u.Scheme + "://" + u.Host + agentgateway.PairConfirmPath
	signature, err := signer.Sign(rand.Reader, agentgateway.PairConfirmTranscript(endpoint, operation, signer.PublicKey().Marshal()))
	if err != nil {
		return guiResultUnknown, empty
	}
	body, err := json.Marshal(map[string]string{"operation": operation, "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(signature))})
	if err != nil {
		return guiResultUnknown, empty
	}
	interval := d.confirmInterval
	if interval <= 0 {
		interval = guiConfirmInterval
	}
	pendingSeen := false
	transientFailure := false
	for {
		if !time.Now().Before(deadline) {
			if pendingSeen && transientFailure {
				return guiResultUnknown, empty
			}
			return guiResultRejected, empty
		}
		requestTimeout := pairRequestTimeout(d)
		if remaining := time.Until(deadline); remaining < requestTimeout {
			requestTimeout = remaining
		}
		requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		req, reqErr := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(string(body)))
		if reqErr != nil {
			cancel()
			return guiResultUnknown, empty
		}
		req.Header.Set("Content-Type", "application/json")
		response, requestErr := client.Do(req)
		if requestErr != nil {
			cancel()
			if pendingSeen {
				transientFailure = true
				if waitGUIConfirmRetry(ctx, interval, deadline) {
					continue
				}
			}
			return guiResultUnknown, empty
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
		response.Body.Close()
		cancel()
		if readErr != nil {
			if pendingSeen {
				transientFailure = true
				if waitGUIConfirmRetry(ctx, interval, deadline) {
					continue
				}
			}
			return guiResultUnknown, empty
		}
		if len(raw) > 16*1024 {
			return guiResultUnknown, empty
		}
		switch response.StatusCode {
		case http.StatusOK:
			result, parseErr := parsePairResult(response.Header.Get("Content-Type"), raw)
			if parseErr != nil {
				return guiResultUnknown, empty
			}
			return guiResultSucceeded, result
		case http.StatusAccepted:
			fields, parseErr := agentwire.Fields(raw, "state")
			var state string
			if parseErr != nil || len(fields) != 1 || json.Unmarshal(fields["state"], &state) != nil || state != "consent_pending" {
				return guiResultUnknown, empty
			}
			pendingSeen = true
			transientFailure = false
			if reopenConsent {
				if d.openBrowser == nil || d.openBrowser(consentURL) != nil {
					return guiResultBrowserFailed, empty
				}
				reopenConsent = false
			}
			if !waitGUIConfirmRetry(ctx, interval, deadline) {
				if ctx.Err() != nil {
					return guiResultUnknown, empty
				}
				return guiResultRejected, empty
			}
			continue
		case http.StatusForbidden:
			if pairHTTPError(raw) == "consent_cancelled" {
				return guiResultCancelled, empty
			}
			return guiResultUnknown, empty
		case http.StatusNotFound:
			if pairHTTPError(raw) == "operation_not_found" {
				return guiResultRejected, empty
			}
			return guiResultUnknown, empty
		case http.StatusServiceUnavailable:
			if pairHTTPError(raw) == "save_failed" {
				return guiResultRejected, empty
			}
			return guiResultUnknown, empty
		case http.StatusConflict:
			errorCode := pairHTTPError(raw)
			switch errorCode {
			case "revision_changed":
				return guiResultRegistrationRequired, empty
			case "replace_blocked":
				return guiResultBlocked, empty
			default:
				return guiResultRejected, empty
			}
		case http.StatusInternalServerError:
			return guiResultUnknown, empty
		case http.StatusBadGateway, http.StatusGatewayTimeout:
			if pendingSeen {
				transientFailure = true
				if waitGUIConfirmRetry(ctx, interval, deadline) {
					continue
				}
			}
			return guiResultUnknown, empty
		default:
			return guiResultUnknown, empty
		}
	}
}

func waitGUIConfirmRetry(ctx context.Context, interval time.Duration, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	wait := interval
	if remaining < wait {
		wait = remaining
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return time.Now().Before(deadline)
	case <-ctx.Done():
		return false
	}
}

func pairHTTPError(raw []byte) string {
	fields, err := agentwire.Fields(raw, "error")
	if err != nil || len(fields) != 1 {
		return ""
	}
	var code string
	if json.Unmarshal(fields["error"], &code) != nil {
		return ""
	}
	return code
}

func completeGUIResult(result string, registration pairResult, pending guiPairPending, pendingPath, dir, home, policyPath string, oldPolicy []byte, signer ssh.Signer, d deps) string {
	if result == guiResultSucceeded {
		if writeGUIKeychainPolicy(dir, policyPath, home, signer, registration, oldPolicy, d.deleteHostKey) != nil {
			return guiResultLocalApplyFailed
		}
		_ = removeGUIWaiting(dir, pendingPath)
		return guiResultSucceeded
	}
	if result == guiResultUnknown || result == guiResultLocalApplyFailed {
		return result
	}
	return cleanupGUIResult(result, pending, pendingPath, dir, d)
}

func cleanupGUIResult(result string, pending guiPairPending, pendingPath, dir string, d deps) string {
	if d.deleteHostKey(pending.HostKeyFingerprint) != nil {
		return guiResultUnavailable
	}
	if removeGUIWaiting(dir, pendingPath) != nil {
		return guiResultUnavailable
	}
	return result
}

func writeGUIKeychainPolicy(dir, policyPath, home string, signer ssh.Signer, result pairResult, oldPolicy []byte, deleteHostKey func(string) error) error {
	raw, err := residentPolicyBytes(home, signer, result, "", "keychain")
	if err != nil {
		return err
	}
	if err := maclaunch.ValidateFixedResidentPolicy(raw, policyPath); err != nil {
		return err
	}
	temp, err := writeTemp(dir, ".pair-policy-", raw)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err := renameFile(temp, policyPath); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		_ = restoreGUIPreviousPolicy(dir, policyPath, oldPolicy)
		return err
	}
	_ = removePreviousPairKey(dir, oldPolicy, signer, deleteHostKey)
	return nil
}

func restoreGUIPreviousPolicy(dir, policyPath string, oldPolicy []byte) error {
	if len(oldPolicy) == 0 {
		if err := os.Remove(policyPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDir(dir)
	}
	temp, err := writeTemp(dir, ".pair-policy-restore-", oldPolicy)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err := renameFile(temp, policyPath); err != nil {
		return err
	}
	return syncDir(dir)
}

func removePreviousPairKey(dir string, oldPolicy []byte, signer ssh.Signer, deleteHostKey func(string) error) error {
	if len(oldPolicy) == 0 || agentwire.CheckJSON(oldPolicy, false) != nil {
		return nil
	}
	var old struct {
		KeyStore    string `json:"host_key_store"`
		KeyPath     string `json:"host_key_path"`
		Fingerprint string `json:"host_key_fingerprint"`
	}
	if json.Unmarshal(oldPolicy, &old) != nil || old.Fingerprint == ssh.FingerprintSHA256(signer.PublicKey()) {
		return nil
	}
	if old.KeyStore == "keychain" && old.KeyPath == "" {
		if old.Fingerprint == "" {
			return nil
		}
		return deleteHostKey(old.Fingerprint)
	}
	if old.KeyStore != "" || old.KeyPath == "" || filepath.Clean(old.KeyPath) != old.KeyPath || filepath.Dir(old.KeyPath) != dir || !strings.HasPrefix(filepath.Base(old.KeyPath), "host-key-") {
		return nil
	}
	if err := os.Remove(old.KeyPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
