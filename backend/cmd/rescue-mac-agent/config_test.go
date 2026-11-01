package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"golang.org/x/crypto/ssh"
)

// Every value is a synthetic placeholder: fixed-seed in-memory keys and
// example.invalid names. No key or document exists on disk.

const configPath = "/Users/synthetic/rescue/mac-agent.json"

func seedKey(b byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
}

func publicOf(t *testing.T, b byte) ssh.PublicKey {
	t.Helper()
	pub, err := ssh.NewPublicKey(seedKey(b).Public())
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func signerOf(t *testing.T, b byte) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromKey(seedKey(b))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func authorized(t *testing.T, b byte) string {
	t.Helper()
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(publicOf(t, b))), "\n")
}

func fingerprint(t *testing.T, b byte) string {
	t.Helper()
	return ssh.FingerprintSHA256(publicOf(t, b))
}

// macDoc: principal member-1 holds key 0x11 at the gateway; this Mac's host
// key is 0x21.
func macDoc(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"registration_endpoint": "wss://gateway.example.invalid/agent-register",
		"host_label":            "mac-1",
		"principal_label":       "member-1",
		"principal_public_key":  authorized(t, 0x11),
		"host_key_path":         "/Users/synthetic/rescue/keys/mac-1",
		"host_key_fingerprint":  fingerprint(t, 0x21),
		"policy_digest":         agentexec.MacCurrentUserPolicyDigest(),
		"work_dir":              "/Users/synthetic/rescue-work",
		"home":                  "/Users/synthetic",
		"session_seconds":       3600,
		"cleanup_seconds":       10,
	}
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLocalConfigAccepts(t *testing.T) {
	c, err := parseLocalConfig(encode(t, macDoc(t)), configPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.endpoint != "wss://gateway.example.invalid/agent-register" || c.host != "mac-1" || c.principal != "member-1" ||
		!bytes.Equal(c.allowed.Marshal(), publicOf(t, 0x11).Marshal()) || c.hostKey.Path != "/Users/synthetic/rescue/keys/mac-1" ||
		c.hostKey.Fingerprint != fingerprint(t, 0x21) || c.workDir != "/Users/synthetic/rescue-work" || c.home != "/Users/synthetic" ||
		c.session != time.Hour || c.cleanup != 10*time.Second {
		t.Fatalf("config = %+v", c)
	}
}

func TestLocalConfigRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, d map[string]any)
	}{
		{"private key member", func(t *testing.T, d map[string]any) { d["host_private_key"] = "-----BEGIN OPENSSH PRIVATE KEY-----" }},
		{"approval member", func(t *testing.T, d map[string]any) { d["approved"] = true }},
		{"reconnect member", func(t *testing.T, d map[string]any) { d["auto_reconnect"] = true }},
		{"shell member", func(t *testing.T, d map[string]any) { d["shell"] = "/bin/zsh" }},
		{"missing policy", func(t *testing.T, d map[string]any) { delete(d, "policy_digest") }},
		{"missing principal key", func(t *testing.T, d map[string]any) { delete(d, "principal_public_key") }},
		{"null home", func(t *testing.T, d map[string]any) { d["home"] = nil }},
		{"other policy", func(t *testing.T, d map[string]any) { d["policy_digest"] = strings.Repeat("0", 64) }},
		{"policy case variant", func(t *testing.T, d map[string]any) {
			d["policy_digest"] = strings.ToUpper(agentexec.MacCurrentUserPolicyDigest())
		}},
		{"host label equals principal label", func(t *testing.T, d map[string]any) { d["host_label"] = "member-1" }},
		{"upper-case label", func(t *testing.T, d map[string]any) { d["principal_label"] = "Member-1" }},
		{"principal key is the host key", func(t *testing.T, d map[string]any) { d["principal_public_key"] = authorized(t, 0x21) }},
		{"principal key with comment", func(t *testing.T, d map[string]any) {
			d["principal_public_key"] = authorized(t, 0x11) + " member-1@example.invalid"
		}},
		{"principal key of other type", func(t *testing.T, d map[string]any) {
			d["principal_public_key"] = "ssh-rsa " + strings.TrimPrefix(authorized(t, 0x11), "ssh-ed25519 ")
		}},
		{"principal key as fingerprint", func(t *testing.T, d map[string]any) { d["principal_public_key"] = fingerprint(t, 0x11) }},
		{"key path equals config path", func(t *testing.T, d map[string]any) { d["host_key_path"] = strings.ToUpper(configPath) }},
		{"relative key path", func(t *testing.T, d map[string]any) { d["host_key_path"] = "keys/mac-1" }},
		{"unclean key path", func(t *testing.T, d map[string]any) { d["host_key_path"] = "/Users/synthetic/rescue/../keys/mac-1" }},
		{"windows key path", func(t *testing.T, d map[string]any) { d["host_key_path"] = `C:\Users\synthetic\mac-1` }},
		{"malformed fingerprint", func(t *testing.T, d map[string]any) { d["host_key_fingerprint"] = "MD5:00" }},
		{"root work dir", func(t *testing.T, d map[string]any) { d["work_dir"] = "/" }},
		{"relative work dir", func(t *testing.T, d map[string]any) { d["work_dir"] = "rescue-work" }},
		{"unclean home", func(t *testing.T, d map[string]any) { d["home"] = "/Users/synthetic/" }},
		{"short session", func(t *testing.T, d map[string]any) { d["session_seconds"] = 59 }},
		{"session beyond channel lifetime", func(t *testing.T, d map[string]any) {
			d["session_seconds"] = int((maxSession + time.Second) / time.Second)
		}},
		{"fractional session", func(t *testing.T, d map[string]any) { d["session_seconds"] = json.Number("3600.0") }},
		{"exponent session", func(t *testing.T, d map[string]any) { d["session_seconds"] = json.Number("36e2") }},
		{"negative session", func(t *testing.T, d map[string]any) { d["session_seconds"] = -3600 }},
		{"string session", func(t *testing.T, d map[string]any) { d["session_seconds"] = "3600" }},
		{"short cleanup", func(t *testing.T, d map[string]any) { d["cleanup_seconds"] = 6 }},
		{"long cleanup", func(t *testing.T, d map[string]any) { d["cleanup_seconds"] = 61 }},
		{"endpoint ws", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "ws://gateway.example.invalid/agent-register"
		}},
		{"endpoint query", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://gateway.example.invalid/agent-register?token=x"
		}},
		{"endpoint root", func(t *testing.T, d map[string]any) { d["registration_endpoint"] = "wss://gateway.example.invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := macDoc(t)
			tc.mutate(t, doc)
			if _, err := parseLocalConfig(encode(t, doc), configPath); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestLocalConfigRefusesRawShapes(t *testing.T) {
	valid := encode(t, macDoc(t))
	duplicate := append(append([]byte(nil), valid[:len(valid)-1]...), `,"host_label":"mac-2"}`...)
	oversize := append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), 16<<10)...)
	for name, raw := range map[string][]byte{
		"empty": nil, "array": []byte(`[]`), "duplicate member": duplicate, "oversize": oversize,
		"trailing value": append(append([]byte(nil), valid...), `{}`...),
	} {
		if _, err := parseLocalConfig(raw, configPath); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
