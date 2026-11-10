package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

func listWork(id, generation string) *residentWork {
	return &residentWork{binding: ResidentWorkBinding{RequestID: id, Generation: generation}, phase: "pending", ownerCleanup: "pending", gatewayCleanup: "pending", output: []map[string]any{{"data": "PRIVATE OUTPUT"}}}
}
func TestResidentListScopePaginationAndDetachedSummaries(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	for n := 1; n <= 35; n++ {
		s.entry.works[id(n)] = listWork(id(n), s.generation)
		if n >= 32 {
			releaseListWork(t, s, id(n), false)
		}
	}
	s.entry.works[id(99)] = listWork(id(99), "other-generation")
	other := &residentSession{gateway: g, entry: &residentEntry{works: map[string]*residentWork{id(80): listWork(id(80), s.generation)}}, generation: s.generation}
	if page := s.FixedList(context.Background(), "", 32); len(page["executions"].([]map[string]any)) != 32 {
		t.Fatal(page)
	}
	first := s.FixedList(context.Background(), "", 30)
	entries := first["executions"].([]map[string]any)
	if len(entries) != 30 || first["next_cursor"] != id(30) || first["schema_version"] != 1 {
		t.Fatal(first)
	}
	for n, e := range entries {
		if e["request_id"] != id(n+1) || len(e) != 8 || e["result_released"] != false || e["owner_cleanup"] != "pending" {
			t.Fatal(e)
		}
	}
	// Transition one original retained record into the existing replay tombstone.
	releaseListWork(t, s, id(31), true)
	second := s.FixedList(context.Background(), first["next_cursor"].(string), 32)
	tail := second["executions"].([]map[string]any)
	if len(tail) != 5 || second["next_cursor"] != "" || tail[0]["request_id"] != id(31) || tail[0]["result_released"] != true || tail[0]["owner_cleanup"] != "confirmed" {
		t.Fatal(second)
	}
	tail[0]["phase"] = "MUTATED"
	entries[0]["phase"] = "MUTATED"
	if s.FixedStatus(context.Background(), id(31))["phase"] != "finished" || s.entry.works[id(1)].phase != "pending" {
		t.Fatal("list aliases owned records")
	}
	if own := other.FixedList(context.Background(), "", 32)["executions"].([]map[string]any); len(own) != 1 || own[0]["request_id"] != id(80) {
		t.Fatal(own)
	}
	old := *s
	old.generation = "unknown-generation"
	if empty := old.FixedList(context.Background(), "", 32)["executions"].([]map[string]any); len(empty) != 0 {
		t.Fatal("foreign generation leaked", empty)
	}
	// Keyset remains usable when the cursor record itself is absent.
	delete(s.entry.works, id(30))
	if got := s.FixedList(context.Background(), id(30), 32)["executions"].([]map[string]any); len(got) != 5 {
		t.Fatal(got)
	}
	// A new smaller ID is found by refreshing, not falsely promised by old pages.
	s.entry.works[id(0)] = listWork(id(0), s.generation)
	if got := s.FixedList(context.Background(), "", 1)["executions"].([]map[string]any); got[0]["request_id"] != id(0) {
		t.Fatal(got)
	}
	for _, q := range []struct {
		cursor string
		limit  int
	}{{"bad", 1}, {"", 0}, {"", 33}} {
		if got := s.FixedList(context.Background(), q.cursor, q.limit); got["error"] != "invalid" {
			t.Fatal(got)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, cancelled} {
		if got := s.FixedList(ctx, "", 1); got["error"] != "not_read" {
			t.Fatal(got)
		}
	}
	raw, _ := json.Marshal(first)
	if bytes.Contains(raw, []byte("PRIVATE")) || bytes.Contains(raw, []byte("chunks")) || bytes.Contains(raw, []byte("generation")) {
		t.Fatal("private payload in summary")
	}
	if len(s.entry.works) != 31 || len(s.entry.tombstones) != 5 {
		t.Fatal("read changed ledger")
	}
}

func TestResidentListThroughAuthenticatedGateway(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	defer g.Close(context.Background())
	id := strings.Repeat("1", 32)
	s.entry.works[id] = listWork(id, s.generation)
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
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
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
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tether_fixed_list","arguments":{"cursor":"","limit":32}}}`
	for _, credential := range []string{"", sign("other-subject")} {
		if res := call(credential, sid, body); res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
			t.Fatal("unauthorized list", res.Code, res.Body.String())
		}
	}
	res := call(bearer, sid, body)
	var envelope map[string]any
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &envelope) != nil {
		t.Fatal(res.Code, res.Body.String())
	}
	result := envelope["result"].(map[string]any)
	wantRaw, _ := json.Marshal(s.FixedList(context.Background(), "", 32))
	var want any
	json.Unmarshal(wantRaw, &want)
	if result["isError"] != false || !reflect.DeepEqual(result["structuredContent"], want) {
		t.Fatal(result)
	}
	var mirror any
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if json.Unmarshal([]byte(text), &mirror) != nil || !reflect.DeepEqual(mirror, want) {
		t.Fatal("text mirror changed")
	}
	if s.entry.current != nil || s.principal.route != nil {
		t.Fatal("list acquired work")
	}
}

func releaseListWork(t *testing.T, s *residentSession, id string, terminal bool) {
	t.Helper()
	w := s.entry.works[id]
	w.phase = "finished"
	w.done = true
	w.ownerCleanup = "confirmed"
	w.gatewayCleanup = "confirmed"
	w.status.Terminal = terminal
	if terminal {
		w.status.Cleanup = agentexec.CleanupConfirmed
	}
	w.completed = make(chan struct{})
	close(w.completed)
	if v := s.FixedRelease(context.Background(), id); v["result_released"] != true {
		t.Fatal(v)
	}
}
func TestResidentListMatchesSharedSchemaGolden(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	pending := strings.Repeat("1", 32)
	released := strings.Repeat("2", 32)
	s.entry.works[pending] = listWork(pending, s.generation)
	s.entry.works[pending].phase = "unknown"
	s.entry.works[pending].ownerCleanup = "unknown"
	s.entry.works[pending].gatewayCleanup = "unknown"
	s.entry.works[pending].failure = "start_uncertain"
	s.entry.works[released] = listWork(released, s.generation)
	releaseListWork(t, s, released, false)
	raw, err := os.ReadFile("../mcpadapter/testdata/resident-list-output.json")
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if json.Unmarshal(raw, &want) != nil {
		t.Fatal("bad golden")
	}
	actual, _ := json.Marshal(s.FixedList(context.Background(), "", 32))
	var got any
	json.Unmarshal(actual, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gateway list changed: %s", actual)
	}
}
