package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func summaryFixture() Report {
	sid := "S-1-5-80-1-2-3-4-5"
	security := Security{OwnerSID: "S-1-5-18", DACLPresent: true, DACLProtected: true, ACEs: []ACE{{Type: 0, Mask: 0xf01ff, SID: "S-1-5-18", Known: true}, {Type: 0, Mask: 0xf01ff, SID: sid, Known: true}}, SDDL: "PRIVATE_SECRET_SENTINEL"}
	token := TokenObservation{User: Group{SID: "S-1-5-18"}, Type: 1, ExpectedServiceSID: sid, Groups: []Group{{SID: sid, Attributes: 4}}}
	snap := Snapshot{PrimaryTokenObject: security, PrimaryToken: token}
	return Report{Schema: "rescue-sessionio-system-observation-v1", Privacy: "PRIVATE_SIDS_AND_SECURITY_DESCRIPTORS", ProductVerdict: "NOT_EVALUATED", InitialProtection: "UNKNOWN", CaptureStatus: "complete", Snapshots: []Snapshot{snap, snap}, Failures: []Failure{}}
}
func TestSummaryOutputsOnlyBoundedObservationAndNeverRawPrivateValues(t *testing.T) {
	r := summaryFixture()
	b, _ := json.Marshal(r)
	var out bytes.Buffer
	if code := RunSummary(bytes.NewReader(b), &out); code != 0 {
		t.Fatal("valid observation summary refused", code)
	}
	var s PublicSummary
	if json.Unmarshal(out.Bytes(), &s) != nil || len(s.TokenObjectPolicy) != 2 || s.TokenObjectPolicy[0] != "MATCH_OBSERVED" || s.NativeAcceptance != "NOT_EVALUATED" || s.ProductGate != "CLOSED" {
		t.Fatal("summary missing exact scope/policy", out.String())
	}
	for _, secret := range []string{"PRIVATE_SECRET_SENTINEL", "S-1-5-80", "S-1-5-18", "owner_dacl_sddl"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("raw value leaked", secret)
		}
	}
	for _, bad := range [][]byte{[]byte("not-json"), append(append([]byte{}, b...), b...), []byte(strings.Repeat("x", maxReportBytes+1))} {
		out.Reset()
		if RunSummary(bytes.NewReader(bad), &out) == 0 || out.Len() != 0 {
			t.Fatal("invalid/unbounded/trailing report accepted")
		}
	}
}
func TestTokenPolicyUnknownAndMismatchCannotBecomeMatch(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*Security)
	}{
		{"unprotected", "MISMATCH_OBSERVED", func(s *Security) { s.DACLProtected = false }},
		{"admin-owner", "MISMATCH_OBSERVED", func(s *Security) { s.OwnerSID = "S-1-5-32-544" }},
		{"extra-admin-ace", "MISMATCH_OBSERVED", func(s *Security) {
			s.ACEs = append(s.ACEs, ACE{Type: 0, Mask: 0xf01ff, SID: "S-1-5-32-544", Known: true})
		}},
		{"unknown-ace", "UNKNOWN", func(s *Security) { s.ACEs = []ACE{{Type: 9}} }},
		{"inherit-flags", "MISMATCH_OBSERVED", func(s *Security) { s.ACEs[0].Flags = 16 }},
		{"query-missing", "UNKNOWN", func(s *Security) { *s = Security{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := summaryFixture()
			r.Snapshots[0].PrimaryTokenObject.ACEs = append([]ACE(nil), r.Snapshots[0].PrimaryTokenObject.ACEs...)
			tc.change(&r.Snapshots[0].PrimaryTokenObject)
			b, _ := json.Marshal(r)
			var out bytes.Buffer
			if RunSummary(bytes.NewReader(b), &out) != 0 {
				t.Fatal("collection refused")
			}
			var s PublicSummary
			if json.Unmarshal(out.Bytes(), &s) != nil || len(s.TokenObjectPolicy) != 2 || s.TokenObjectPolicy[0] != tc.want {
				t.Fatal("policy promoted/misclassified", out.String())
			}
		})
	}
}

func TestSummaryCannotClaimPersistenceFromReportBytesAlone(t *testing.T) {
	r := summaryFixture()
	b, _ := json.Marshal(r)
	var out bytes.Buffer
	if RunSummary(bytes.NewReader(b), &out) != 0 {
		t.Fatal("observation refused")
	}
	var fields map[string]any
	if json.Unmarshal(out.Bytes(), &fields) != nil || fields["persistence_status"] != "REQUIRES_ORIGINAL_EXIT_AND_CONTROLLER_VERIFICATION" {
		t.Fatal("report bytes promoted without writer/exit proof", out.String())
	}
}

func TestSummaryRejectsMissingNullDuplicateAndCaseAliasFields(t *testing.T) {
	b, _ := json.Marshal(summaryFixture())
	original := string(b)
	for _, bad := range []string{
		strings.Replace(original, `,"worker_has_impersonation_token":false`, "", 1),
		strings.Replace(original, `"dacl_null":false`, `"dacl_null":null`, 1),
		strings.Replace(original, `"capture_status":"complete"`, `"capture_status":"failed","capture_status":"complete"`, 1),
		strings.Replace(original, `"capture_status":`, `"CAPTURE_STATUS":`, 1),
	} {
		if bad == original {
			t.Fatal("fixture did not alter exact report shape")
		}
		var out bytes.Buffer
		if RunSummary(strings.NewReader(bad), &out) == 0 || out.Len() != 0 {
			t.Fatal("noncanonical/missing observation promoted", out.String())
		}
	}
}
func TestSummaryDigestBindsOriginalReportAcrossTextTransportNewline(t *testing.T) {
	b, _ := json.Marshal(summaryFixture())
	sum := sha256.Sum256(b)
	var out bytes.Buffer
	if RunSummary(bytes.NewReader(append(append([]byte{}, b...), '\r', '\n')), &out) != 0 {
		t.Fatal("outer transport newline refused")
	}
	var s PublicSummary
	if json.Unmarshal(out.Bytes(), &s) != nil || s.ReportSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("digest rebased to text transport", out.String())
	}
}
