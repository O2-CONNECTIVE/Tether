//go:build !unix

package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNativePairingRegistryRefusedWithoutProtectedStore(t *testing.T) {
	// Without the unix lock and atomic rename, a pairing registry could be
	// written by two processes or left half-written, so it never opens.
	if store, err := openNativeRegistry(context.Background(), filepath.Join(t.TempDir(), "pairs.json"), nil); err == nil || store != nil {
		t.Fatal("pairing registry opened on a platform without the protected store")
	}
}
