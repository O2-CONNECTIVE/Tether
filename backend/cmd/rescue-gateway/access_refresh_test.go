package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"strings"
	"testing"
	"time"
)

func rotatingLocalDoc(t *testing.T) map[string]any {
	t.Helper()
	doc := accessLocalDoc(t)
	gw := doc["gateway"].(map[string]any)
	gw["access_issuer"] = "https://synthetic-team.cloudflareaccess.com"
	gw["access_key_source"] = "cloudflare_certs"
	delete(gw, "access_keys")
	return doc
}

// The normal CLI must validate this explicit source-only opt-in, then reach
// the same signer gate. The deliberately refused signer prevents all I/O.
func TestEntryRotatingAccessOptInReachesSignerValidation(t *testing.T) {
	r := newRecorder(t, encode(t, rotatingLocalDoc(t)))
	r.failKey = fingerprint(t, 0x11)
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 2 ||
		r.trace() != "read load:member-1" || !strings.Contains(diagnostic.String(), "署名鍵を取得できませんでした") {
		t.Fatalf("exit=%d trace=%q diagnostic=%q", code, r.trace(), diagnostic.String())
	}
}

func TestEntryRotatingAccessRequiresInitialRefresh(t *testing.T) {
	r := newRecorder(t, encode(t, rotatingLocalDoc(t)))
	d := r.deps()
	d.refreshAccessKeys = func(ctx context.Context, v *agentgateway.AccessVerifier) error {
		r.log("refresh")
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("unbounded refresh")
		}
		if _, err := v.Verify("untrusted"); !errors.Is(err, agentgateway.ErrToken) {
			t.Error("empty verifier accepted")
		}
		return errors.New("synthetic provider secret")
	}
	d.waitAccessRefresh = func(context.Context, time.Duration) bool { t.Error("retry after initial failure"); return false }
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, d); code != 2 ||
		r.trace() != "read load:member-1 load:member-2 refresh" ||
		!strings.Contains(diagnostic.String(), "Access公開鍵を取得できませんでした") {
		t.Fatalf("exit=%d trace=%q diagnostic=%q", code, r.trace(), diagnostic.String())
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestEntryRotatingAccessOwnsSerialRefreshAndJoinsCancellation(t *testing.T) {
	for _, flag := range []string{"-config", "-pilot-config", "-discovery-pilot-config"} {
		t.Run(flag, func(t *testing.T) {
			doc := rotatingLocalDoc(t)
			if flag == "-discovery-pilot-config" {
				doc = map[string]any{"mode": "discovery_only", "gateway": doc["gateway"]}
			}
			r := newRecorder(t, encode(t, doc))
			r.listener = newFakeListener()
			d := r.deps()
			ticks, waiting, entered, joined := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			count := 0
			var verifier *agentgateway.AccessVerifier
			d.refreshAccessKeys = func(ctx context.Context, v *agentgateway.AccessVerifier) error {
				count++
				if count == 1 {
					verifier = v
					r.log("refresh")
					return nil // no key injection: the real verifier remains fail-closed
				}
				if count != 2 || v != verifier {
					t.Error("parallel/replaced verifier")
				}
				close(entered)
				<-ctx.Done()
				close(joined)
				return ctx.Err()
			}
			d.waitAccessRefresh = func(ctx context.Context, interval time.Duration) bool {
				if interval != 60*time.Second {
					t.Error("changed configured interval")
				}
				select {
				case waiting <- struct{}{}:
				default:
				}
				select {
				case <-ctx.Done():
					return false
				case <-ticks:
					return true
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var diagnostic bytes.Buffer
			exited := make(chan int, 1)
			go func() { exited <- execute(ctx, []string{flag, configPath}, &diagnostic, d) }()
			select {
			case <-r.listened:
			case code := <-exited:
				t.Fatalf("early exit=%d", code)
			case <-time.After(3 * time.Second):
				t.Fatal("no listen")
			}
			if !strings.Contains(r.trace(), "refresh listen:") {
				t.Fatal("listen preceded initial refresh", r.trace())
			}
			<-waiting
			// The CLI uses the real configured, initially empty rotating verifier.
			// Invalid/unknown assertions cannot cause a refresh or any implicit retry.
			for i := 0; i < 3; i++ {
				if code, _ := roundTrip(t, r.listener, "POST /mcp HTTP/1.1\r\nHost: gateway.example.invalid\r\nConnection: close\r\nCf-Access-Jwt-Assertion: untrusted\r\nContent-Type: application/json\r\nAccept: application/json\r\nContent-Length: 2\r\n\r\n{}"); code != 403 {
					t.Fatalf("empty verifier status=%d", code)
				}
			}
			ticks <- struct{}{}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("no scheduled refresh")
			}
			cancel()
			select {
			case code := <-exited:
				if code != 0 {
					t.Fatalf("exit=%d diagnostic=%q", code, diagnostic.String())
				}
				select {
				case <-joined:
				default:
					t.Fatal("returned before refresh joined")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("refresh not cancelled/joined")
			}
			if count != 2 {
				t.Fatalf("refresh calls=%d", count)
			}
			checkDiagnostic(t, diagnostic.String())
		})
	}
}

func TestEntryRotatingAccessCancelsInitialFetchWithoutListening(t *testing.T) {
	r := newRecorder(t, encode(t, rotatingLocalDoc(t)))
	d := r.deps()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.refreshAccessKeys = func(bounded context.Context, _ *agentgateway.AccessVerifier) error {
		r.log("refresh")
		cancel()
		<-bounded.Done()
		return nil // A late/synthetic success must not override cancellation.
	}
	d.waitAccessRefresh = func(context.Context, time.Duration) bool { t.Error("scheduler started"); return false }
	var diagnostic bytes.Buffer
	if code := execute(ctx, []string{"-config", configPath}, &diagnostic, d); code != 2 || strings.Contains(r.trace(), "listen:") {
		t.Fatalf("exit=%d trace=%q", code, r.trace())
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestEntryRotatingAccessListenFailureJoinsOwner(t *testing.T) {
	r := newRecorder(t, encode(t, rotatingLocalDoc(t)))
	d := r.deps()
	stopped := make(chan struct{})
	d.refreshAccessKeys = func(context.Context, *agentgateway.AccessVerifier) error { return nil }
	d.waitAccessRefresh = func(ctx context.Context, _ time.Duration) bool { <-ctx.Done(); close(stopped); return false }
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, d); code != 1 {
		t.Fatalf("exit=%d", code)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("listen failure leaked owner")
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestEntryRotatingAccessFailureWaitsBeforeNextAttempt(t *testing.T) {
	r := newRecorder(t, encode(t, rotatingLocalDoc(t)))
	r.listener = newFakeListener()
	d := r.deps()
	ticks, waiting := make(chan struct{}), make(chan struct{}, 1)
	count := 0
	d.refreshAccessKeys = func(context.Context, *agentgateway.AccessVerifier) error {
		count++
		if count == 1 {
			return nil
		}
		return errors.New("synthetic credential/header/URL detail")
	}
	d.waitAccessRefresh = func(ctx context.Context, interval time.Duration) bool {
		if interval != time.Minute {
			t.Error("failure changed retry interval")
		}
		waiting <- struct{}{}
		select {
		case <-ctx.Done():
			return false
		case <-ticks:
			return true
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- execute(ctx, []string{"-config", configPath}, &diagnostic, d) }()
	select {
	case <-r.listened:
	case <-time.After(3 * time.Second):
		t.Fatal("no listen")
	}
	<-waiting
	ticks <- struct{}{}
	<-waiting // the failed attempt completed and re-entered the full delay
	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatal(code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("not joined")
	}
	if count != 2 || !strings.Contains(diagnostic.String(), "既存の有効期限は延長しません") {
		t.Fatalf("calls=%d diagnostic=%q", count, diagnostic.String())
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestEntryRejectsAmbiguousRotatingAccessBeforeKeyOrFetch(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"both sources":    func(g map[string]any) { g["access_keys"] = accessGatewayDoc(t)["access_keys"] },
		"unknown source":  func(g map[string]any) { g["access_key_source"] = "https://attacker.invalid/certs" },
		"boolean source":  func(g map[string]any) { g["access_key_source"] = true },
		"empty source":    func(g map[string]any) { g["access_key_source"] = "" },
		"null source":     func(g map[string]any) { g["access_key_source"] = nil },
		"non-team issuer": func(g map[string]any) { g["access_issuer"] = accessIssuer },
		"issuer path":     func(g map[string]any) { g["access_issuer"] = "https://synthetic-team.cloudflareaccess.com/path" },
		"policy override": func(g map[string]any) { g["access_refresh_seconds"] = 1 },
		"oauth mixed":     func(g map[string]any) { g["auth_mode"] = "oauth_bearer_jwt" },
	} {
		t.Run(name, func(t *testing.T) {
			doc := rotatingLocalDoc(t)
			edit(doc["gateway"].(map[string]any))
			r := newRecorder(t, encode(t, doc))
			d := r.deps()
			d.refreshAccessKeys = func(context.Context, *agentgateway.AccessVerifier) error {
				t.Fatal("invalid config fetched")
				return nil
			}
			if code := execute(context.Background(), []string{"-config", configPath}, nil, d); code != 2 || r.trace() != "read" {
				t.Fatalf("exit=%d trace=%q", code, r.trace())
			}
		})
	}
}

func TestNativeAccessRefreshAndCancelledWait(t *testing.T) {
	d := nativeDeps()
	if d.refreshAccessKeys == nil || d.waitAccessRefresh == nil {
		t.Fatal("native scheduler unwired")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.waitAccessRefresh(ctx, time.Minute) {
		t.Fatal("cancelled wait scheduled work")
	}
	if err := d.refreshAccessKeys(ctx, nil); !errors.Is(err, agentgateway.ErrAccessKeyRefresh) {
		t.Fatal("native refresh not delegated to verifier")
	}
}

func TestAccessRefreshJoinReportsPendingOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	if err := joinAccessRefresh(cancel, done, time.Millisecond); err == nil || ctx.Err() == nil {
		t.Fatal("pending owner reported clean or not cancelled")
	}
	close(done)
	if err := joinAccessRefresh(cancel, done, time.Second); err != nil {
		t.Fatal("confirmed owner not joined")
	}
}

func TestStaticModeDoesNotStartRefreshOwner(t *testing.T) {
	d := deps{
		refreshAccessKeys: func(context.Context, *agentgateway.AccessVerifier) error { t.Fatal("static mode fetched"); return nil },
		waitAccessRefresh: func(context.Context, time.Duration) bool { t.Fatal("static mode scheduled"); return false },
	}
	stop, err := startAccessRefresh(context.Background(), agentgateway.FileConfig{}, nil, nil, d)
	if err != nil || stop == nil || stop() != nil {
		t.Fatal("static composition changed")
	}
}
