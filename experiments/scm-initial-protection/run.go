package main

import (
	"context"
	"encoding/json"
	"time"
)

const (
	serviceName      = "RescueHostProtectionProbe-20261030"
	reportPath       = `C:\ProgramData\RescueHost-ProtectionProbe-20261030\reports\subject.json`
	maxQueryBytes    = 64 * 1024
	maxReportBytes   = 256 * 1024
	maxSDDLBytes     = 32 * 1024
	maxGroups        = 128
	maxPrivileges    = 64
	cooperativeLimit = 15 * time.Second
	hardLimit        = 20 * time.Second
)

// Failure intentionally has no free-form OS error, hostname, path, or account name.
type Failure struct {
	Stage string `json:"stage"`
	Win32 uint32 `json:"win32_code"`
}

type Security struct {
	OwnerSID    string `json:"owner_sid"`
	DACLPresent bool   `json:"dacl_present"`
	DACLNull    bool   `json:"dacl_null"`
	SDDL        string `json:"owner_dacl_sddl"`
}

type Group struct {
	SID        string `json:"sid"`
	Attributes uint32 `json:"attributes"`
}

type Privilege struct {
	LUIDLow    uint32 `json:"luid_low"`
	LUIDHigh   int32  `json:"luid_high"`
	Attributes uint32 `json:"attributes"`
}

type TokenObservation struct {
	User                     Group           `json:"user"`
	Groups                   []Group         `json:"groups"`
	RestrictedSIDs           []Group         `json:"restricted_sids"`
	Privileges               []Privilege     `json:"privileges"`
	OwnerSID                 string          `json:"token_owner_sid"`
	DefaultDACLNull          bool            `json:"token_default_dacl_null"`
	DefaultDACLHex           string          `json:"token_default_dacl_hex"`
	Type                     uint32          `json:"token_type"`
	SessionID                uint32          `json:"session_id"`
	ElevationType            uint32          `json:"elevation_type"`
	Elevated                 uint32          `json:"elevated"`
	Integrity                Group           `json:"integrity"`
	HasRestrictions          uint32          `json:"has_restrictions"`
	IsAppContainer           uint32          `json:"is_app_container"`
	Statistics               TokenStatistics `json:"statistics"`
	ServiceSIDPresentEnabled bool            `json:"service_sid_present_enabled"`
	ExpectedServiceSID       string          `json:"expected_service_sid"`
}

type TokenStatistics struct {
	TokenIDLow           uint32 `json:"token_id_low"`
	TokenIDHigh          int32  `json:"token_id_high"`
	AuthenticationIDLow  uint32 `json:"authentication_id_low"`
	AuthenticationIDHigh int32  `json:"authentication_id_high"`
	ModifiedIDLow        uint32 `json:"modified_id_low"`
	ModifiedIDHigh       int32  `json:"modified_id_high"`
}

type Snapshot struct {
	Phase                       string           `json:"phase"`
	StartedAfterEntryNS         int64            `json:"started_after_service_entry_ns"`
	FinishedAfterEntryNS        int64            `json:"finished_after_service_entry_ns"`
	ProcessID                   uint32           `json:"process_id"`
	WorkerThreadID              uint32           `json:"worker_thread_id"`
	ProcessCreationFiletime     uint64           `json:"process_creation_filetime"`
	WorkerCreationFiletime      uint64           `json:"worker_creation_filetime"`
	Process                     Security         `json:"process_object"`
	WorkerThread                Security         `json:"worker_thread_object"`
	PrimaryTokenObject          Security         `json:"primary_token_object"`
	PrimaryToken                TokenObservation `json:"primary_token"`
	WorkerHasImpersonationToken bool             `json:"worker_has_impersonation_token"`
}

type Report struct {
	Schema            string     `json:"schema"`
	Privacy           string     `json:"privacy"`
	ProductVerdict    string     `json:"product_verdict"`
	InitialProtection string     `json:"initial_protection"`
	TimingScope       string     `json:"timing_scope"`
	CaptureStatus     string     `json:"capture_status"`
	PersistenceStatus string     `json:"persistence_status"`
	Snapshots         []Snapshot `json:"snapshots"`
	Failures          []Failure  `json:"failures"`
}

// Collector owns self-only query handles. Capture and Close run serially on one
// worker; the native service locks that worker to an OS thread.
type Collector interface {
	Capture(context.Context, string) (Snapshot, []Failure)
	Close() []Failure
}

// ReportWriter must check flush and close; failures affect the service exit code.
type ReportWriter interface{ WriteReport([]byte) []Failure }

func Run(ctx context.Context, c Collector, w ReportWriter, status func(string)) Report {
	ctx, cancel := context.WithTimeout(ctx, cooperativeLimit)
	defer cancel()
	r := Report{
		Schema: "rescue-host-scm-self-observation-v1", Privacy: "PRIVATE_SIDS_AND_SECURITY_DESCRIPTORS",
		ProductVerdict: "NOT_EVALUATED", InitialProtection: "UNKNOWN",
		TimingScope:   "Both samples follow Go runtime initialization; first locked worker is not the process initial thread; snapshots are non-atomic current observations.",
		CaptureStatus: "complete", PersistenceStatus: "REQUIRES_CLEAN_SERVICE_EXIT_AND_CONTROLLER_VERIFICATION",
		Snapshots: []Snapshot{}, Failures: []Failure{},
	}
	status("start_pending")
	if ctx.Err() == nil {
		first, failures := c.Capture(ctx, "first_locked_worker_after_go_runtime_init")
		r.Snapshots = append(r.Snapshots, first)
		r.Failures = append(r.Failures, failures...)
	}
	r.Failures = append(r.Failures, contextFailures(ctx)...)
	// Running is diagnostic control readiness, even if the first sample failed.
	// The native status adapter holds this state for a bounded five seconds.
	status("running")
	if len(r.Failures) == 0 {
		r.Failures = append(r.Failures, contextFailures(ctx)...)
	}
	if len(r.Failures) == 0 {
		second, failures := c.Capture(ctx, "same_locked_worker_after_running")
		r.Snapshots = append(r.Snapshots, second)
		r.Failures = append(r.Failures, failures...)
		r.Failures = append(r.Failures, contextFailures(ctx)...)
	}
	status("stop_pending")
	r.Failures = append(r.Failures, c.Close()...)
	if len(r.Failures) == 0 {
		r.Failures = append(r.Failures, contextFailures(ctx)...)
	}
	if len(r.Failures) != 0 {
		r.CaptureStatus = "failed"
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > maxReportBytes {
		r.CaptureStatus = "failed"
		r.Failures = append(r.Failures, Failure{Stage: "report_bound"})
		return r
	}
	r.Failures = append(r.Failures, w.WriteReport(data)...)
	if len(r.Failures) == 0 {
		r.Failures = append(r.Failures, contextFailures(ctx)...)
	}
	if len(r.Failures) != 0 {
		r.CaptureStatus = "failed"
	}
	return r
}

func contextFailures(ctx context.Context) []Failure {
	if ctx.Err() == context.DeadlineExceeded {
		return []Failure{{Stage: "capture_timeout"}}
	}
	if ctx.Err() != nil {
		return []Failure{{Stage: "capture_canceled"}}
	}
	return nil
}
