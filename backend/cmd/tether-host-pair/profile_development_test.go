//go:build tether_development && darwin && cgo && !osusergo

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

func TestDevelopmentPairingUsesSeparateLocationAndRegisteredCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home, productPolicy, err := residentmac.EnrolledLocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var d deps
	configurePairing(&d)
	gotHome, gotPolicy, err := d.location(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantPolicy := filepath.Join(home, "Library", "Application Support", "com.o2connective.tether.development", "resident-policy.json")
	if gotHome != home || gotPolicy != wantPolicy || gotPolicy == productPolicy {
		t.Fatal("development pairing did not select its fixed OS-account location")
	}
	if d.callbackAddress != "127.0.0.1:51847" {
		t.Fatal("development pairing did not select the registered loopback callback")
	}
}
