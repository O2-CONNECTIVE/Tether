//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
