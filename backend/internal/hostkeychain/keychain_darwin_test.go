//go:build darwin && cgo

package hostkeychain

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) ([]byte, ssh.PublicKey, string) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	private := ed25519.NewKeyFromSeed(seed)
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(seed) })
	return seed, public, ssh.FingerprintSHA256(public)
}

func prepareTestKeychain(t *testing.T) {
	t.Helper()
	old := UseDataProtectionKeychain
	UseDataProtectionKeychain = false
	t.Cleanup(func() { UseDataProtectionKeychain = old })
}

func TestStoreLoadDeleteRoundTrip(t *testing.T) {
	// A resident host must recover the same identity from either accepted key form and delete it cleanly.
	for _, form := range []string{"seed", "private key"} {
		t.Run(form, func(t *testing.T) {
			seed, public, fingerprint := testKey(t)
			prepareTestKeychain(t)
			key := seed
			if form == "private key" {
				key = ed25519.NewKeyFromSeed(seed)
				defer clear(key)
			}
			if err := Store(key, fingerprint); err != nil {
				t.Fatalf("Store: %v", err)
			}
			t.Cleanup(func() {
				if err := Delete(fingerprint); err != nil {
					t.Errorf("remove test account: %v", err)
				}
			})
			signer, err := Load(fingerprint)
			if err != nil || signer == nil || string(signer.PublicKey().Marshal()) != string(public.Marshal()) {
				t.Fatalf("Load signer = %v, %v", signer, err)
			}
			if err := Delete(fingerprint); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if signer, err := Load(fingerprint); signer != nil || !errors.Is(err, ErrNotFound) {
				t.Fatalf("Load after Delete = %v, %v", signer, err)
			}
		})
	}
}

func TestLoadRejectsAccountFingerprintMismatch(t *testing.T) {
	_, expectedPublic, account := testKey(t)
	wrongSeed, _, _ := testKey(t)
	prepareTestKeychain(t)
	// A stale or misbound item must never authenticate as the fingerprint named by its account.
	if err := storeSeed(account, wrongSeed); err != nil {
		t.Fatalf("store mismatched fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := Delete(account); err != nil {
			t.Errorf("remove test account: %v", err)
		}
	})
	signer, err := Load(account)
	if signer != nil || !errors.Is(err, ErrFingerprint) {
		t.Fatalf("Load mismatched item = %v, %v (expected public %s)", signer, err, ssh.FingerprintSHA256(expectedPublic))
	}
}
