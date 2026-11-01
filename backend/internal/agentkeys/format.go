package agentkeys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/pem"

	"golang.org/x/crypto/ssh"
)

const (
	openSSHMagic = "openssh-key-v1\x00"
	pemBegin     = "-----BEGIN OPENSSH PRIVATE KEY-----\n"
	pemEnd       = "-----END OPENSSH PRIVATE KEY-----\n"
)

// parseSigner accepts exactly one unencrypted OpenSSH Ed25519 private key in
// one LF-terminated PEM block with nothing before or after it. It verifies the
// outer public key, inner public key, the seed-derived public half, the
// configured fingerprint and a signature round trip. live is checked last so
// that an expired caller never receives the signer. On any failure the parsed
// private key is cleared (best effort).
func parseSigner(data []byte, fingerprint string, live func() bool) (ssh.Signer, error) {
	if len(data) == 0 || len(data) > maxKeyBytes || !bytes.HasPrefix(data, []byte(pemBegin)) || !bytes.HasSuffix(data, []byte(pemEnd)) {
		return nil, ErrDenied
	}
	// pem.Decode skips malformed leading blocks and trims CR, so the accepted
	// input is pinned to one BEGIN, one END and LF-only lines.
	if bytes.Count(data, []byte("-----BEGIN ")) != 1 || bytes.Count(data, []byte("-----END ")) != 1 || bytes.IndexByte(data, '\r') >= 0 {
		return nil, ErrDenied
	}
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, ErrDenied
	}
	defer wipe(block.Bytes)
	if block.Type != "OPENSSH PRIVATE KEY" || len(block.Headers) != 0 || len(rest) != 0 {
		return nil, ErrDenied
	}
	public, ok := outerPublic(block.Bytes)
	if !ok {
		return nil, ErrDenied
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, ErrDenied
	}
	private, ok := raw.(*ed25519.PrivateKey)
	if !ok || private == nil || len(*private) != ed25519.PrivateKeySize {
		return nil, ErrDenied
	}
	accepted := false
	defer func() {
		if !accepted {
			clear(*private)
		}
	}()
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, ErrDenied
	}
	if ssh.FingerprintSHA256(signer.PublicKey()) != fingerprint || !bytes.Equal(public, signer.PublicKey().Marshal()) {
		return nil, ErrDenied
	}
	message := []byte("rescue-host agent key consistency v1")
	signature, err := signer.Sign(nil, message) // Ed25519 signing is deterministic.
	if err != nil || signature == nil || signature.Format != ssh.KeyAlgoED25519 || signer.PublicKey().Verify(message, signature) != nil {
		return nil, ErrDenied
	}
	if live == nil || !live() {
		return nil, ErrDenied
	}
	accepted = true
	return signer, nil
}

// outerPublic checks the envelope fields that ssh.ParseRawPrivateKey does not
// bind: no cipher/KDF, exactly one key, matching outer and inner Ed25519
// public keys, the seed's public half, and minimal canonical padding.
func outerPublic(data []byte) ([]byte, bool) {
	if !bytes.HasPrefix(data, []byte(openSSHMagic)) {
		return nil, false
	}
	var outer struct {
		Cipher, KDF, Options string
		Count                uint32
		Public, Private      []byte
	}
	if err := ssh.Unmarshal(data[len(openSSHMagic):], &outer); err != nil || outer.Cipher != "none" || outer.KDF != "none" || outer.Options != "" || outer.Count != 1 {
		return nil, false
	}
	public, err := ssh.ParsePublicKey(outer.Public)
	if err != nil || public.Type() != ssh.KeyAlgoED25519 {
		return nil, false
	}
	var inner struct {
		Check1, Check2  uint32
		Algorithm       string
		Public, Private []byte
		Comment         string
		Padding         []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(outer.Private, &inner); err != nil {
		return nil, false
	}
	defer clear(inner.Private)
	if inner.Check1 != inner.Check2 || inner.Algorithm != ssh.KeyAlgoED25519 || len(inner.Public) != ed25519.PublicKeySize || len(inner.Private) != ed25519.PrivateKeySize {
		return nil, false
	}
	if len(outer.Private)%8 != 0 || len(inner.Padding) >= 8 {
		return nil, false
	}
	for i, value := range inner.Padding {
		if value != byte(i+1) {
			return nil, false
		}
	}
	if !bytes.Equal(inner.Public, inner.Private[ed25519.SeedSize:]) {
		return nil, false
	}
	// The stored public half must also be the one the seed derives.
	derived := ed25519.NewKeyFromSeed(inner.Private[:ed25519.SeedSize])
	defer clear(derived)
	if !bytes.Equal(derived[ed25519.SeedSize:], inner.Public) {
		return nil, false
	}
	innerPublic, err := ssh.NewPublicKey(ed25519.PublicKey(inner.Public))
	if err != nil || !bytes.Equal(outer.Public, innerPublic.Marshal()) {
		return nil, false
	}
	return outer.Public, true
}
