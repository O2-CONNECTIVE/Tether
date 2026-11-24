package main

import "testing"

func pairingDoc(t *testing.T) map[string]any {
	t.Helper()
	doc := residentDoc(t)
	host := item(doc, "hosts", 0)
	delete(host, "public_key")
	host["paired"] = true
	doc["pairing"] = map[string]any{
		"registry_path": "/tmp/pairing-registry.json", "audience": "pair-api",
		"client_id": "pair-client", "token_types": []any{"at+jwt"},
		"consent": pairingConsentDoc(),
	}
	return doc
}

func pairingConsentDoc() map[string]any {
	return map[string]any{
		"client_id": "browser-client", "authorization_endpoint": "https://idp.example.invalid/authorize",
		"token_endpoint": "https://idp.example.invalid/token",
	}
}
