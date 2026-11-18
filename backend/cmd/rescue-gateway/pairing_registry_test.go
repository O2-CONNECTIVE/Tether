package main

import (
	"encoding/json"
	"testing"
)

func TestPairingRegistryRejectsInvalidRecordsA14(t *testing.T) {
	doc := localDoc(t)
	doc["pairing"] = map[string]any{"registry_path": "/tmp/pair.json", "audience": "pair-api", "client_id": "pair-client", "token_types": []any{"at+jwt"}}
	cfg, err := parseLocalConfig(encode(t, doc), configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		records []storedPairRecord
	}{
		{"unknown host label", []storedPairRecord{{HostLabel: "unknown", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"static host label", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"principal key duplicate", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x11), Fingerprint: fingerprint(t, 0x11), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"fingerprint mismatch", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x32), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"duplicate key", []storedPairRecord{
			{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"},
			{HostLabel: "mac-2", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"},
		}},
		{"issuer mismatch", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://other.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"subject mismatch", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-2", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"malformed registration time", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "yesterday"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, marshalErr := json.Marshal(storedPairRegistry{Hosts: tc.records})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err := validatePairingRegistry(raw, cfg); err == nil {
				t.Fatal("A14 expected invalid persisted pairing to be rejected")
			}
		})
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"malformed JSON", []byte(`{"hosts":[`)},
		{"unknown top-level member", []byte(`{"hosts":[],"extra":true}`)},
		{"unknown record member", []byte(`{"hosts":[{"label":"mac-1","public_key":"x","fingerprint":"x","issuer":"x","subject":"x","registered_at":"2026-11-18T00:00:00Z","extra":true}]}`)},
		{"missing required record member", []byte(`{"hosts":[{"label":"mac-1","public_key":"x","fingerprint":"x","issuer":"x","subject":"x"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validatePairingRegistry(tc.raw, cfg); err == nil {
				t.Fatal("A14 expected malformed registry format to be rejected")
			}
		})
	}
}
