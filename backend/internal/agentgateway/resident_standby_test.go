package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// standbyControl is the resident control with a standby report. It records
// every read and refuses, at once, any call that could reach the Mac.
type standbyControl struct {
	*residentControlFixture
	mu     sync.Mutex
	live   string // the generation of the control that is up
	issued string // what ResidentGeneration says: "" once the live control has issued its last work sequence
	ended  bool
	report ResidentStandby
	reads  []string
	begins int
}

func (c *standbyControl) ResidentGeneration(string, string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.issued
}

// ResidentStandby answers as the transport does: false unless the control asked
// for is up; a control that is up and has no report yet has revision 0.
func (c *standbyControl) ResidentStandby(host, principal, generation string) (ResidentStandby, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads = append(c.reads, host+"/"+principal+"/"+generation)
	if c.ended || generation != c.live {
		return ResidentStandby{}, false
	}
	return c.report, true
}
func (c *standbyControl) BeginResidentFixed(context.Context, string, string, string, string, time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.begins++
	return errors.New("standby test: no work may start")
}
func (c *standbyControl) BeginResidentRun(context.Context, string, string, string, string, time.Time, ResidentRunScope) error {
	return c.BeginResidentFixed(context.Background(), "", "", "", "", time.Time{})
}
func (c *standbyControl) counts() (reads, begins int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reads), c.begins
}

func standbyRig(t *testing.T) (*Gateway, *residentSession, *standbyControl, time.Time) {
	t.Helper()
	g, s, _, fixture := residentEvidenceRig(t)
	t.Cleanup(func() { g.Close(context.Background()) })
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	g.clock = func() time.Time { return now }
	control := &standbyControl{residentControlFixture: fixture, live: s.generation, issued: s.generation}
	g.resident.control = control
	return g, s, control, now
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}

// The AI must learn, before standby ends, when it ends and how long is left, so
// it can wrap up its work (#399). The end is the Mac's; the time left is counted
// here, and never below zero.
func TestResidentStandbyStatusReturnsEndAndRemaining(t *testing.T) {
	_, s, control, now := standbyRig(t)
	for _, c := range []struct {
		report ResidentStandby
		want   string
	}{
		{ResidentStandby{Revision: 3, Standby: "on", Until: now.Add(90*time.Minute + 1500*time.Millisecond)},
			`{"remaining_ms":5401500,"schema_version":1,"standby":"on","standby_until":"2026-11-19T10:30:01.500Z"}`},
		// A Mac in another zone still reads in UTC.
		{ResidentStandby{Revision: 3, Standby: "on", Until: now.Add(time.Hour).In(time.FixedZone("JST", 9*3600))},
			`{"remaining_ms":3600000,"schema_version":1,"standby":"on","standby_until":"2026-11-19T10:00:00.000Z"}`},
		// An end already passed here is no time left, even before the Mac stops.
		{ResidentStandby{Revision: 4, Standby: "on", Until: now.Add(-time.Second)},
			`{"remaining_ms":0,"schema_version":1,"standby":"on","standby_until":"2026-11-19T08:59:59.000Z"}`},
		// A Mac that admits no new work publishes no end.
		{ResidentStandby{Revision: 5, Standby: "on"},
			`{"remaining_ms":0,"schema_version":1,"standby":"on","standby_until":null}`},
		{ResidentStandby{Revision: 6, Standby: "stopping"},
			`{"remaining_ms":0,"schema_version":1,"standby":"stopping","standby_until":null}`},
	} {
		control.report = c.report
		if got := jsonOf(t, s.StandbyStatus(context.Background())); got != c.want {
			t.Fatalf("status of %+v = %s; want %s", c.report, got, c.want)
		}
	}
	// Only this session's principal, its enrolled host and its control
	// generation were asked: no other host's standby can be read here.
	for _, read := range control.reads {
		if read != "host1/member1/"+s.generation {
			t.Fatalf("read %q", read)
		}
	}
	if reads, _ := control.counts(); reads != 5 {
		t.Fatalf("reads = %d", reads)
	}
}

// The answer comes from one read of the control (#412 review): a control that
// is up is never told to the AI as ended, so it does not start over while its
// work goes on, and an ended one is never read as up.
func TestResidentStandbyStatusErrors(t *testing.T) {
	g, s, control, now := standbyRig(t)
	status := func(s *residentSession, ctx context.Context) string {
		t.Helper()
		return jsonOf(t, s.StandbyStatus(ctx))
	}
	if got := status(s, context.Background()); got != `{"error":"not_reported"}` {
		t.Fatal("control up before the Mac reported:", got)
	}
	// A control that cannot report standby reports none.
	g.resident.control = control.residentControlFixture
	if got := status(s, context.Background()); got != `{"error":"not_reported"}` {
		t.Fatal("control without reports:", got)
	}
	g.resident.control = control
	control.report = ResidentStandby{Revision: 1, Standby: "on", Until: now.Add(time.Hour)}
	// A control that has issued its last work sequence is still up: its
	// standby is read, not told as ended.
	control.issued = ""
	if got := status(s, context.Background()); got != `{"remaining_ms":3600000,"schema_version":1,"standby":"on","standby_until":"2026-11-19T10:00:00.000Z"}` {
		t.Fatal("control up after its last sequence:", got)
	}
	control.issued = s.generation
	ended := *s
	ended.generation = strings.Repeat("b", 64)
	if got := status(&ended, context.Background()); got != `{"error":"control_ended","reinitialize_required":true}` {
		t.Fatal("another generation:", got)
	}
	// The control ends between the session's checks and the read.
	control.ended = true
	if got := status(s, context.Background()); got != `{"error":"control_ended","reinitialize_required":true}` {
		t.Fatal("ended control:", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := status(s, ctx); got != `{"error":"not_read"}` {
		t.Fatal("cancelled read:", got)
	}
}

// Standby is extended only by the person at the Mac (#399). No tool, name or
// argument an MCP client sends reaches anything but a read.
func TestResidentStandbyStatusThroughAuthenticatedGatewayCannotExtend(t *testing.T) {
	g, s, control, now := standbyRig(t)
	control.report = ResidentStandby{Revision: 3, Standby: "on", Until: now.Add(time.Hour)}
	sign := func(subject string) string {
		header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
		claims, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://example.test/mcp", "sub": subject, "scope": "scope", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
		enc := base64.RawURLEncoding
		input := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
		key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32))
		return input + "." + enc.EncodeToString(ed25519.Sign(key, []byte(input)))
	}
	call := func(bearer, sid, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://example.test/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		if sid != "" {
			req.Header.Set(HeaderSession, sid)
			req.Header.Set(HeaderProtocol, "2025-06-18")
		}
		res := httptest.NewRecorder()
		g.ServeHTTP(res, req)
		return res
	}
	bearer := sign("subject")
	init := call(bearer, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`)
	sid := init.Header().Get(HeaderSession)
	if init.Code != http.StatusOK || sid == "" {
		t.Fatal(init.Code, init.Body.String())
	}
	if n := call(bearer, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); n.Code != http.StatusAccepted {
		t.Fatal(n.Code, n.Body.String())
	}
	listing := call(bearer, sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if !strings.Contains(listing.Body.String(), `"name":"tether_standby_status"`) {
		t.Fatal("tool not offered:", listing.Body.String())
	}
	want := jsonOf(t, s.StandbyStatus(context.Background()))
	for _, args := range []string{`{}`, ``} {
		params := `{"name":"tether_standby_status"`
		if args != "" {
			params += `,"arguments":` + args
		}
		res := call(bearer, sid, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+params+`}}`)
		var envelope struct {
			Result struct {
				IsError           bool            `json:"isError"`
				StructuredContent json.RawMessage `json:"structuredContent"`
			}
		}
		if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &envelope) != nil || envelope.Result.IsError || string(envelope.Result.StructuredContent) != want {
			t.Fatal(args, res.Code, res.Body.String())
		}
	}
	if res := call(sign("other-subject"), sid, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"tether_standby_status","arguments":{}}}`); res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
		t.Fatal("another subject read standby", res.Code, res.Body.String())
	}
	readsBefore, _ := control.counts()
	for _, params := range []string{
		`{"name":"tether_standby_status","arguments":{"hours":24}}`,
		`{"name":"tether_standby_status","arguments":{"extend":true}}`,
		`{"name":"tether_standby_status","arguments":{"standby_until":"2099-01-01T00:00:00.000Z"}}`,
		`{"name":"tether_standby_status","arguments":{"remaining_ms":86400000}}`,
		`{"name":"tether_standby_status","arguments":{"request_id":"0123456789abcdef0123456789abcdef"}}`,
		`{"name":"tether_standby_status","arguments":null}`,
		`{"name":"tether_standby_status","arguments":[]}`,
		`{"name":"tether_standby_extend","arguments":{"hours":24}}`,
		`{"name":"tether_extend_standby","arguments":{}}`,
		`{"name":"tether_standby_on","arguments":{"hours":24}}`,
	} {
		res := call(bearer, sid, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":`+params+`}`)
		var envelope struct{ Error *struct{ Code int } }
		if json.Unmarshal(res.Body.Bytes(), &envelope) != nil || envelope.Error == nil || envelope.Error.Code != -32602 {
			t.Fatal("not refused as invalid params:", params, res.Code, res.Body.String())
		}
	}
	if reads, begins := control.counts(); reads != readsBefore || begins != 0 {
		t.Fatalf("refused calls reached the control: reads %d -> %d, begins %d", readsBefore, reads, begins)
	}
	if s.entry.current != nil || len(s.entry.works) != 0 || s.principal.route != nil {
		t.Fatal("a standby call acquired work")
	}
	if got := jsonOf(t, s.StandbyStatus(context.Background())); got != want {
		t.Fatal("standby changed:", got)
	}
}
