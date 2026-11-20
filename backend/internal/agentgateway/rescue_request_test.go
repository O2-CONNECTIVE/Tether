package agentgateway_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

const (
	rescueBusyMessage = "a rescue request or connection is already active for this principal"
	rescueCodeMessage = "the rescue code is not valid"
	rescueRoleMessage = "this principal may not request a rescue"
	rescueNameMessage = "the gateway has no display_name or organization for this principal"
)

type requestRig struct {
	*rig
	clock *rescueTestClock
}

type rescueTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *rescueTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rescueTestClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *rescueTestClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func newRequestRig(t *testing.T, edit func(*agentgateway.Config)) *requestRig {
	t.Helper()
	clock := &rescueTestClock{now: time.Now()}
	cfg := agentgateway.Config{
		Verifier: verifier(t), Resource: testResource, RescueSigner: fixtureSigner(t, 0x49),
		Organization: "O2 CONNECTIVE", Clock: clock.Now, CallTimeout: 5 * time.Second,
		Principals: []agentgateway.Principal{
			{Label: label(0), Subject: subjects[0], DisplayName: "山田（サポート）", Roles: bothRoles, Signer: fixtureSigner(t, 0x21)},
			{Label: label(1), Subject: subjects[1], DisplayName: "鈴木", Roles: bothRoles, Signer: fixtureSigner(t, 0x22)},
			{Label: label(2), Subject: subjects[2], DisplayName: "佐藤", Roles: bothRoles, Signer: fixtureSigner(t, 0x23)},
			{Label: label(3), Subject: subjects[3], DisplayName: "田中", Roles: bothRoles, Signer: fixtureSigner(t, 0x24)},
		},
	}
	if edit != nil {
		edit(&cfg)
	}
	return &requestRig{rig: newModeRig(t, cfg), clock: clock}
}

func openRequestCode(t *testing.T, r *requestRig, n int) (agentgateway.RescueReception, ssh.Signer) {
	t.Helper()
	host := fixtureSigner(t, byte(0x70+n))
	hostNonce := bytes.Repeat([]byte{byte(0x31 + n)}, agentgateway.RescueNonceSize)
	gatewayNonce := bytes.Repeat([]byte{byte(0x41 + n)}, agentgateway.RescueNonceSize)
	proof, err := host.Sign(rand.Reader, agentgateway.RescueTranscript(r.gw.RescueEndpoint(), host.PublicKey().Marshal(), gatewayNonce, agentgateway.RescuePurposeNew, nil))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.gw.OpenRescue(agentgateway.RescueRequest{Key: host.PublicKey().Marshal(), HostNonce: hostNonce, GatewayNonce: gatewayNonce, Purpose: agentgateway.RescuePurposeNew, Proof: proof.Blob})
	if err != nil {
		t.Fatalf("OpenRescue: %v", err)
	}
	return res, host
}

// rescueTool keeps an absent tool or malformed response as data so red tests
// fail at the behavioral assertion instead of panicking on a type assertion.
func (c *caller) rescueTool(name string, args map[string]any) (state, message string, isError bool) {
	c.r.t.Helper()
	c.next++
	resp := c.r.do("POST", c.bearer, c.sid, rpc(c.next, "tools/call", map[string]any{"name": name, "arguments": args}), nil)
	result, ok := resp.message["result"].(map[string]any)
	if !ok {
		return "", string(resp.body), true
	}
	if v, ok := result["structuredContent"].(map[string]any); ok {
		state, _ = v["state"].(string)
	}
	isError, _ = result["isError"].(bool)
	if content, ok := result["content"].([]any); ok {
		for _, item := range content {
			if part, ok := item.(map[string]any); ok {
				if s, ok := part["text"].(string); ok {
					message = s
					break
				}
			}
		}
	}
	return
}

func requirePending(t *testing.T, c *caller, code string) {
	t.Helper()
	state, msg, failed := c.rescueTool("rescue_request", map[string]any{"code": code})
	if failed || state != "pending" {
		t.Fatalf("rescue_request = state %q, isError %v, message %q; want pending", state, failed, msg)
	}
}

func TestRescueRequestAllowUsesOnlyPerRequestClientKey(t *testing.T) {
	r := newRequestRig(t, nil)
	r.clock.Set(time.Now().Add(-60 * time.Second))
	res, hostKey := openRequestCode(t, r, 0)
	c := r.caller(0)
	requirePending(t, c, res.Code)
	identity := receiveIdentity(t, res)
	client, err := ssh.ParsePublicKey(identity.Client)
	if err != nil || client.Type() != ssh.KeyAlgoED25519 || bytes.Equal(identity.Client, fixtureSigner(t, 0x21).PublicKey().Marshal()) {
		t.Fatalf("request client key = %q; want a per-request Ed25519 key distinct from the principal signer", identity.Client)
	}
	transcript := agentgateway.RescueRequestTranscript(r.gw.RescueEndpoint(), res.ID, hostKey.PublicKey().Marshal(), identity.Nonce, identity.Client, identity.DisplayName, identity.Organization, identity.Profile)
	if err := fixtureSigner(t, 0x49).PublicKey().Verify(transcript, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: identity.Signature}); err != nil {
		t.Fatalf("request identity signature does not verify: %v", err)
	}
	if identity.Profile != agentgateway.RescueProfile {
		t.Fatalf("request profile = %q, want %q", identity.Profile, agentgateway.RescueProfile)
	}
	// The host only accepts C and pins K, so using the principal's long-term signer cannot open this channel.
	m, stream, err := r.prepareApprovedHost(0, 24*time.Hour, []ssh.PublicKey{client}, hostKey, 5*time.Second)
	if err != nil {
		t.Fatalf("prepare one-key host: %v", err)
	}
	r.clock.Set(m.dispatch.Deadline.Add(-24*time.Hour - 30*time.Second))
	answer := signAnswer(t, hostKey, r.gw.RescueEndpoint(), res.ID, identity, agentgateway.RescueAllow, m.dispatch)
	answer.MaxLifetimeAllowance = time.Minute
	if err := r.gw.AnswerRescue(res.ID, res.AttemptID, answer, stream, nil); err != nil {
		t.Fatalf("AnswerRescue(allow) = %v", err)
	}
	if stream.isClosed() {
		t.Fatal("allow connection was closed")
	}
	status, _, failed := c.rescueTool("rescue_request_status", map[string]any{})
	if failed || status != "allow" {
		t.Fatalf("rescue_request_status = %q, %v; want allow", status, failed)
	}
	bound := r.caller(0) // A fresh initialize must bind to the newly created route.
	state, errCall := bound.start("rescue-allow", "printf ready\n")
	if errCall {
		t.Fatalf("route tool start = %v, isError %v", state, errCall)
	}
	id := commandID(t, state)
	proc := m.proc(t)
	emit(t, proc, "ready\n")
	close(proc.events)
	close(proc.exited)
	if final := bound.awaitTerminal(id); final["phase"] != "exited" {
		t.Fatalf("route command terminal state = %v", final)
	}
	otherReception, _ := openRequestCode(t, r, 99)
	if _, msg, busy := c.rescueTool("rescue_request", map[string]any{"code": otherReception.Code}); !busy || msg != rescueBusyMessage {
		t.Fatalf("second request = %q, %v; want active-request refusal", msg, busy)
	}
	assertNoIdentity(t, otherReception)
}

func TestRescueRequestUsesConfiguredIdentityNotTokenClaims(t *testing.T) {
	r := newRequestRig(t, nil)
	res, _ := openRequestCode(t, r, 1)
	c := r.caller(0)
	// The signed token's display claims are untrusted metadata for this feature.
	claimsWithNames := claims(subjects[0])
	claimsWithNames["name"], claimsWithNames["org"] = "Mallory", "Evil"
	c.bearer = sign(t, edHeader(), claimsWithNames, issuerKey)
	requirePending(t, c, res.Code)
	identity := receiveIdentity(t, res)
	if identity.DisplayName != "山田（サポート）" || identity.Organization != "O2 CONNECTIVE" {
		t.Fatalf("identity = %q / %q; want configured values", identity.DisplayName, identity.Organization)
	}
}

func TestRescueRequestDenyAndTimeoutNeverOpenRoute(t *testing.T) {
	for _, answer := range []string{agentgateway.RescueDeny, agentgateway.RescueTimeout} {
		t.Run(answer, func(t *testing.T) {
			r := newRequestRig(t, nil)
			res, hostKey := openRequestCode(t, r, 2)
			c := r.caller(0)
			requirePending(t, c, res.Code)
			identity := receiveIdentity(t, res)
			client, _ := ssh.ParsePublicKey(identity.Client)
			m, stream, err := r.prepareApprovedHost(0, time.Minute, []ssh.PublicKey{client}, hostKey, 5*time.Second)
			if err != nil {
				t.Fatalf("prepare one-key host: %v", err)
			}
			if err := r.gw.AnswerRescue(res.ID, res.AttemptID, signAnswer(t, hostKey, r.gw.RescueEndpoint(), res.ID, identity, answer, m.dispatch), stream, nil); err == nil {
				t.Fatalf("AnswerRescue(%s) = nil; want no connection", answer)
			}
			if !stream.isClosed() {
				t.Fatalf("%s left the host stream open", answer)
			}
			assertDiscoveryWithoutRoute(t, r.caller(0))
			m.waitHostWithin(t, 5*time.Second)
			want := answer
			state, _, failed := c.rescueTool("rescue_request_status", map[string]any{})
			if failed || state != want {
				t.Fatalf("status = %q, %v; want %q", state, failed, want)
			}
			waitReceptionDone(t, res)
			discovery := r.caller(0)
			names := rescueToolNames(t, discovery)
			if !names["rescue_request"] || !names["rescue_request_status"] || names["rescue_start"] {
				t.Fatalf("post-denial discovery tools = %v", names)
			}
		})
	}
}

func TestRescueRequestRejectsLateAllow(t *testing.T) {
	r := newRequestRig(t, nil)
	res, hostKey := openRequestCode(t, r, 3)
	c := r.caller(0)
	requirePending(t, c, res.Code)
	identity := receiveIdentity(t, res)
	r.clock.Add(75 * time.Second)
	client, _ := ssh.ParsePublicKey(identity.Client)
	m, stream, err := r.prepareApprovedHost(0, time.Minute, []ssh.PublicKey{client}, hostKey, 5*time.Second)
	if err != nil {
		t.Fatalf("prepare one-key host: %v", err)
	}
	err = r.gw.AnswerRescue(res.ID, res.AttemptID, signAnswer(t, hostKey, r.gw.RescueEndpoint(), res.ID, identity, agentgateway.RescueAllow, m.dispatch), stream, nil)
	if err == nil || !stream.isClosed() {
		t.Fatalf("late allow = %v, stream closed %v; want refusal and close", err, stream.isClosed())
	}
	assertDiscoveryWithoutRoute(t, r.caller(0))
	m.waitHostWithin(t, 5*time.Second)
	state, _, failed := c.rescueTool("rescue_request_status", map[string]any{})
	if failed || state != "timeout" {
		t.Fatalf("late allow status = %q, %v; want timeout", state, failed)
	}
	waitReceptionDone(t, res)
}

func TestRescueRequestInvalidAnswerVariantsFailAndReleaseBusy(t *testing.T) {
	cases := []string{"wrong reception", "wrong nonce", "wrong digest", "wrong signing key", "deny signed as allow", "different signed dispatch", "different host key", "principal signer in allowed keys"}
	for i, name := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRequestRig(t, nil)
			res, hostKey := openRequestCode(t, r, 4+i)
			c := r.caller(0)
			requirePending(t, c, res.Code)
			identity := receiveIdentity(t, res)
			client, _ := ssh.ParsePublicKey(identity.Client)
			allowedKeys := []ssh.PublicKey{client}
			dispatchHost := hostKey
			if name == "different host key" {
				dispatchHost = fixtureSigner(t, 0x7d)
			}
			if name == "principal signer in allowed keys" {
				allowedKeys = append(allowedKeys, fixtureSigner(t, 0x21).PublicKey())
			}
			m, stream, err := r.prepareApprovedHost(0, time.Minute, allowedKeys, dispatchHost, 5*time.Second)
			if err != nil {
				t.Fatalf("prepare approved host: %v", err)
			}
			dispatch := m.dispatch
			signedID, signedNonce, signedDigest := res.ID, identity.Nonce, []byte(nil)
			signingKey, signedAnswer, sentAnswer := hostKey, agentgateway.RescueAllow, agentgateway.RescueAllow
			signedDispatch, sentDispatch := dispatch, dispatch
			switch name {
			case "wrong reception":
				signedID = bytes.Repeat([]byte{0xee}, len(res.ID))
			case "wrong nonce":
				signedNonce = bytes.Repeat([]byte{0xee}, len(identity.Nonce))
			case "wrong digest":
				signedDigest = []byte("not the request digest")
			case "wrong signing key":
				signingKey = fixtureSigner(t, 0x7e)
			case "deny signed as allow":
				signedAnswer = agentgateway.RescueDeny
			case "different signed dispatch":
				signedDispatch.HostKey = fixtureSigner(t, 0x7c).PublicKey().Marshal()
			}
			wrong := signAnswerFields(t, signingKey, hostKey.PublicKey(), r.gw.RescueEndpoint(), signedID, identity, signedNonce, signedDigest, signedAnswer, sentAnswer, signedDispatch, sentDispatch)
			if err := r.gw.AnswerRescue(res.ID, res.AttemptID, wrong, stream, nil); err == nil {
				t.Fatal("invalid answer was accepted")
			}
			if !stream.isClosed() {
				t.Fatal("invalid answer did not close the stream")
			}
			assertDiscoveryWithoutRoute(t, r.caller(0))
			m.waitHostWithin(t, 5*time.Second)
			state, _, failed := c.rescueTool("rescue_request_status", map[string]any{})
			if failed || state != "failed" {
				t.Fatalf("invalid answer status = %q, %v; want failed", state, failed)
			}
			waitReceptionDone(t, res)
			other, _ := openRequestCode(t, r, 20+i)
			requirePending(t, c, other.Code)
		})
	}
}

func TestRescueRequestBusyDoesNotConsumeSecondCode(t *testing.T) {
	r := newRequestRig(t, nil)
	first, _ := openRequestCode(t, r, 30)
	second, _ := openRequestCode(t, r, 31)
	c := r.caller(0)
	requirePending(t, c, first.Code)
	if _, msg, failed := c.rescueTool("rescue_request", map[string]any{"code": second.Code}); !failed || msg != rescueBusyMessage {
		t.Fatalf("same-principal second request = %q, %v; want busy", msg, failed)
	}
	assertNoIdentity(t, second)
	if _, msg, failed := r.caller(1).rescueTool("rescue_request", map[string]any{"code": second.Code}); failed {
		t.Fatalf("another principal could not use the unconsumed code: %q", msg)
	}
	receiveIdentity(t, second)
}

func TestRescueRequestReceptionEndingDuringConfirmationEndsRequest(t *testing.T) {
	r := newRequestRig(t, nil)
	res, hostKey := openRequestCode(t, r, 32)
	c := r.caller(0)
	requirePending(t, c, res.Code)
	identity := receiveIdentity(t, res)
	r.gw.EndRescue(res.ID)
	select {
	case <-res.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("EndRescue did not close the reception Done channel")
	}
	state, _, failed := c.rescueTool("rescue_request_status", map[string]any{})
	if failed || state != "ended" {
		t.Fatalf("ended reception status = %q, %v; want ended", state, failed)
	}
	assertDiscoveryWithoutRoute(t, r.caller(0))
	client, _ := ssh.ParsePublicKey(identity.Client)
	m, stream, err := r.prepareApprovedHost(0, time.Minute, []ssh.PublicKey{client}, hostKey, 5*time.Second)
	if err != nil {
		t.Fatalf("prepare one-key host: %v", err)
	}
	err = r.gw.AnswerRescue(res.ID, res.AttemptID, signAnswer(t, hostKey, r.gw.RescueEndpoint(), res.ID, identity, agentgateway.RescueAllow, m.dispatch), stream, nil)
	if err == nil || !stream.isClosed() {
		t.Fatalf("late answer after EndRescue = %v, closed %v; want refusal and close", err, stream.isClosed())
	}
	assertDiscoveryWithoutRoute(t, r.caller(0))
	m.waitHostWithin(t, 5*time.Second)
}

func TestRescueRequestInvalidCodesHaveOneResponse(t *testing.T) {
	for _, name := range []string{"disabled", "denied", "timed out", "spent", "unknown"} {
		t.Run(name, func(t *testing.T) {
			r := newRequestRig(t, nil)
			res, hostKey := openRequestCode(t, r, 40)
			code := "000000000000"
			switch name {
			case "disabled":
				r.gw.EndRescue(res.ID)
				code = res.Code
			case "denied", "timed out", "spent":
				first := r.caller(1)
				requirePending(t, first, res.Code)
				identity := receiveIdentity(t, res)
				code = res.Code
				if name != "spent" {
					stream, peer := memPair()
					answer := agentgateway.RescueDeny
					if name == "timed out" {
						answer = agentgateway.RescueTimeout
					}
					_ = r.gw.AnswerRescue(res.ID, res.AttemptID, signAnswer(t, hostKey, r.gw.RescueEndpoint(), res.ID, identity, answer, agentgateway.Dispatch{}), stream, nil)
					_ = peer.Close()
					waitReceptionDone(t, res)
				}
			}
			_, msg, failed := r.caller(0).rescueTool("rescue_request", map[string]any{"code": code})
			if !failed || msg != rescueCodeMessage {
				t.Fatalf("invalid-code response = %q, %v; want identical %q", msg, failed, rescueCodeMessage)
			}
			assertNoIdentity(t, res)
		})
	}
}

func TestRescueRequestRequiresConfiguredDisplayNameAndOrganization(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*agentgateway.Config)
	}{
		{"missing display name", func(c *agentgateway.Config) { c.Principals[0].DisplayName = "" }},
		{"missing organization", func(c *agentgateway.Config) { c.Organization = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRequestRig(t, tc.edit)
			res, _ := openRequestCode(t, r, 50)
			if _, msg, failed := r.caller(0).rescueTool("rescue_request", map[string]any{"code": res.Code}); !failed || msg != rescueNameMessage {
				t.Fatalf("missing identity setting response = %q, %v; want %q", msg, failed, rescueNameMessage)
			}
			// With no organization no principal may request, so only the display
			// name case can show the code was not spent.
			if tc.name == "missing organization" {
				assertNoIdentity(t, res)
				return
			}
			if _, msg, failed := r.caller(1).rescueTool("rescue_request", map[string]any{"code": res.Code}); failed {
				t.Fatalf("rejected request consumed code: %q", msg)
			}
			receiveIdentity(t, res)
		})
	}
}

func TestRescueRequestRequiresRescueOperatorBeforeRedeem(t *testing.T) {
	r := newRequestRig(t, func(c *agentgateway.Config) {
		c.Principals[0].Roles = []agentgateway.Role{agentgateway.RoleResidentOwner}
	})
	res, _ := openRequestCode(t, r, 60)
	if _, msg, failed := r.caller(0).rescueTool("rescue_request", map[string]any{"code": res.Code}); !failed || msg != rescueRoleMessage {
		t.Fatalf("resident-only response = %q, %v; want %q", msg, failed, rescueRoleMessage)
	}
	if _, msg, failed := r.caller(1).rescueTool("rescue_request", map[string]any{"code": res.Code}); failed {
		t.Fatalf("role refusal consumed code: %q", msg)
	}
	receiveIdentity(t, res)
}

func TestRescueIdentityConfigurationValidation(t *testing.T) {
	name64 := strings.Repeat("山", 64)
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"Japanese name and organization", name64, true},
		{"empty", "", false},
		{"65 runes", name64 + "山", false},
		{"bell", "山\u0007田", false},
		{"newline", "山\n田", false},
		{"bidi override", "山\u202e田", false},
		{"bidi isolate", "山\u2066田", false},
		{"not a string", 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := configDoc(t, func(d map[string]any) {
				d["organization"] = tc.value
				people := d["principals"].([]any)
				p := people[0].(map[string]any)
				p["display_name"] = tc.value
			})
			parsed, err := agentgateway.ParseConfig(doc)
			if tc.valid {
				if err != nil || parsed.Organization != "山"+strings.Repeat("山", 63) || parsed.Principals[0].DisplayName != name64 {
					t.Fatalf("ParseConfig lost valid identity settings: %+v, %v", parsed, err)
				}
				cfg, err := parsed.GatewayConfig()
				if err != nil || cfg.Organization != parsed.Organization || cfg.Principals[0].DisplayName != name64 {
					t.Fatalf("GatewayConfig identity settings = %+v, %v", cfg, err)
				}
				access, err := agentgateway.ParseConfig(accessConfigDoc(t, func(d map[string]any) {
					d["organization"] = name64
					people := d["principals"].([]any)
					people[0].(map[string]any)["display_name"] = name64
				}))
				if err != nil {
					t.Fatalf("Access ParseConfig identity values: %v", err)
				}
				accessGateway, err := access.GatewayConfig()
				if err != nil || accessGateway.Organization != name64 || accessGateway.Principals[0].DisplayName != name64 {
					t.Fatalf("Access GatewayConfig identity values = %+v, %v", accessGateway, err)
				}
				return
			}
			if !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("ParseConfig(%s) = %v; want ErrConfig", fmt.Sprint(tc.value), err)
			}
		})
	}
	invalidValues := []string{strings.Repeat("山", 65), "山\u0007", "山\n", "山\u202e", "山\u2066"}
	for _, value := range invalidValues {
		cfg := agentgateway.Config{Verifier: verifier(t), Resource: testResource, Organization: value,
			Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0], DisplayName: "山田", Roles: bothRoles, Signer: fixtureSigner(t, 0x21)}}}
		if _, err := agentgateway.New(cfg); !errors.Is(err, agentgateway.ErrConfig) {
			t.Fatalf("New(organization %q) = %v; want ErrConfig", value, err)
		}
	}
	for _, value := range invalidValues {
		cfg := agentgateway.Config{Verifier: verifier(t), Resource: testResource, Organization: "O2 CONNECTIVE",
			Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0], DisplayName: value, Roles: bothRoles, Signer: fixtureSigner(t, 0x21)}}}
		if _, err := agentgateway.New(cfg); !errors.Is(err, agentgateway.ErrConfig) {
			t.Fatalf("New(display_name %q) = %v; want ErrConfig", value, err)
		}
	}
	cfg := agentgateway.Config{Verifier: verifier(t), Resource: testResource,
		Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0], Roles: bothRoles}}}
	if g, err := agentgateway.New(cfg); err != nil {
		t.Fatalf("New must allow missing display_name and organization until a rescue request: %v", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = g.Close(ctx)
	}
}

// OpenRescue's request payload is delivered once and must carry an authentic
// proof of the configured display values and the request-scoped client key.
func receiveIdentity(t *testing.T, res agentgateway.RescueReception) agentgateway.RescueIdentity {
	t.Helper()
	select {
	case identity, ok := <-res.Request:
		if !ok {
			t.Fatal("rescue reception ended before identity was delivered")
		}
		return identity
	case <-time.After(5 * time.Second):
		t.Fatal("no identity request reached the host")
		return agentgateway.RescueIdentity{}
	}
}

func assertNoIdentity(t *testing.T, res agentgateway.RescueReception) {
	t.Helper()
	select {
	case identity := <-res.Request:
		t.Fatalf("invalid or busy request reached host: %+v", identity)
	default:
	}
}

func waitReceptionDone(t *testing.T, res agentgateway.RescueReception) {
	t.Helper()
	select {
	case <-res.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("rescue reception was not closed")
	}
}

func rescueToolNames(t *testing.T, c *caller) map[string]bool {
	t.Helper()
	c.next++
	resp := c.r.do("POST", c.bearer, c.sid, rpc(c.next, "tools/list", nil), nil)
	result, _ := resp.message["result"].(map[string]any)
	rows, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, row := range rows {
		if tool, ok := row.(map[string]any); ok {
			if name, ok := tool["name"].(string); ok {
				names[name] = true
			}
		}
	}
	return names
}

func assertDiscoveryWithoutRoute(t *testing.T, c *caller) {
	t.Helper()
	names := rescueToolNames(t, c)
	if !names["rescue_request"] || !names["rescue_request_status"] || names["rescue_start"] {
		t.Fatalf("discovery session tools after refusal = %v", names)
	}
}

func signAnswer(t *testing.T, key ssh.Signer, endpoint string, id []byte, identity agentgateway.RescueIdentity, answer string, d agentgateway.Dispatch) agentgateway.RescueAnswer {
	t.Helper()
	return signAnswerFields(t, key, key.PublicKey(), endpoint, id, identity, identity.Nonce, nil, answer, answer, d, d)
}

func signAnswerFields(t *testing.T, key ssh.Signer, hostPublic ssh.PublicKey, endpoint string, signedID []byte, identity agentgateway.RescueIdentity, signedNonce, digestOverride []byte, signedAnswer, sentAnswer string, signedDispatch, sentDispatch agentgateway.Dispatch) agentgateway.RescueAnswer {
	t.Helper()
	digest := sha256.Sum256(agentgateway.RescueRequestTranscript(endpoint, signedID, hostPublic.Marshal(), identity.Nonce, identity.Client, identity.DisplayName, identity.Organization, identity.Profile))
	var digestBytes []byte = digest[:]
	if digestOverride != nil {
		digestBytes = digestOverride
	}
	sig, err := key.Sign(rand.Reader, agentgateway.RescueAnswerTranscript(signedID, signedNonce, digestBytes, signedAnswer, signedDispatch))
	if err != nil {
		t.Fatal(err)
	}
	return agentgateway.RescueAnswer{Answer: sentAnswer, Dispatch: sentDispatch, Signature: sig.Blob}
}

// A mistyped code must not lock the operator out: the refusal ends the
// attempt, so the right code still goes through (mutation M5 survived).
func TestRescueRequestInvalidCodeDoesNotLeavePrincipalBusy(t *testing.T) {
	r := newRequestRig(t, nil)
	res, _ := openRequestCode(t, r, 0)
	c := r.caller(0)
	if _, msg, failed := c.rescueTool("rescue_request", map[string]any{"code": "000000000000"}); !failed || msg != rescueCodeMessage {
		t.Fatalf("mistyped code = %q, %v; want %q", msg, failed, rescueCodeMessage)
	}
	requirePending(t, c, res.Code)
	receiveIdentity(t, res)
}

// One request carries one answer. A replayed allow on a reception whose
// channel is open must not tear down that session or report it as failed
// (mutation M12 survived).
func TestRescueRequestSecondAnswerAfterAllowIsRefused(t *testing.T) {
	r := newRequestRig(t, nil)
	res, hostKey := openRequestCode(t, r, 0)
	c := r.caller(0)
	requirePending(t, c, res.Code)
	identity := receiveIdentity(t, res)
	client, err := ssh.ParsePublicKey(identity.Client)
	if err != nil {
		t.Fatal(err)
	}
	m, stream, err := r.prepareApprovedHost(0, time.Minute, []ssh.PublicKey{client}, hostKey, 5*time.Second)
	if err != nil {
		t.Fatalf("prepare one-key host: %v", err)
	}
	answer := signAnswer(t, hostKey, r.gw.RescueEndpoint(), res.ID, identity, agentgateway.RescueAllow, m.dispatch)
	if err := r.gw.AnswerRescue(res.ID, res.AttemptID, answer, stream, nil); err != nil {
		t.Fatalf("AnswerRescue(allow) = %v", err)
	}
	replay, peer := net.Pipe()
	defer peer.Close()
	if err := r.gw.AnswerRescue(res.ID, res.AttemptID, answer, replay, nil); err == nil {
		t.Fatal("second AnswerRescue on an answered reception = nil; want refusal")
	}
	if stream.isClosed() {
		t.Fatal("replayed answer closed the open channel")
	}
	select {
	case <-res.Done:
		t.Fatal("replayed answer ended the reception of an open channel")
	default:
	}
	if status, _, failed := c.rescueTool("rescue_request_status", map[string]any{}); failed || status != agentgateway.RescueAllow {
		t.Fatalf("status after replayed answer = %q, %v; want allow", status, failed)
	}
}
