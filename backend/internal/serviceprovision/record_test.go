package serviceprovision

import (
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

func TestValidateRecordRejectsUnconfiguredValues(t *testing.T) {
	if err := ValidateRecord(Record{}); err != ErrRecord {
		t.Fatal("未構成の配置記録を受理しました")
	}
}

func validRecord() Record {
	const user = "S-1-5-21-1-2-3-1000"
	const service = "S-1-5-80-1-2-3-4-5"
	volume := protectedkey.VolumeProfile{Root: `C:\`, Filesystem: "NTFS", Serial: 7}
	nodes := func(path string) []protectedkey.NodePolicy {
		return []protectedkey.NodePolicy{
			{Path: `C:\`, OwnerSID: user, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}},
			{Path: path, OwnerSID: user, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}},
		}
	}
	keys := protectedkey.Profile{Version: 1, ProcessUserSID: user, ServiceSID: service, OperatorSID: "S-1-5-21-1-2-3-1001", GuestSID: "S-1-5-21-1-2-3-1002", Volume: volume, Provisioning: protectedkey.ProvisioningRecord{Reference: "synthetic", ProtectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Catalog: map[string]protectedkey.KeyProfile{"fixed_host": {Path: `C:\host-key`, Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Nodes: nodes(`C:\host-key`)}}}
	image := func(path string) bootstrapauth.Image {
		return bootstrapauth.Image{Path: path, SHA256: [32]byte{1}, MaxBytes: 1024, Volume: volume, Nodes: nodes(path)}
	}
	return Record{Protocol: Protocol, Profile: bootstrapauth.Profile{Keys: keys, ServiceName: "SyntheticService", SupervisorImage: image(`C:\supervisor.exe`), ChildImage: image(`C:\child.exe`)}, Endpoint: `\\.\pipe\rescue-service-SyntheticService`, ServiceCommandLine: `"C:\supervisor.exe"`, ServiceSecurity: serviceready.SecurityPolicy{OwnerSID: user, Protected: true, ACL: []protectedkey.ACE{{SID: service, Mask: 0xf01ff}}}, ProcessSecurity: serviceready.SecurityPolicy{OwnerSID: user, Protected: true, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1fffff}}}, ReadyCleanupTimeout: time.Second, Budgets: servicehost.Budgets{Startup: 2 * time.Second, Cleanup: 2 * time.Second, Observe: time.Millisecond}, PolicyRef: "fixed_policy", PollInterval: time.Millisecond, OperationTimeout: time.Second, StdioCleanupTimeout: time.Second, StderrBytes: 4096, HostKeyRef: "fixed_host", Limits: stdio.Limits{FrameBytes: 16384, Frames: 10, CleanupTimeout: time.Second}, Backend: bootstrap.Config{OperationMode: "restricted", AllowedKeys: []string{"AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea"}, MaximumDuration: 20 * time.Second, LeaseDuration: time.Second, MaxChannels: 1, MaxHandshakes: 2, MaxTransports: 3}}
}
func TestRecordQueryPolicyRemainsCandidateUntilRuntimeTokenCheck(t *testing.T) {
	for _, mask := range []uint32{0x1000, 0x400, 0x121400} {
		r := validRecord()
		r.ProcessSecurity.ACL = append(r.ProcessSecurity.ACL, protectedkey.ACE{SID: r.Profile.Keys.GuestSID, Mask: mask})
		if err := ValidateRecord(r); err != nil {
			t.Fatalf("query-only process candidate rejected: mask=%x err=%v", mask, err)
		}
		// Value validation is not token evidence; runtime Prepare checks it.
	}
}

func TestValidateRecordRequiresEveryFixedInput(t *testing.T) {
	if err := ValidateRecord(validRecord()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Record){
		"endpoint":           func(r *Record) { r.Endpoint = `\\.\pipe\other` },
		"command args":       func(r *Record) { r.ServiceCommandLine += ` --other` },
		"service protection": func(r *Record) { r.ServiceSecurity.Protected = false },
		"guest service write": func(r *Record) {
			r.ServiceSecurity.ACL = append(r.ServiceSecurity.ACL, protectedkey.ACE{SID: r.Profile.Keys.GuestSID, Mask: 0x10})
		},
		"guest process duplicate": func(r *Record) {
			r.ProcessSecurity.ACL = append(r.ProcessSecurity.ACL, protectedkey.ACE{SID: r.Profile.Keys.GuestSID, Mask: 0x40})
		},
		"ready budget":   func(r *Record) { r.ReadyCleanupTimeout = 0 },
		"startup budget": func(r *Record) { r.Budgets.Startup = 0 },
		"cleanup budget": func(r *Record) { r.Budgets.Cleanup = 0 },
		"observe budget": func(r *Record) { r.Budgets.Observe = 0 },
		"policy ref":     func(r *Record) { r.PolicyRef = "../other" },
		"poll":           func(r *Record) { r.PollInterval = 0 },
		"operation":      func(r *Record) { r.OperationTimeout = 0 },
		"stdio cleanup":  func(r *Record) { r.StdioCleanupTimeout = 0 },
		"stderr":         func(r *Record) { r.StderrBytes = 0 },
		"host ref":       func(r *Record) { r.HostKeyRef = "missing" },
		"frame bytes":    func(r *Record) { r.Limits.FrameBytes = 1 },
		"frame count":    func(r *Record) { r.Limits.Frames = 0 },
		"frame cleanup":  func(r *Record) { r.Limits.CleanupTimeout = 0 },
		"mode":           func(r *Record) { r.Backend.OperationMode = "other" },
		"keys":           func(r *Record) { r.Backend.AllowedKeys = nil },
		"max duration":   func(r *Record) { r.Backend.MaximumDuration = 3 * time.Hour },
		"lease":          func(r *Record) { r.Backend.LeaseDuration = time.Hour },
		"channels":       func(r *Record) { r.Backend.MaxChannels = 33 },
		"transports":     func(r *Record) { r.Backend.MaxTransports = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			r := validRecord()
			mutate(&r)
			if ValidateRecord(r) != ErrRecord {
				t.Fatal("不足または危険な公開配置値を受理しました")
			}
		})
	}
}
