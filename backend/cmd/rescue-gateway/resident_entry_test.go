package main

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"golang.org/x/crypto/ssh"
)

func TestEntryResidentOmissionKeepsLegacyProtocolClosed(t *testing.T) {
	l := serveResidentEntry(t, localDoc(t))
	code, _ := roundTrip(t, l, "GET /agent-register HTTP/1.1\r\nHost: gateway.example.invalid\r\nConnection: close\r\nSec-WebSocket-Protocol: "+agenttransport.ResidentSubprotocol+"\r\n\r\n")
	if code != http.StatusBadRequest {
		t.Fatal("legacy enrollment enabled resident transport", code)
	}
}

func TestEntryComposesAuthenticatedResidentBeforeFirstSession(t *testing.T) {
	for _, access := range []bool{false, true} {
		t.Run(strconv.FormatBool(access), func(t *testing.T) {
			doc := residentDoc(t)
			if access {
				doc["gateway"] = accessGatewayDoc(t)
			}
			l := serveResidentEntry(t, doc)
			auth := residentAuth(t, access, "synthetic-subject-1")
			other := residentAuth(t, access, "synthetic-subject-2")
			oldSID := residentInitialize(t, l, auth)
			ws, transcript := residentPeer(t, l)
			sid := residentAwaitSession(t, l, auth)
			args := map[string]any{"request_id": strings.Repeat("1", 32)}
			if code, _ := residentTool(t, l, auth, oldSID, "rescue_start", map[string]any{"request_id": "legacy", "command": "fixed", "timeout_ms": 1000}); code != 404 {
				t.Fatal("discovery session adopted resident authority", code)
			}
			if code, _ := residentTool(t, l, other, sid, "tether_fixed_status", args); code != 404 {
				t.Fatal("different principal borrowed resident session", code)
			}
			wantUnauth := 401
			if access {
				wantUnauth = 403
			}
			if code, _ := residentTool(t, l, "", sid, "tether_fixed_status", args); code != wantUnauth {
				t.Fatal("resident bypassed per-request authentication", code)
			}
			otherSID := residentInitialize(t, l, other)
			if code, _, body := residentRPC(t, l, other, otherSID, "tools/list", nil); code != 200 || strings.Contains(body, "tether_fixed_start") {
				t.Fatal("unselected principal acquired resident tools", code)
			}
			if code, body := residentTool(t, l, auth, sid, "tether_fixed_status", args); code != 200 || !strings.Contains(body, "not_found") {
				t.Fatal("fixed status not bound", code, body)
			}
			if code, body := residentTool(t, l, auth, sid, "tether_fixed_cancel", args); code != 200 || !strings.Contains(body, "not_found") {
				t.Fatal("unknown cancel fabricated cleanup", code, body)
			}
			if code, body := residentTool(t, l, auth, sid, "tether_fixed_start", map[string]any{"request_id": strings.Repeat("2", 32), "command": "id"}); code != 200 || !strings.Contains(body, `"error"`) {
				t.Fatal("command override admitted", code, body)
			}
			if code, body := residentTool(t, l, auth, sid, "tether_fixed_start", args); code != 200 || !strings.Contains(body, `"phase":"pending"`) {
				t.Fatal("fixed request not dispatched", code, body)
			}
			intent := residentReadMessage(t, ws)
			if intent["type"] != "resident_fixed" || intent["operation"] != agentgateway.ResidentFixedID || intent["timeout_ms"] != "15000" || intent["request_id"] != args["request_id"] || intent["sequence"] != "1" {
				t.Fatal("wrong fixed request binding")
			}
			values := []string{intent["generation"], intent["sequence"], intent["request_id"], intent["operation"], intent["deadline_unix_ms"], intent["timeout_ms"]}
			residentVerifyMessage(t, intent, transcript("gateway-fixed", values...))
			ms, err := strconv.ParseInt(intent["deadline_unix_ms"], 10, 64)
			if err != nil || time.Until(time.UnixMilli(ms)) <= 0 || time.Until(time.UnixMilli(ms)) > 15*time.Second {
				t.Fatal("unbounded fixed work")
			}
			// The synthetic host admits no native work. Only its signed refusal
			// permits the existing no-acquisition cleanup observation.
			refusal := []string{intent["generation"], intent["sequence"], intent["request_id"], "work_unconfigured"}
			sig, err := signerOf(t, 0x21).Sign(nil, transcript("host-refused", refusal...))
			if err != nil {
				t.Fatal(err)
			}
			residentWriteMessage(t, ws, map[string]string{"type": "resident_refused", "generation": refusal[0], "sequence": refusal[1], "request_id": refusal[2], "code": refusal[3], "signature": base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
			deadline := time.Now().Add(5 * time.Second)
			for {
				code, body := residentTool(t, l, auth, sid, "tether_fixed_status", args)
				if code == 200 && strings.Contains(body, `"phase":"finished"`) {
					if !strings.Contains(body, `"error":"admission_refused_or_uncertain"`) || !strings.Contains(body, `"owner_cleanup":"confirmed"`) || !strings.Contains(body, `"gateway_cleanup":"confirmed"`) {
						t.Fatal("refused work misreported", body)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("signed refusal never reached MCP status", code, body)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestResidentOnlyEntryNeverFallsBackToGenericSession(t *testing.T) {
	for _, access := range []bool{false, true} {
		t.Run(strconv.FormatBool(access), func(t *testing.T) {
			doc := residentDoc(t)
			if access {
				doc["gateway"] = accessGatewayDoc(t)
			}
			doc["resident_only"] = true
			l := serveResidentEntry(t, doc)
			auth := residentAuth(t, access, "synthetic-subject-1")
			params := map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}}
			code, headers, body := residentRPC(t, l, auth, "", "initialize", params)
			offlineSID := headers.Get(agentgateway.HeaderSession)
			if code != 200 || offlineSID == "" || !strings.Contains(body, "Resident catalog only") {
				t.Fatal("offline catalog unavailable", code, body)
			}
			if code, _, _ := residentRPC(t, l, auth, offlineSID, "notifications/initialized", nil); code != 202 {
				t.Fatal("offline initialized", code)
			}
			assertOffline := func() {
				t.Helper()
				if code, _, body := residentRPC(t, l, auth, offlineSID, "tools/list", nil); code != 200 || !strings.Contains(body, "tether_fixed_get_execution") || strings.Contains(body, "rescue_start") {
					t.Fatal("offline catalog", code, body)
				}
				for _, tool := range []string{"tether_fixed_start", "tether_fixed_status", "tether_fixed_cancel", "tether_fixed_release", "tether_fixed_list", "tether_fixed_read_output", "tether_fixed_get_execution", "rescue_start"} {
					if code, body := residentTool(t, l, auth, offlineSID, tool, map[string]any{"request_id": strings.Repeat("a", 32)}); code != 200 || !strings.Contains(body, "Resident catalog only") || !strings.Contains(body, `"error"`) {
						t.Fatal("offline call not refused", tool, code, body)
					}
				}
			}
			assertOffline()
			wantUnauth := 401
			if access {
				wantUnauth = 403
			}
			if code, _, _ := residentRPC(t, l, "", offlineSID, "tools/list", nil); code != wantUnauth {
				t.Fatal("offline auth bypass", code)
			}
			if code, _, _ := residentRPC(t, l, residentAuth(t, access, "synthetic-subject-2"), offlineSID, "tools/list", nil); code != 404 {
				t.Fatal("offline session borrowed", code)
			}
			ws, _ := residentPeer(t, l)
			defer ws.CloseNow()
			var sid string
			deadline := time.Now().Add(5 * time.Second)
			for sid == "" {
				code, h, body := residentRPC(t, l, auth, "", "initialize", params)
				sid = h.Get(agentgateway.HeaderSession)
				if code == 200 && sid != "" && !strings.Contains(body, "Resident catalog only") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("resident control did not become available", code)
				}
				if code == 200 && sid != "" && strings.Contains(body, "Resident catalog only") {
					residentDelete(t, l, auth, sid)
					sid = ""
				} else if code != 503 || sid != "" {
					t.Fatal("unexpected initialization response", code, body)
				}
				time.Sleep(time.Millisecond)
			}
			assertOffline() // A later live control must not promote the old catalog session.
			if code, _, _ := residentRPC(t, l, auth, sid, "notifications/initialized", nil); code != 202 {
				t.Fatal("initialized failed", code)
			}
			if code, _, body := residentRPC(t, l, auth, sid, "tools/list", nil); code != 200 || !strings.Contains(body, "tether_fixed_start") || strings.Contains(body, "rescue_start") {
				t.Fatal("resident-only catalog is not fixed", code, body)
			}
			if code, body := residentTool(t, l, auth, sid, "rescue_start", map[string]any{"request_id": "legacy", "command": "id", "timeout_ms": 1000}); code == 200 && !strings.Contains(body, `"error"`) {
				t.Fatal("generic command admitted", code, body)
			}
			other := residentAuth(t, access, "synthetic-subject-2")
			if code, _, _ := residentRPC(t, l, other, "", "initialize", params); code != 503 {
				t.Fatal("unenrolled principal created fallback session", code)
			}
			residentDelete(t, l, auth, offlineSID)
			if code, _, _ := residentRPC(t, l, auth, offlineSID, "tools/list", nil); code != 404 {
				t.Fatal("deleted offline session survived", code)
			}
		})
	}
}
