package maclaunch

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"golang.org/x/crypto/ssh"
)

func fixedPolicyBytes(t *testing.T, policyPath, keyPath string) []byte {
	t.Helper()
	principal, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	principalKey, err := ssh.NewPublicKey(principal)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(host)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"protocol": "rescue-mac-resident-policy/1", "policy_digest": agentexec.MacCurrentUserPolicyDigest(), "registration_endpoint": "wss://gateway.test/register", "host_label": "host", "principal_label": "principal", "principal_public_key": base64.StdEncoding.EncodeToString(principalKey.Marshal()), "host_key_path": keyPath, "host_key_fingerprint": ssh.FingerprintSHA256(hostKey), "work_dir": "/Users/test", "home": "/Users/test", "cleanup_seconds": 10, "operations": []any{map[string]any{"id": ResidentFixedRequestID, "command": ResidentFixedCommand, "timeout_ms": 15000}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = policyPath
	return raw
}
func TestValidateFixedResidentPolicyAcceptsExactAndRejectsExpandedScope(t *testing.T) {
	path := "/Users/test/Library/Application Support/resident-policy.json"
	key := "/Users/test/Library/Application Support/host-key-abc"
	valid := fixedPolicyBytes(t, path, key)
	if err := ValidateFixedResidentPolicy(valid, path); err != nil {
		t.Fatalf("fixed policy rejected: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(valid, &doc); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(map[string]any){
		"operation": func(d map[string]any) {
			d["operations"] = []any{map[string]any{"id": "custom", "command": "echo unsafe", "timeout_ms": 15000}}
		},
		"same-path-key":  func(d map[string]any) { d["host_key_path"] = path },
		"missing-member": func(d map[string]any) { delete(d, "principal_label") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range doc {
				copy[k] = v
			}
			mutate(copy)
			raw, _ := json.Marshal(copy)
			if err := ValidateFixedResidentPolicy(raw, path); err == nil {
				t.Fatal("invalid fixed enrollment accepted")
			}
		})
	}
}
