package serviceprovision

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func purposeRecordFixture(t *testing.T) []byte {
	t.Helper()
	record := validRecord()
	data, err := json.Marshal(map[string]any{
		"protocol":           "rescue-windows-agent-purpose/1",
		"purpose":            "single-guest-command",
		"serviceDigest":      strings.Repeat("01", 32),
		"runtimeGuestDigest": strings.Repeat("02", 32),
		"operatorKeys":       record.Backend.AllowedKeys,
		"maximumDurationNs":  "300000000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAgentPurposeRecordIsSeparateAndDerivesOnlyFixedPurpose(t *testing.T) {
	purpose, err := decodeAgentPurpose(purposeRecordFixture(t))
	if err != nil || purpose.maximum != 300000000000 {
		t.Fatalf("valid purpose: %+v %v", purpose, err)
	}
	if _, err := decodeAgentPurpose([]byte(validJSON)); err == nil {
		t.Fatal("legacy SSH record became agent purpose")
	}
}

func TestAgentPurposeRejectsUnboundedAmbiguousOrAuthorityFields(t *testing.T) {
	raw := purposeRecordFixture(t)
	cases := map[string][]byte{
		"oversize":          append(raw, bytes.Repeat([]byte(" "), agentPurposeBytes)...),
		"leading-space":     append([]byte(" "), raw...),
		"double-line":       append(append([]byte(nil), raw...), []byte("\n\n")...),
		"invalid-utf8":      {'{', 0xff, '}'},
		"duplicate":         bytes.Replace(raw, []byte(`"purpose":`), []byte(`"purpose":"single-guest-command","purpose":`), 1),
		"duplicate-escaped": bytes.Replace(raw, []byte(`"purpose":`), []byte(`"pur\u0070ose":"single-guest-command","purpose":`), 1),
	}
	for name, change := range map[string]any{
		"protocol": "rescue-service-provisioning/1", "purpose": "ssh-forward",
		"serviceDigest": strings.Repeat("00", 32), "runtimeGuestDigest": strings.Repeat("AB", 32),
		"maximumDurationNs": "300000000001", "operatorKeys": []string{},
		"approval": true, "policyDigest": strings.Repeat("01", 32), "command": "whoami",
	} {
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		object[name] = change
		data, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		cases[name] = data
	}
	for _, duration := range []string{"0", "-1", "01", "1.0", "9223372036854775808"} {
		cases["duration-"+duration] = bytes.Replace(raw, []byte("300000000000"), []byte(duration), 1)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAgentPurpose(data); err == nil {
				t.Fatal("unsafe record accepted")
			}
		})
	}
}

func TestAgentPurposeRejectsRepeatedOperatorAndCopiesKeys(t *testing.T) {
	raw := purposeRecordFixture(t)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	keys := object["operatorKeys"].([]any)
	object["operatorKeys"] = []any{keys[0], keys[0]}
	repeated, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeAgentPurpose(repeated); err == nil {
		t.Fatal("duplicate operator accepted")
	}
	valid, err := decodeAgentPurpose(append(append([]byte(nil), raw...), byte('\n')))
	if err != nil {
		t.Fatal(err)
	}
	if len(valid.keys) != 1 || len(valid.fingerprints) != 1 {
		t.Fatal("key evidence absent")
	}
	clear(raw)
	if valid.keys[0] == "" {
		t.Fatal("input buffer retained")
	}
}
