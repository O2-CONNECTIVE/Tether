package grantstore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/grantstore"
)

func grant() supervisor.ProvisionedGrant {
	return supervisor.ProvisionedGrant{
		Binding:      supervisor.Binding{Generation: 7, Identity: supervisor.Identity{LaunchID: "approved-launch", Epoch: "1", PolicyDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest,
		HardDeadline: time.Date(2026, 10, 28, 12, 0, 20, 0, time.UTC), BackendLeaseCeiling: 3 * time.Second,
	}
}

func TestRecordRoundTripAndInterruptedOrCorruptRecordsFailClosed(t *testing.T) {
	record, err := grantstore.InitialRecord(grant())
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != grantstore.Ready || record.Revision != 1 || record.Generation != 7 {
		t.Fatalf("initial metadata: %+v", record)
	}
	encoded, err := grantstore.EncodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 89 || string(encoded[:8]) != "RHGRANT1" {
		t.Fatalf("unexpected storage format: %d bytes", len(encoded))
	}
	for _, name := range []string{"valid", "interrupted", "corrupt", "trailing"} {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), encoded...)
			switch name {
			case "interrupted":
				data = data[:len(data)/2]
			case "corrupt":
				data[20] ^= 1
			case "trailing":
				data = append(data, 0)
			}
			path := filepath.Join(t.TempDir(), "synthetic-record")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			read, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := grantstore.DecodeRecord(read)
			if name == "valid" {
				if err != nil || got != record {
					t.Fatalf("round trip: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("bad record accepted")
			}
		})
	}
}

func TestInvalidMetadataAndEquivalentTimeZones(t *testing.T) {
	g := grant()
	first, err := grantstore.InitialRecord(g)
	if err != nil {
		t.Fatal(err)
	}
	g.HardDeadline = g.HardDeadline.In(time.FixedZone("synthetic", 9*60*60))
	second, err := grantstore.InitialRecord(g)
	if err != nil || second != first {
		t.Fatalf("same absolute deadline changed binding: %v %v", second, err)
	}
	for _, name := range []string{"phase", "revision", "generation", "digest"} {
		t.Run(name, func(t *testing.T) {
			r := first
			switch name {
			case "phase":
				r.Phase = 5
			case "revision":
				r.Revision = 2
			case "generation":
				r.Generation = 0
			case "digest":
				r.GrantDigest = [32]byte{}
			}
			if data, err := grantstore.EncodeRecord(r); data != nil || err == nil {
				t.Fatal("invalid metadata encoded")
			}
		})
	}
}
