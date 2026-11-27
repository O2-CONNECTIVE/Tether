//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type guiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f guiRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func guiPairHTTPResponse(request *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func guiConfirmSuccessBody(t *testing.T) string {
	t.Helper()
	principal := mustSignerFromSeed(t, 0x31)
	raw, err := json.Marshal(pairResult{
		RegistrationEndpoint: "wss://gateway.example.test/register",
		HostLabel:            "gui-host",
		PrincipalLabel:       "gui-member",
		PrincipalPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(principal.PublicKey()))),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGUIConfirmReadsBodyAfterHeaders(t *testing.T) {
	// Why: a successful response must survive a server that flushes headers before the registration body.
	signer := mustSignerFromSeed(t, 0x32)
	body := guiConfirmSuccessBody(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	result, registration := confirmGUIPair(context.Background(), server.Client(), deps{pairTimeout: time.Second}, server.URL,
		validGUIPendingOperation(), ssh.FingerprintSHA256(signer.PublicKey()), signer, time.Now().Add(time.Second), "", false)
	if result != guiResultSucceeded || registration.HostLabel != "gui-host" {
		t.Fatalf("confirm result=%q registration=%+v, want succeeded after delayed body", result, registration)
	}
}

func TestGUIConfirmTerminalResponsesNeedGatewayErrorCodes(t *testing.T) {
	// Why: Worker gate and binding refusals are not evidence that the gateway ended the pending registration.
	cases := []struct {
		name, body string
		status     int
		want       string
		keep       bool
	}{
		{name: "worker 404", status: http.StatusNotFound, body: "Not Found\n", want: guiResultUnknown, keep: true},
		{name: "worker 503", status: http.StatusServiceUnavailable, body: "native route not configured\n", want: guiResultUnknown, keep: true},
		{name: "403 without gateway code", status: http.StatusForbidden, body: "Forbidden\n", want: guiResultUnknown, keep: true},
		{name: "403 unknown gateway code", status: http.StatusForbidden, body: `{"error":"consent_invalid"}`, want: guiResultUnknown, keep: true},
		{name: "unknown 404 code", status: http.StatusNotFound, body: `{"error":"route_unavailable"}`, want: guiResultUnknown, keep: true},
		{name: "unknown 503 code", status: http.StatusServiceUnavailable, body: `{"error":"consent_unavailable"}`, want: guiResultUnknown, keep: true},
		{name: "other status", status: http.StatusBadRequest, body: `{"error":"invalid_request"}`, want: guiResultUnknown, keep: true},
		{name: "gateway operation missing", status: http.StatusNotFound, body: `{"error":"operation_not_found"}`, want: guiResultRejected},
		{name: "gateway save failed", status: http.StatusServiceUnavailable, body: `{"error":"save_failed"}`, want: guiResultRejected},
		{name: "gateway consent cancelled", status: http.StatusForbidden, body: `{"error":"consent_cancelled"}`, want: guiResultCancelled},
		{name: "replace blocked", status: http.StatusConflict, body: `{"error":"replace_blocked"}`, want: guiResultBlocked},
		{name: "key in use", status: http.StatusConflict, body: `{"error":"key_in_use"}`, want: guiResultRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGUIPairFixture(t, nil)
			fingerprint := seedGUIPendingKey(t, f, 0x33)
			pending := marshalGUIPendingJSON(t, f.gateway.URL, validGUIPendingOperation(), fingerprint, time.Now().Add(time.Hour))
			pendingPath := writeGUIPendingRecord(t, f, pending)
			f.depsOverride = func(d *deps) {
				d.guiClient = &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					return guiPairHTTPResponse(request, tc.status, "application/json", tc.body), nil
				})}
			}
			code, stdout, stderr := f.run(t)
			got := guiResult(t, stdout)
			if code != 0 || stderr != "" || got != tc.want || f.keychain.has(fingerprint) == !tc.keep {
				t.Fatalf("code=%d result=%q stderr=%q key=%v, want result=%q key-kept=%v", code, got, stderr, f.keychain.has(fingerprint), tc.want, tc.keep)
			}
			_, statErr := os.Stat(pendingPath)
			if tc.keep && statErr != nil || !tc.keep && !os.IsNotExist(statErr) {
				t.Fatalf("pending stat error=%v, keep=%v", statErr, tc.keep)
			}
		})
	}
}

func TestGUIConfirmRetriesTransientFailuresAfterConsentPending(t *testing.T) {
	// Why: an interrupted network reply after consent is not terminal and must be retried with the same operation.
	signer := mustSignerFromSeed(t, 0x34)
	successBody := guiConfirmSuccessBody(t)
	var calls atomic.Int32
	client := &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return guiPairHTTPResponse(request, http.StatusAccepted, "application/json", `{"state":"consent_pending"}`), nil
		case 2:
			return nil, errors.New("synthetic connection reset")
		case 3:
			return guiPairHTTPResponse(request, http.StatusBadGateway, "text/plain", "upstream refused\n"), nil
		case 4:
			return guiPairHTTPResponse(request, http.StatusGatewayTimeout, "text/plain", "upstream timed out\n"), nil
		case 5:
			return guiPairHTTPResponse(request, http.StatusAccepted, "application/json", `{"state":"consent_pending"}`), nil
		default:
			return guiPairHTTPResponse(request, http.StatusOK, "application/json", successBody), nil
		}
	})}
	result, registration := confirmGUIPair(context.Background(), client, deps{pairTimeout: time.Second, confirmInterval: time.Millisecond},
		"https://gateway.example.test", validGUIPendingOperation(), ssh.FingerprintSHA256(signer.PublicKey()), signer, time.Now().Add(time.Second), "", false)
	if result != guiResultSucceeded || registration.HostLabel != "gui-host" || calls.Load() != 6 {
		t.Fatalf("confirm result=%q registration=%+v requests=%d, want succeeded after six attempts", result, registration, calls.Load())
	}
}

func TestGUIConfirmFailureUntilDeadlineRemainsUnknown(t *testing.T) {
	// Why: after consent, repeated transport failures may hide a commit and must retain an unknown outcome at expiry.
	signer := mustSignerFromSeed(t, 0x35)
	var calls atomic.Int32
	client := &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return guiPairHTTPResponse(request, http.StatusAccepted, "application/json", `{"state":"consent_pending"}`), nil
		}
		return nil, errors.New("synthetic connection reset")
	})}
	result, _ := confirmGUIPair(context.Background(), client, deps{pairTimeout: time.Second, confirmInterval: time.Millisecond},
		"https://gateway.example.test", validGUIPendingOperation(), ssh.FingerprintSHA256(signer.PublicKey()), signer, time.Now().Add(25*time.Millisecond), "", false)
	if result != guiResultUnknown || calls.Load() < 3 {
		t.Fatalf("confirm result=%q requests=%d, want unknown after repeated retries", result, calls.Load())
	}
}

func TestGUIConfirmPendingUntilDeadlineIsRejected(t *testing.T) {
	// Why: a confirmed operation that remains explicitly consent_pending through expiry is terminally expired.
	signer := mustSignerFromSeed(t, 0x36)
	var calls atomic.Int32
	client := &http.Client{Transport: guiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return guiPairHTTPResponse(request, http.StatusAccepted, "application/json", `{"state":"consent_pending"}`), nil
	})}
	result, _ := confirmGUIPair(context.Background(), client, deps{pairTimeout: time.Second, confirmInterval: time.Millisecond},
		"https://gateway.example.test", validGUIPendingOperation(), ssh.FingerprintSHA256(signer.PublicKey()), signer, time.Now().Add(15*time.Millisecond), "", false)
	if result != guiResultRejected || calls.Load() < 2 {
		t.Fatalf("confirm result=%q requests=%d, want rejected after repeated pending replies", result, calls.Load())
	}
}

func TestGUIReplacementPromptWarnsAboutTheCurrentLocalRegistration(t *testing.T) {
	// Why: replacing another account's fixed policy makes this Mac's current registration unusable even when gateway preflight says new.
	f := newGUIPairFixture(t, nil)
	oldSigner := mustSignerFromSeed(t, 0x37)
	oldPolicy, err := json.Marshal(map[string]string{
		"host_key_store": "keychain", "host_key_fingerprint": ssh.FingerprintSHA256(oldSigner.PublicKey()),
		"principal_label": "previous-principal", "host_label": "previous-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.policy, oldPolicy, 0600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := f.run(t)
	got := strings.Join(f.nativeArgs, " ")
	want := "この Mac の今の登録（previous-principal / previous-host）は使えなくなります"
	if code != 0 || guiResult(t, stdout) != guiResultSucceeded || stderr != "" || f.nativeCalls.Load() != 1 || !strings.Contains(got, want) {
		t.Fatalf("code=%d result=%q stderr=%q native values=%q, want warning %q", code, guiResult(t, stdout), stderr, got, want)
	}
}
