package hostkeychain

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/ssh"
)

var (
	ErrNotFound    = errors.New("hostkeychain: item not found")
	ErrUnavailable = errors.New("hostkeychain: keychain unavailable")
	ErrFingerprint = errors.New("hostkeychain: account fingerprint mismatch")
	ErrInvalid     = errors.New("hostkeychain: invalid key or fingerprint")
	ErrDuplicate   = errors.New("hostkeychain: item already exists")
	ErrUnsupported = errors.New("hostkeychain: unsupported on this platform")
)

// UseDataProtectionKeychain is true in product builds. Unsigned tests set it
// to false because Security rejects data-protection-keychain access without
// the signed entitlement.
var UseDataProtectionKeychain = true

// Store persists an Ed25519 seed or private key under its fingerprint account.
// Load verifies the account against the key before returning a signer.
func Store(key []byte, fingerprint string) error {
	if !validFingerprint(fingerprint) {
		return ErrInvalid
	}
	seed, err := seedBytes(key)
	if err != nil {
		return err
	}
	defer clear(seed)
	private := ed25519.NewKeyFromSeed(seed)
	defer clear(private)
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil || ssh.FingerprintSHA256(signer.PublicKey()) != fingerprint {
		return ErrFingerprint
	}
	return storeSeed(fingerprint, seed)
}

// Load returns the signer only when the stored Ed25519 key matches its account.
func Load(fingerprint string) (ssh.Signer, error) {
	if !validFingerprint(fingerprint) {
		return nil, ErrInvalid
	}
	seed, err := loadSeed(fingerprint)
	if err != nil {
		return nil, err
	}
	defer clear(seed)
	if len(seed) != ed25519.SeedSize {
		return nil, ErrInvalid
	}
	private := ed25519.NewKeyFromSeed(seed)
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		clear(private)
		return nil, ErrInvalid
	}
	if ssh.FingerprintSHA256(signer.PublicKey()) != fingerprint {
		clear(private)
		return nil, ErrFingerprint
	}
	return signer, nil
}

// Delete removes an item. Deleting a missing item is idempotent for cleanup.
func Delete(fingerprint string) error {
	if !validFingerprint(fingerprint) {
		return ErrInvalid
	}
	return deleteSeed(fingerprint)
}

func seedBytes(key []byte) ([]byte, error) {
	switch len(key) {
	case ed25519.SeedSize:
		return append([]byte(nil), key...), nil
	case ed25519.PrivateKeySize:
		private := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
		valid := bytes.Equal(private, key)
		clear(private)
		if !valid {
			return nil, ErrInvalid
		}
		return append([]byte(nil), key[:ed25519.SeedSize]...), nil
	default:
		return nil, ErrInvalid
	}
}

func validFingerprint(value string) bool {
	const prefix = "SHA256:"
	if len(value) != len(prefix)+43 || value[:len(prefix)] != prefix {
		return false
	}
	encoded := value[len(prefix):]
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == ed25519.PublicKeySize && base64.RawStdEncoding.EncodeToString(decoded) == encoded
}
