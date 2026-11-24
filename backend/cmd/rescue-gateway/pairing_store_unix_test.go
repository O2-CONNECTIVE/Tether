//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
		if err := store.Save(context.Background(), next); !errors.Is(err, agentgateway.ErrRegistryDurabilityUnknown) {
			t.Fatalf("directory sync failure after rename = %v, want durability unknown", err)
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

func TestNativePairingRegistrySaveRechecksRegistryProtectionA14(t *testing.T) {
	// A registry file that lost its protection after start must not be
	// replaced as if nothing happened: the save fails and nothing changes.
	path := filepath.Join(t.TempDir(), "pairs.json")
	old := []byte(`{"hosts":[]}`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	unprotected := false
	store, err := openRegistryWith(context.Background(), path, registryOps{
		readTrust: func(_ context.Context, p string) ([]byte, error) {
			if unprotected && p == path {
				return nil, errors.New("synthetic unprotected registry")
			}
			return os.ReadFile(p)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	unprotected = true
	if err := store.Save(context.Background(), []byte(`{"hosts":[{"label":"new"}]}`)); err == nil {
		t.Fatal("save succeeded over an unprotected registry file")
	}
	if got, _ := os.ReadFile(path); string(got) != string(old) {
		t.Fatalf("registry changed after a refused save: %q", got)
	}
}

func TestNativePairingRegistryOpenRequiresFileAndDirectorySync(t *testing.T) {
	// Startup must refuse a registry whose current bytes or parent directory cannot be synced.
	for _, tc := range []struct {
		name     string
		fileFail bool
	}{
		{name: "registry file sync", fileFail: true},
		{name: "parent directory sync"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pairs.json")
			if err := os.WriteFile(path, []byte(`{"hosts":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			ops := registryOps{readTrust: func(context.Context, string) ([]byte, error) {
				return []byte("synthetic protected file"), nil
			}}
			if tc.fileFail {
				ops.syncFile = func(*os.File) error { return errors.New("synthetic file sync failure") }
			} else {
				ops.syncDir = func(string) error { return errors.New("synthetic directory sync failure") }
			}
			store, err := openRegistryWith(context.Background(), path, ops)
			if store != nil {
				_ = store.Close()
			}
			if err == nil {
				t.Fatalf("open succeeded after %s failure", tc.name)
			}
		})
	}
}

func TestNativePairingRegistryRefusesSymlinkedLockA14(t *testing.T) {
	// The lock file is written before its protection is checked, so a planted
	// symlink must not let the gateway write through it.
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pairs.json")
	if err := os.Symlink(victim, path+".lock"); err != nil {
		t.Fatal(err)
	}
	store, err := openRegistryWith(context.Background(), path, registryOps{
		readTrust: func(context.Context, string) ([]byte, error) { return nil, nil },
	})
	if err == nil {
		_ = store.Close()
		t.Fatal("registry opened through a symlinked lock")
	}
	if got, _ := os.ReadFile(victim); len(got) != 0 {
		t.Fatalf("lock text was written through the symlink: %q", got)
	}
}
