package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func residentDoc(t *testing.T) map[string]any {
	t.Helper()
	d := localDoc(t)
	// Resident principals must have only one enrolled host, even if a legacy
	// composition allowed more than one. Selection never widens that map.
	item(d, "hosts", 1)["principals"] = []any{"member-2"}
	d["resident"] = []any{map[string]any{"host_label": "mac-1", "principal_label": "member-1", "operation": agentgateway.ResidentFixedID}}
	return d
}

func TestResidentConfigAcceptedBeforeNativeLoads(t *testing.T) {
	doc := residentDoc(t)
	if _, err := parseLocalConfig(encode(t, doc), configPath); err != nil {
		t.Fatal("explicit fixed resident composition refused", err)
	}
	r := newRecorder(t, encode(t, doc))
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 1 ||
		r.trace() != "read load:member-1 load:member-2 listen:127.0.0.1:8787" {
		t.Fatalf("exit=%d trace=%q", code, r.trace())
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestResidentConfigRefusesBeforeAnyKeyOrListener(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"null", func(d map[string]any) { d["resident"] = nil }},
		{"empty", func(d map[string]any) { d["resident"] = []any{} }},
		{"unknown host", func(d map[string]any) { item(d, "resident", 0)["host_label"] = "mac-9" }},
		{"unknown principal", func(d map[string]any) { item(d, "resident", 0)["principal_label"] = "member-9" }},
		{"unmapped pair", func(d map[string]any) { item(d, "resident", 0)["host_label"] = "mac-2" }},
		{"ambiguous host", func(d map[string]any) { item(d, "hosts", 1)["principals"] = []any{"member-1", "member-2"} }},
		{"old operation", func(d map[string]any) { item(d, "resident", 0)["operation"] = "rescue-fixed-command-v1" }},
		{"arbitrary operation", func(d map[string]any) { item(d, "resident", 0)["operation"] = "shell" }},
		{"command override", func(d map[string]any) { item(d, "resident", 0)["command"] = "id" }},
		{"missing operation", func(d map[string]any) { delete(item(d, "resident", 0), "operation") }},
		{"duplicate principal", func(d map[string]any) { d["resident"] = append(d["resident"].([]any), item(d, "resident", 0)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := residentDoc(t)
			tc.change(doc)
			r := newRecorder(t, encode(t, doc))
			var diagnostic bytes.Buffer
			if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 2 || r.trace() != "read" {
				t.Fatalf("exit=%d trace=%q", code, r.trace())
			}
			checkDiagnostic(t, diagnostic.String())
		})
	}
}

func TestResidentConfigPinsOnlyExplicitExistingPair(t *testing.T) {
	c, err := parseLocalConfig(encode(t, residentDoc(t)), configPath)
	if err != nil || len(c.resident) != 1 {
		t.Fatal("resident mapping missing", err)
	}
	e := c.resident[0]
	if e.HostLabel != "mac-1" || e.PrincipalLabel != "member-1" || !bytes.Equal(e.HostKey, publicOf(t, 0x21).Marshal()) {
		t.Fatal("resident mapping widened or substituted")
	}
	legacy, err := parseLocalConfig(encode(t, localDoc(t)), configPath)
	if err != nil || len(legacy.resident) != 0 {
		t.Fatal("legacy configuration silently promoted", err)
	}
}

func TestResidentConfigRejectsDuplicateAndPartialEntries(t *testing.T) {
	doc := residentDoc(t)
	doc["resident"] = append(doc["resident"].([]any), map[string]any{"host_label": "mac-2", "principal_label": "member-2", "operation": "shell"})
	badPartial := encode(t, doc)
	good := string(encode(t, residentDoc(t)))
	duplicate := strings.Replace(good, `"operation":`, `"operation":"tether-fixed-command-v1","operation":`, 1)
	for _, raw := range [][]byte{badPartial, []byte(duplicate)} {
		c, err := parseLocalConfig(raw, configPath)
		if err == nil || len(c.resident) != 0 || len(c.keys) != 0 {
			t.Fatal("invalid input published partial configuration")
		}
	}
}

func TestResidentOnlyConfigRequiresExactExplicitOptIn(t *testing.T) {
	for _, value := range []any{nil, false, "true", 1, []any{}, map[string]any{}} {
		d := residentDoc(t)
		d["resident_only"] = value
		if _, err := parseLocalConfig(encode(t, d), configPath); err == nil {
			t.Fatalf("invalid resident_only accepted: %v", value)
		}
	}
	d := residentDoc(t)
	d["resident_only"] = true
	cfg, err := parseLocalConfig(encode(t, d), configPath)
	if err != nil || !cfg.residentOnly {
		t.Fatal("explicit resident-only scope refused", err)
	}
	delete(d, "resident")
	if _, err := parseLocalConfig(encode(t, d), configPath); err == nil {
		t.Fatal("resident-only accepted without fixed enrollment")
	}
}
