package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// #408: the gateway refuses to start, before any key is read, when a role
// rule fails, and says which entry and why by position only.

func roles(doc map[string]any, i int, r ...string) {
	item(doc["gateway"].(map[string]any), "principals", i)["roles"] = r
}

func pairedDoc(t *testing.T) map[string]any {
	d := residentDoc(t)
	d["pairing"] = map[string]any{"registry_path": "/tmp/pair.json", "audience": "pair-api", "client_id": "pair-client", "token_types": []any{"at+jwt"}, "consent": pairingConsentDoc()}
	d["hosts"] = []any{
		map[string]any{"label": "mac-1", "paired": true, "principals": []any{"member-1"}},
		map[string]any{"label": "mac-2", "public_key": authorized(t, 0x22), "principals": []any{"member-2"}},
	}
	return d
}

func TestRoleSplitConfigStarts(t *testing.T) {
	// The migrated shape: the resident host serves only its owner, the
	// rescue host only rescue operators.
	for name, doc := range map[string]map[string]any{"static resident": residentDoc(t), "paired resident": pairedDoc(t)} {
		roles(doc, 0, "resident_owner")
		roles(doc, 1, "rescue_operator")
		if _, err := parseLocalConfig(encode(t, doc), configPath); err != nil {
			t.Fatalf("%s: split roles refused: %v", name, err)
		}
	}
}

func TestRoleRulesRefuseStartWithPosition(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  func() map[string]any
		want string
	}{
		{"principal without roles", func() map[string]any {
			d := localDoc(t)
			delete(item(d["gateway"].(map[string]any), "principals", 1), "roles")
			return d
		}, "principals の 2 番目"},
		{"unknown role", func() map[string]any { d := localDoc(t); roles(d, 1, "operator"); return d }, "principals の 2 番目"},
		{"multi-factor before #398", func() map[string]any {
			d := localDoc(t)
			d["gateway"].(map[string]any)["role_assurance"] = map[string]any{"rescue_operator": "multi_factor"}
			return d
		}, "multi_factor"},
		// The old shape: the resident host also served another principal's
		// rescue connection. The gateway now refuses it instead of serving it.
		{"resident host shared with a rescue operator", func() map[string]any {
			d := residentDoc(t)
			item(d, "hosts", 0)["principals"] = []any{"member-1", "member-2"}
			return d
		}, "hosts の 1 番目"},
		{"rescue host lists a resident owner only", func() map[string]any { d := localDoc(t); roles(d, 1, "resident_owner"); return d }, "hosts の 2 番目"},
		{"resident enrollment of a rescue operator", func() map[string]any { d := residentDoc(t); roles(d, 0, "rescue_operator"); return d }, "resident の 1 番目"},
		{"pair slot of a rescue operator", func() map[string]any { d := pairedDoc(t); roles(d, 0, "rescue_operator"); return d }, "resident の 1 番目"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRecorder(t, encode(t, tc.doc()))
			var diagnostic bytes.Buffer
			if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 2 || r.trace() != "read" {
				t.Fatalf("exit=%d trace=%q", code, r.trace())
			}
			if !strings.Contains(diagnostic.String(), tc.want) {
				t.Fatalf("diagnostic %q does not name %q", diagnostic.String(), tc.want)
			}
			checkDiagnostic(t, diagnostic.String())
		})
	}
}
