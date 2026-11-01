package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Every value is a synthetic placeholder: fixed-seed in-memory keys and
// example.invalid names. No key or document exists on disk.

const configPath = `C:\Users\synthetic\rescue\gateway.json`

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

func gatewayDoc() map[string]any {
	pub := seedKey(0x51).Public().(ed25519.PublicKey)
	return map[string]any{
		"listen": "127.0.0.1:8787", "auth_mode": "oauth_bearer_jwt", "issuer": "https://issuer.example.invalid/", "resource": "https://gateway.example.invalid/mcp",
		"scope": "rescue.agent", "token_types": []any{"at+jwt"}, "leeway_seconds": 30,
		"jwks": map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "synthetic-ed", "alg": "EdDSA",
			"x": base64.RawURLEncoding.EncodeToString(pub)}}},
		"principals": []any{
			map[string]any{"label": "member-1", "subject": "synthetic-subject-1"},
			map[string]any{"label": "member-2", "subject": "synthetic-subject-2"},
		},
	}
}

func localDoc(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"gateway":               gatewayDoc(),
		"registration_endpoint": "wss://gateway.example.invalid/agent-register",
		"principal_keys": []any{
			map[string]any{"label": "member-1", "path": `C:\Users\synthetic\rescue\keys\member-1`, "fingerprint": fingerprint(t, 0x11)},
			map[string]any{"label": "member-2", "path": `C:\Users\synthetic\rescue\keys\member-2`, "fingerprint": fingerprint(t, 0x12)},
		},
		"hosts": []any{
			map[string]any{"label": "mac-1", "public_key": authorized(t, 0x21), "principals": []any{"member-1"}},
			map[string]any{"label": "mac-2", "public_key": authorized(t, 0x22), "principals": []any{"member-1", "member-2"}},
		},
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

func item(doc map[string]any, member string, i int) map[string]any {
	return doc[member].([]any)[i].(map[string]any)
}

func TestLocalConfigAcceptsExactMapping(t *testing.T) {
	c, err := parseLocalConfig(encode(t, localDoc(t)), configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.keys) != 2 || len(c.gateway.Principals) != 2 || len(c.hosts) != 2 || c.endpoint != "wss://gateway.example.invalid/agent-register" {
		t.Fatalf("config = %+v", c)
	}
	for i, p := range c.gateway.Principals {
		if p.Signer != nil || !strings.HasSuffix(c.keys[i].Path, p.Label) || c.keys[i].Fingerprint != fingerprint(t, byte(0x11+i)) {
			t.Fatalf("principal %d key reference = %+v", i, c.keys[i])
		}
	}
	if c.hosts[1].Label != "mac-2" || !bytes.Equal(c.hosts[1].PublicKey.Marshal(), publicOf(t, 0x22).Marshal()) ||
		strings.Join(c.hosts[1].Principals, ",") != "member-1,member-2" {
		t.Fatalf("host = %+v", c.hosts[1])
	}
}

func TestLocalConfigKeyOrderFollowsPrincipals(t *testing.T) {
	doc := localDoc(t)
	keys := doc["principal_keys"].([]any)
	keys[0], keys[1] = keys[1], keys[0]
	c, err := parseLocalConfig(encode(t, doc), configPath)
	if err != nil || c.keys[0].Fingerprint != fingerprint(t, 0x11) || c.keys[1].Fingerprint != fingerprint(t, 0x12) {
		t.Fatalf("err=%v keys=%+v", err, c.keys)
	}
}

func TestLocalConfigRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, doc map[string]any)
	}{
		{"private key member", func(t *testing.T, d map[string]any) {
			d["private_key"] = "-----BEGIN OPENSSH PRIVATE KEY-----"
		}},
		{"private key in principal entry", func(t *testing.T, d map[string]any) { item(d, "principal_keys", 0)["private_key"] = "x" }},
		{"secret in gateway document", func(t *testing.T, d map[string]any) { d["gateway"].(map[string]any)["client_secret"] = "x" }},
		{"approval-like member", func(t *testing.T, d map[string]any) { d["auto_enroll"] = true }},
		{"unknown host member", func(t *testing.T, d map[string]any) { item(d, "hosts", 0)["address"] = "127.0.0.1:22" }},
		{"missing hosts", func(t *testing.T, d map[string]any) { delete(d, "hosts") }},
		{"missing endpoint", func(t *testing.T, d map[string]any) { delete(d, "registration_endpoint") }},
		{"null hosts", func(t *testing.T, d map[string]any) { d["hosts"] = nil }},
		{"null fingerprint", func(t *testing.T, d map[string]any) { item(d, "principal_keys", 0)["fingerprint"] = nil }},
		{"invalid gateway document", func(t *testing.T, d map[string]any) { d["gateway"].(map[string]any)["listen"] = "0.0.0.0:8787" }},
		{"key path equals config path", func(t *testing.T, d map[string]any) {
			item(d, "principal_keys", 0)["path"] = strings.ToUpper(configPath)
		}},
		{"shared key path", func(t *testing.T, d map[string]any) {
			item(d, "principal_keys", 1)["path"] = strings.ToUpper(item(d, "principal_keys", 0)["path"].(string))
		}},
		{"shared fingerprint", func(t *testing.T, d map[string]any) {
			item(d, "principal_keys", 1)["fingerprint"] = fingerprint(t, 0x11)
		}},
		{"relative key path", func(t *testing.T, d map[string]any) { item(d, "principal_keys", 0)["path"] = `keys\member-1` }},
		{"key path with newline", func(t *testing.T, d map[string]any) {
			item(d, "principal_keys", 0)["path"] = "/Users/synthetic/keys\n/member-1"
		}},
		{"malformed fingerprint", func(t *testing.T, d map[string]any) { item(d, "principal_keys", 0)["fingerprint"] = "SHA256:abc" }},
		{"key for unknown principal", func(t *testing.T, d map[string]any) { item(d, "principal_keys", 1)["label"] = "member-3" }},
		{"key keyed by subject", func(t *testing.T, d map[string]any) {
			item(d, "principal_keys", 0)["label"] = "synthetic-subject-1"
		}},
		{"two keys for one principal", func(t *testing.T, d map[string]any) { item(d, "principal_keys", 1)["label"] = "member-1" }},
		{"missing principal key", func(t *testing.T, d map[string]any) {
			d["principal_keys"] = d["principal_keys"].([]any)[:1]
		}},
		{"extra principal key", func(t *testing.T, d map[string]any) {
			d["principal_keys"] = append(d["principal_keys"].([]any), map[string]any{
				"label": "member-3", "path": `C:\Users\synthetic\rescue\keys\member-3`, "fingerprint": fingerprint(t, 0x13)})
		}},
		{"host label equals principal label", func(t *testing.T, d map[string]any) { item(d, "hosts", 0)["label"] = "member-1" }},
		{"host serves unknown principal", func(t *testing.T, d map[string]any) { item(d, "hosts", 0)["principals"] = []any{"member-9"} }},
		{"host maps subject", func(t *testing.T, d map[string]any) {
			item(d, "hosts", 0)["principals"] = []any{"synthetic-subject-1"}
		}},
		{"host maps principal twice", func(t *testing.T, d map[string]any) {
			item(d, "hosts", 0)["principals"] = []any{"member-1", "member-1"}
		}},
		{"host without principal", func(t *testing.T, d map[string]any) { item(d, "hosts", 0)["principals"] = []any{} }},
		{"duplicate host label", func(t *testing.T, d map[string]any) { item(d, "hosts", 1)["label"] = "mac-1" }},
		{"duplicate host key", func(t *testing.T, d map[string]any) { item(d, "hosts", 1)["public_key"] = authorized(t, 0x21) }},
		{"host key is a principal key", func(t *testing.T, d map[string]any) { item(d, "hosts", 0)["public_key"] = authorized(t, 0x12) }},
		{"host key with comment", func(t *testing.T, d map[string]any) {
			item(d, "hosts", 0)["public_key"] = authorized(t, 0x21) + " mac-1@example.invalid"
		}},
		{"host key non-canonical base64", func(t *testing.T, d map[string]any) {
			key := authorized(t, 0x21)
			item(d, "hosts", 0)["public_key"] = key[:20] + "\n" + key[20:] // the decoder skips newlines
		}},
		{"host key with options", func(t *testing.T, d map[string]any) {
			item(d, "hosts", 0)["public_key"] = "restrict " + authorized(t, 0x21)
		}},
		{"host key of other type", func(t *testing.T, d map[string]any) {
			item(d, "hosts", 0)["public_key"] = "ssh-rsa " + strings.TrimPrefix(authorized(t, 0x21), "ssh-ed25519 ")
		}},
		{"no hosts", func(t *testing.T, d map[string]any) { d["hosts"] = []any{} }},
		{"five hosts", func(t *testing.T, d map[string]any) {
			var hosts []any
			for i := byte(0); i < 5; i++ {
				hosts = append(hosts, map[string]any{"label": "mac-" + string('a'+rune(i)), "public_key": authorized(t, 0x30+i), "principals": []any{"member-1"}})
			}
			d["hosts"] = hosts
		}},
		{"endpoint ws", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "ws://gateway.example.invalid/agent-register"
		}},
		{"endpoint https", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "https://gateway.example.invalid/agent-register"
		}},
		{"endpoint other host", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://other.example.invalid/agent-register"
		}},
		{"endpoint explicit port", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://gateway.example.invalid:443/agent-register"
		}},
		{"endpoint is the resource", func(t *testing.T, d map[string]any) { d["registration_endpoint"] = "wss://gateway.example.invalid/mcp" }},
		{"endpoint is metadata", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://gateway.example.invalid/.well-known/oauth-protected-resource"
		}},
		{"endpoint is resource metadata", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://gateway.example.invalid/.well-known/oauth-protected-resource/mcp"
		}},
		{"endpoint under well-known", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://gateway.example.invalid/.well-known/agent"
		}},
		{"endpoint root", func(t *testing.T, d map[string]any) { d["registration_endpoint"] = "wss://gateway.example.invalid/" }},
		{"endpoint query", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://gateway.example.invalid/agent-register?host=mac-1"
		}},
		{"endpoint userinfo", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://user:pass@gateway.example.invalid/agent-register"
		}},
		{"endpoint upper-case host", func(t *testing.T, d map[string]any) {
			d["registration_endpoint"] = "wss://Gateway.example.invalid/agent-register"
		}},
		{"endpoint not a string", func(t *testing.T, d map[string]any) { d["registration_endpoint"] = 443 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := localDoc(t)
			tc.mutate(t, doc)
			if _, err := parseLocalConfig(encode(t, doc), configPath); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestLocalConfigRefusesRawShapes(t *testing.T) {
	valid := encode(t, localDoc(t))
	duplicate := append(append([]byte(nil), valid[:len(valid)-1]...), `,"registration_endpoint":"wss://gateway.example.invalid/agent-register"}`...)
	oversize := append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), 16<<10)...)
	for name, raw := range map[string][]byte{
		"empty": nil, "array": []byte(`[]`), "duplicate member": duplicate, "oversize": oversize,
		"trailing value": append(append([]byte(nil), valid...), `{}`...), "invalid utf-8": append(append([]byte(nil), valid[:len(valid)-1]...), 0xff, '}'),
	} {
		if _, err := parseLocalConfig(raw, configPath); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
