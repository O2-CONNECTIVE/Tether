package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
)

// PublicSummary contains observations only, never SID/SDDL/LUID/PID, authority
// or an executable gate. All strings are fixed enumerations, not raw input.
type PublicSummary struct {
	PersistenceStatus               string   `json:"persistence_status"`
	Schema                          string   `json:"schema"`
	ReportSHA256                    string   `json:"report_sha256"`
	CaptureStatus                   string   `json:"capture_status"`
	SnapshotCount                   int      `json:"snapshot_count"`
	FailureCount                    int      `json:"failure_count"`
	SystemPrimaryObserved           bool     `json:"system_primary_observed"`
	DedicatedServiceEnabledObserved bool     `json:"dedicated_service_enabled_observed"`
	TokenObjectPolicy               []string `json:"token_object_policy"`
	NativeAcceptance                string   `json:"native_acceptance"`
	ProductGate                     string   `json:"product_gate"`
}

// RunSummary reads only bounded stdin. It invokes no SCM/token/file native API.
// A MATCH_OBSERVED value is not initial protection, authentication or admission.
func RunSummary(in io.Reader, out io.Writer) int {
	data, e := io.ReadAll(io.LimitReader(in, maxReportBytes+1))
	if e != nil || len(data) == 0 || len(data) > maxReportBytes {
		return 2
	}
	var r Report
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return 2
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return 2
	}
	if r.Schema != "rescue-sessionio-system-observation-v1" || r.Privacy != "PRIVATE_SIDS_AND_SECURITY_DESCRIPTORS" || r.ProductVerdict != "NOT_EVALUATED" || r.InitialProtection != "UNKNOWN" || len(r.Snapshots) > 2 || len(r.Failures) > 128 {
		return 2
	}
	if r.CaptureStatus != "complete" && r.CaptureStatus != "failed" {
		return 2
	}
	// Only this source's canonical Go report shape is accepted. This rejects
	// duplicate/case-alias/omitted/defaulted-null fields; outer transport newline
	// is ignored, but the digest remains the actual native writer's JSON bytes.
	canonical, e := json.Marshal(r)
	if e != nil || !bytes.Equal(bytes.TrimSpace(data), canonical) {
		return 2
	}
	sum := sha256.Sum256(canonical)
	s := PublicSummary{PersistenceStatus: "REQUIRES_ORIGINAL_EXIT_AND_CONTROLLER_VERIFICATION", Schema: "rescue-system-observation-summary-v1", ReportSHA256: hex.EncodeToString(sum[:]), CaptureStatus: r.CaptureStatus, SnapshotCount: len(r.Snapshots), FailureCount: len(r.Failures), TokenObjectPolicy: []string{}, NativeAcceptance: "NOT_EVALUATED", ProductGate: "CLOSED"}
	s.SystemPrimaryObserved = len(r.Snapshots) == 2
	s.DedicatedServiceEnabledObserved = len(r.Snapshots) == 2
	for _, v := range r.Snapshots {
		if len(v.PrimaryToken.Groups) > maxGroups || len(v.PrimaryTokenObject.ACEs) > maxGroups {
			return 2
		}
		t := v.PrimaryToken
		s.SystemPrimaryObserved = s.SystemPrimaryObserved && t.User.SID == "S-1-5-18" && t.Type == 1 && !v.WorkerHasImpersonationToken
		s.DedicatedServiceEnabledObserved = s.DedicatedServiceEnabledObserved && len(ValidateObservedSubject(t)) == 0
		policy := "UNKNOWN"
		if r.CaptureStatus == "complete" && len(r.Failures) == 0 {
			policy = tokenPolicy(v.PrimaryTokenObject, t.ExpectedServiceSID)
		}
		s.TokenObjectPolicy = append(s.TokenObjectPolicy, policy)
	}
	if r.CaptureStatus != "complete" || len(r.Failures) != 0 || len(r.Snapshots) != 2 {
		s.SystemPrimaryObserved = false
		s.DedicatedServiceEnabledObserved = false
	}
	b, e := json.Marshal(s)
	if e != nil || len(b) > 65535 {
		return 2
	}
	b = append(b, '\n')
	n, e := out.Write(b)
	if e != nil || n != len(b) {
		return 2
	}
	if r.CaptureStatus != "complete" || len(r.Failures) != 0 || len(r.Snapshots) != 2 {
		return 1
	}
	return 0
}
func tokenPolicy(s Security, sid string) string {
	if s.OwnerSID == "" || !serviceSID(sid) {
		return "UNKNOWN"
	}
	for _, a := range s.ACEs {
		if !a.Known {
			return "UNKNOWN"
		}
	}
	if s.OwnerSID != "S-1-5-18" || !s.DACLProtected || !s.DACLPresent || s.DACLNull || len(s.ACEs) != 2 {
		return "MISMATCH_OBSERVED"
	}
	expected := map[string]bool{"S-1-5-18": true, sid: true}
	for _, a := range s.ACEs {
		if a.Type != 0 || a.Flags != 0 || a.Mask != 0xf01ff || !expected[a.SID] {
			return "MISMATCH_OBSERVED"
		}
		delete(expected, a.SID)
	}
	if len(expected) != 0 {
		return "MISMATCH_OBSERVED"
	}
	return "MATCH_OBSERVED"
}
