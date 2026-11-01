package agentkeys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/pem"
	"testing"

	"golang.org/x/crypto/ssh"
)

// RFC 8032 section 7.1 TEST 1 is a published test vector. It is used only in
// memory and is not anyone's key. The fingerprint is the value fixed by the
// existing protectedkey tests for the same vector.
const (
	testSeedHex     = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	testFingerprint = "SHA256:bbXpuKG6zhzdmnxq256TlqzFBzRl2f6OOg722cYNbU8"
)

type envelope struct {
	Cipher, KDF, Options string
	Count                uint32
	Public, Private      []byte
}

type innerKey struct {
	Check1, Check2  uint32
	Algorithm       string
	Public, Private []byte
	Comment         string
	Padding         []byte `ssh:"rest"`
}

// syntheticKey encodes the test vector as an unencrypted OpenSSH key. edit may
// change the envelope or the private section before encoding; when edit
// leaves Padding nil, minimal canonical padding is added, and when it sets
// env.Private, that block is used verbatim.
func syntheticKey(t *testing.T, edit func(*envelope, *innerKey)) []byte {
	t.Helper()
	seed, err := hex.DecodeString(testSeedHex)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	in := innerKey{Check1: 0x13572468, Check2: 0x13572468, Algorithm: ssh.KeyAlgoED25519, Public: append([]byte(nil), key[32:]...), Private: append([]byte(nil), key...), Comment: "public test vector"}
	env := envelope{Cipher: "none", KDF: "none", Count: 1, Public: pub.Marshal()}
	if edit != nil {
		edit(&env, &in)
	}
	if env.Private == nil {
		padded := in.Padding == nil
		env.Private = ssh.Marshal(in)
		for i := byte(1); padded && len(env.Private)%8 != 0; i++ {
			env.Private = append(env.Private, i)
		}
	}
	return pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: append([]byte(openSSHMagic), ssh.Marshal(env)...)})
}

func alive() bool { return true }

func TestParsesSingleCanonicalEd25519Key(t *testing.T) {
	signer, err := parseSigner(syntheticKey(t, nil), testFingerprint, alive)
	if err != nil || signer == nil {
		t.Fatalf("parseSigner = %v, %v", signer, err)
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != testFingerprint {
		t.Fatal("unexpected public key")
	}
}

func TestRefusesNoncanonicalEncoding(t *testing.T) {
	valid := syntheticKey(t, nil)
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	cases := map[string][]byte{
		"empty":               nil,
		"leading text":        join([]byte("comment\n"), valid),
		"trailing text":       join(valid, []byte("comment\n")),
		"trailing newline":    join(valid, []byte("\n")),
		"trailing space":      join(valid, []byte(" ")),
		"missing final LF":    valid[:len(valid)-1],
		"duplicate block":     join(valid, valid),
		"malformed lead":      join([]byte(pemBegin+"!\n"+pemEnd), valid),
		"CRLF lines":          bytes.ReplaceAll(valid, []byte("\n"), []byte("\r\n")),
		"PEM header":          bytes.Replace(valid, []byte(pemBegin), []byte(pemBegin+"Proc-Type: 4,ENCRYPTED\n\n"), 1),
		"other PEM type":      bytes.ReplaceAll(valid, []byte("OPENSSH PRIVATE KEY"), []byte("PRIVATE KEY")),
		"oversize":            join(valid[:len(valid)-len(pemEnd)], bytes.Repeat([]byte("A"), maxKeyBytes), []byte(pemEnd)),
		"base64 damaged":      bytes.Replace(valid, []byte("b3BlbnNzaC1rZXktdjEA"), []byte("b3BlbnNzaC1rZXktdjEB"), 1),
		"embedded END marker": bytes.Replace(valid, []byte(pemBegin), []byte(pemBegin+"-----END X-----\n"), 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if signer, err := parseSigner(data, testFingerprint, alive); signer != nil || err != ErrDenied {
				t.Fatalf("parseSigner = %v, %v", signer, err)
			}
		})
	}
}

func TestRefusesInconsistentOrUnsupportedKey(t *testing.T) {
	cases := map[string]func(*envelope, *innerKey){
		"encrypted":            func(e *envelope, _ *innerKey) { e.Cipher, e.KDF = "aes256-ctr", "bcrypt" },
		"kdf without cipher":   func(e *envelope, _ *innerKey) { e.KDF = "bcrypt" },
		"kdf options":          func(e *envelope, _ *innerKey) { e.Options = "x" },
		"two keys":             func(e *envelope, _ *innerKey) { e.Count = 2 },
		"outer public changed": func(e *envelope, _ *innerKey) { e.Public[len(e.Public)-1] ^= 1 },
		"outer public RSA name": func(e *envelope, _ *innerKey) {
			e.Public = ssh.Marshal(struct{ Name, E, N string }{"ssh-rsa", "\x01\x00\x01", "\x00\xc1"})
		},
		"inner public changed": func(_ *envelope, k *innerKey) { k.Public[0] ^= 1 },
		"seed changed":         func(_ *envelope, k *innerKey) { k.Private[0] ^= 1 },
		"embedded public half": func(_ *envelope, k *innerKey) { k.Private[40] ^= 1 },
		"check words differ":   func(_ *envelope, k *innerKey) { k.Check2++ },
		"non-Ed25519 algorithm": func(_ *envelope, k *innerKey) {
			k.Algorithm = ssh.KeyAlgoRSA
		},
		"short private": func(_ *envelope, k *innerKey) { k.Private = k.Private[:63] },
		"padding not sequential": func(_ *envelope, k *innerKey) {
			k.Comment += "x"
			body := ssh.Marshal(*k)
			for len(body)%8 != 0 {
				body = append(body, 9)
			}
			k.Padding = body[len(ssh.Marshal(*k)):]
		},
		"padding a full extra block": func(_ *envelope, k *innerKey) {
			k.Comment += "x"
			body := ssh.Marshal(*k)
			for i := byte(1); len(body)%8 != 0 || len(body) < len(ssh.Marshal(*k))+8; i++ {
				body = append(body, i)
			}
			k.Padding = body[len(ssh.Marshal(*k)):]
		},
		"unaligned block": func(e *envelope, k *innerKey) {
			k.Comment += "x"
			body := ssh.Marshal(*k)
			if len(body)%8 == 0 {
				body = append(body, 1)
			}
			e.Private = body
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			if signer, err := parseSigner(syntheticKey(t, edit), testFingerprint, alive); signer != nil || err != ErrDenied {
				t.Fatalf("parseSigner = %v, %v", signer, err)
			}
		})
	}
}

func TestRefusesOtherFingerprintOrExpiredCaller(t *testing.T) {
	for name, fp := range map[string]string{
		"other key":   "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"empty":       "",
		"lowercased":  "sha256:bbXpuKG6zhzdmnxq256TlqzFBzRl2f6OOg722cYNbU8",
		"with suffix": testFingerprint + " comment",
	} {
		t.Run(name, func(t *testing.T) {
			if signer, err := parseSigner(syntheticKey(t, nil), fp, alive); signer != nil || err != ErrDenied {
				t.Fatalf("parseSigner = %v, %v", signer, err)
			}
		})
	}
	t.Run("expired", func(t *testing.T) {
		if signer, err := parseSigner(syntheticKey(t, nil), testFingerprint, func() bool { return false }); signer != nil || err != ErrDenied {
			t.Fatalf("parseSigner = %v, %v", signer, err)
		}
	})
}
