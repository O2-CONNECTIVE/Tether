package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func TestNativePairingRegistryRenameAndDirectorySyncA11(t *testing.T) {
	// Atomic replacement must preserve old data on rename failure and report a committed rename despite directory-sync failure.
	newStore := func(t *testing.T, path string, failRename, failSync *bool) registry {
		t.Helper()
		store, err := openRegistryWith(context.Background(), path, registryOps{
			readTrust: func(context.Context, string) ([]byte, error) { return []byte("synthetic protected file"), nil },
			rename: func(old, new string) error {
				if *failRename {
					return errors.New("synthetic rename failure")
				}
				return os.Rename(old, new)
			},
			syncDir: func(string) error {
				if *failSync {
					return errors.New("synthetic directory sync failure")
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("open registry: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	old, next := []byte(`{"hosts":[{"label":"old"}]}`), []byte(`{"hosts":[{"label":"new"}]}`)
	t.Run("rename failure preserves old contents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pairs.json")
		failRename, failSync := false, false
		store := newStore(t, path, &failRename, &failSync)
		if err := store.Save(context.Background(), old); err != nil {
			t.Fatalf("initial save: %v", err)
		}
		failRename = true
		if err := store.Save(context.Background(), next); err == nil {
			t.Fatal("rename failure returned nil")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(old) {
			t.Fatalf("rename failure changed committed file: %q, %v", got, err)
		}
	})
	t.Run("directory sync failure reports committed contents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pairs.json")
		failRename, failSync := false, false
		store := newStore(t, path, &failRename, &failSync)
		if err := store.Save(context.Background(), old); err != nil {
			t.Fatalf("initial save: %v", err)
		}
		failSync = true
		if err := store.Save(context.Background(), next); err != nil {
			t.Fatalf("directory sync failure after rename returned error: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(next) {
			t.Fatalf("directory sync failure did not leave new contents: %q, %v", got, err)
		}
	})
	t.Run("protected read failure refuses open", func(t *testing.T) {
		called := false
		_, err := openRegistryWith(context.Background(), filepath.Join(t.TempDir(), "pairs.json"), registryOps{
			readTrust: func(context.Context, string) ([]byte, error) {
				called = true
				return nil, errors.New("synthetic protected read failure")
			},
		})
		if !called || err == nil {
			t.Fatalf("protected read failure was not observed: called=%v err=%v", called, err)
		}
	})
}

func TestNativePairingRegistryExclusiveLockA14(t *testing.T) {
	// One process must own a registry lock so two gateways cannot race enrollment writes.
	path := filepath.Join(t.TempDir(), "pairs.json")
	ops := registryOps{readTrust: func(context.Context, string) ([]byte, error) { return []byte("synthetic protected file"), nil }}
	first, err := openRegistryWith(context.Background(), path, ops)
	if err != nil {
		t.Fatalf("first registry open: %v", err)
	}
	defer first.Close()
	second, err := openRegistryWith(context.Background(), path, ops)
	if err == nil {
		_ = second.Close()
		t.Fatal("second registry acquired an already-held lock")
	}
}

func TestNativePairingRegistryProtectedReadFailureA14(t *testing.T) {
	// A registry cannot be opened when its protected path check fails.
	called := false
	_, err := openRegistryWith(context.Background(), filepath.Join(t.TempDir(), "pairs.json"), registryOps{
		readTrust: func(context.Context, string) ([]byte, error) {
			called = true
			return nil, errors.New("synthetic protected read failure")
		},
	})
	if !called || err == nil {
		t.Fatalf("protected read failure was not observed: called=%v err=%v", called, err)
	}
}

func TestPairingRegistryRejectsInvalidRecordsA14(t *testing.T) {
	// Persisted records must match an available pairing slot and its configured identity.
	doc := pairingDoc(t)
	cfg, err := parseLocalConfig(encode(t, doc), configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		records []storedPairRecord
	}{
		{"unknown host label", []storedPairRecord{{HostLabel: "unknown", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"static host label", []storedPairRecord{{HostLabel: "mac-2", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"principal key duplicate", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x11), Fingerprint: fingerprint(t, 0x11), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"fingerprint mismatch", []storedPairRecord{{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x32), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"}}},
		{"duplicate key", []storedPairRecord{
			{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"},
			{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"},
		}},
		{"same slot twice", []storedPairRecord{
			{HostLabel: "mac-1", PublicKey: authorized(t, 0x31), Fingerprint: fingerprint(t, 0x31), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"},
			{HostLabel: "mac-1", PublicKey: authorized(t, 0x32), Fingerprint: fingerprint(t, 0x32), Issuer: "https://issuer.example.invalid/", Subject: "synthetic-subject-1", RegisteredAt: "2026-11-18T00:00:00Z"},
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

func TestPairingRegistryRoundTripsWrittenRecordsA14(t *testing.T) {
	// The gateway must be able to restart from the registry it wrote itself,
	// and a missing registry file means every slot starts empty.
	cfg, err := parseLocalConfig(encode(t, pairingDoc(t)), configPath)
	if err != nil {
		t.Fatal(err)
	}
	if records, err := validatePairingRegistry(nil, cfg); err != nil || len(records) != 0 {
		t.Fatalf("missing registry: records=%d err=%v", len(records), err)
	}
	want := agentgateway.PairRecord{HostLabel: "mac-1", HostKey: publicOf(t, 0x31).Marshal(), Issuer: "https://issuer.example.invalid/",
		Subject: "synthetic-subject-1", RegisteredAt: time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)}
	raw, err := encodePairingRegistry([]agentgateway.PairRecord{want})
	if err != nil {
		t.Fatal(err)
	}
	got, err := validatePairingRegistry(raw, cfg)
	if err != nil || len(got) != 1 || got[0].HostLabel != want.HostLabel || !bytes.Equal(got[0].HostKey, want.HostKey) ||
		got[0].Issuer != want.Issuer || got[0].Subject != want.Subject || !got[0].RegisteredAt.Equal(want.RegisteredAt) {
		t.Fatalf("round trip lost a record: got=%+v err=%v", got, err)
	}
	if _, err := encodePairingRegistry([]agentgateway.PairRecord{{HostLabel: "mac-1", HostKey: []byte("not a key")}}); err == nil {
		t.Fatal("an unparsable host key must not be written")
	}
}
