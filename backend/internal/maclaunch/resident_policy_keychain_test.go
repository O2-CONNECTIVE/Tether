package maclaunch

import (
	"encoding/json"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

func keychainPolicyBytes(t *testing.T, policyPath, hostPath, fingerprint string) []byte {
	t.Helper()
	raw := fixedPolicyBytes(t, policyPath, hostPath)
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	policy["host_key_store"] = "keychain"
	policy["host_key_fingerprint"] = fingerprint
	if hostPath == "" {
		delete(policy, "host_key_path")
	}
	policy["policy_digest"] = agentexec.MacCurrentUserPolicyDigest()
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestResidentPolicyAcceptsKeychainStoreAndLegacyPEM(t *testing.T) {
	path := "/Users/test/Library/Application Support/resident-policy.json"
	legacy := fixedPolicyBytes(t, path, "/Users/test/keys/host-key")
	if _, err := parseResidentPolicy(legacy); err != nil {
		t.Fatalf("legacy policy rejected: %v", err)
	}
	keychain := keychainPolicyBytes(t, path, "", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if _, err := parseResidentPolicy(keychain); err != nil {
		t.Fatalf("Keychain policy rejected: %v", err)
	}
	if err := ValidateFixedResidentPolicy(keychain, path); err != nil {
		t.Fatalf("fixed Keychain policy rejected: %v", err)
	}
}

func TestValidateFixedResidentPolicyRejectsInvalidHostKeySelection(t *testing.T) {
	policyPath := "/Users/test/Library/Application Support/resident-policy.json"
	hostPath := "/Users/test/keys/host-key"
	for name, edit := range map[string]func(map[string]any){
		"missing-fingerprint-with-empty-store": func(policy map[string]any) {
			policy["host_key_store"] = ""
			delete(policy, "host_key_fingerprint")
		},
		"missing-fingerprint-with-keychain-and-numeric-path": func(policy map[string]any) {
			policy["host_key_store"] = "keychain"
			policy["host_key_path"] = 123
			delete(policy, "host_key_fingerprint")
		},
		"neither-store-nor-path": func(policy map[string]any) {
			delete(policy, "host_key_path")
		},
		"both-store-and-path": func(policy map[string]any) {
			policy["host_key_store"] = "keychain"
		},
		"unknown-store": func(policy map[string]any) {
			delete(policy, "host_key_path")
			policy["host_key_store"] = "cloud"
		},
		"non-string-store": func(policy map[string]any) {
			delete(policy, "host_key_path")
			policy["host_key_store"] = 123
		},
		"non-string-path": func(policy map[string]any) {
			policy["host_key_path"] = 123
		},
		"non-string-fingerprint": func(policy map[string]any) {
			policy["host_key_fingerprint"] = 123
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := fixedPolicyBytes(t, policyPath, hostPath)
			var policy map[string]any
			if err := json.Unmarshal(raw, &policy); err != nil {
				t.Fatal(err)
			}
			edit(policy)
			raw, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateFixedResidentPolicy(raw, policyPath); err == nil {
				t.Fatal("invalid host key selection accepted")
			}
		})
	}
}
