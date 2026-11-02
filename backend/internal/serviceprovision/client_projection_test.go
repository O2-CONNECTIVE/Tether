package serviceprovision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func clientProjectionRecord() []byte {
	return []byte(strings.ReplaceAll(validJSON, "S-1-5-21-1-2-3-1000", "S-1-5-18"))
}
func TestClientProjectionPairsExactRecordWithSelectedBuild(t *testing.T) {
	data := clientProjectionRecord()
	view, err := DeriveUntrustedClientProjection(data, [32]byte{1})
	sum := sha256.Sum256(data)
	if err != nil || view.Activation != "blocked" || view.Trust != "unverified" || view.RecordSHA256 != hex.EncodeToString(sum[:]) || view.ServiceName != "SyntheticService" || view.ProcessUserSID != "S-1-5-18" {
		t.Fatalf("candidate: %+v %v", view, err)
	}
	if _, err = DeriveUntrustedClientProjection(data, [32]byte{2}); err == nil {
		t.Fatal("different selected build accepted")
	}
}

func TestClientProjectionRetainsExactBytesAndNeverSerializesRuntimeAuthority(t *testing.T) {
	data := clientProjectionRecord()
	first, err := DeriveUntrustedClientProjection(data, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeriveUntrustedClientProjection(append(append([]byte(nil), data...), '\n'), [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if first.RecordSHA256 == second.RecordSHA256 {
		t.Fatal("record was remarshaled before hashing")
	}
	first.RecordSHA256 = ""
	second.RecordSHA256 = ""
	if !reflect.DeepEqual(first, second) {
		t.Fatal("LF changed projected expectations")
	}
	encoded, err := EncodeUntrustedClientProjection(data, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	again, err := EncodeUntrustedClientProjection(data, [32]byte{1})
	if err != nil || !bytes.Equal(encoded, again) || encoded[len(encoded)-1] != '\n' {
		t.Fatal("non-deterministic artifact")
	}
	var object map[string]any
	if err = json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	expected := []string{"protocol", "activation", "trust", "sourceProtocol", "recordSHA256", "serviceBuildSHA256", "serviceName", "endpoint", "serviceCommandLine", "processUserSID", "serviceSID", "operatorSID", "excludedGuestSID", "volume", "serviceImage", "serviceSecurityExpectation", "processSecurityExpectation"}
	if len(object) != len(expected) {
		t.Fatalf("unexpected fields: %v", object)
	}
	for _, key := range expected {
		if _, ok := object[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	if object["serviceCommandLine"] != `"C:\supervisor.exe"` || object["excludedGuestSID"] != "S-1-5-21-1-2-3-1002" {
		t.Fatal("fixed source expectations omitted")
	}
}
func TestClientProjectionHasNoCallerOverrideOrMutableAlias(t *testing.T) {
	data := clientProjectionRecord()
	first, err := DeriveUntrustedClientProjection(data, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	first.ServiceImage.Nodes[0].ACL[0].SID = "changed"
	first.ProcessSecurity.ACL[0].Mask = 0
	first.ServiceName = "changed"
	second, err := DeriveUntrustedClientProjection(data, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if second.ServiceName != "SyntheticService" || second.ServiceImage.Nodes[0].ACL[0].SID != "S-1-5-80-1-2-3-4-5" || second.ProcessSecurity.ACL[0].Mask != 0x1fffff {
		t.Fatal("projection alias")
	}
	clear(data)
	if second.ServiceName != "SyntheticService" {
		t.Fatal("input buffer alias")
	}
}
func TestClientProjectionRejectsAmbiguousUnsafeOrWrongPair(t *testing.T) {
	good := string(clientProjectionRecord())
	for name, data := range map[string]string{
		"crlf":         good + "\r\n",
		"empty":        "",
		"legacy-user":  validJSON,
		"duplicate":    strings.Replace(good, `{"protocol":`, `{"protocol":"rescue-service-provisioning/1","protocol":`, 1),
		"authority":    strings.Replace(good, `{"protocol":`, `{"authority":true,"protocol":`, 1),
		"endpoint":     strings.Replace(good, "rescue-service-SyntheticService", "arbitrary-endpoint", 1),
		"unprotected":  strings.Replace(good, `"protected":true`, `"protected":false`, 1),
		"image-digest": strings.Replace(good, "0100000000000000000000000000000000000000000000000000000000000000", "0200000000000000000000000000000000000000000000000000000000000000", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DeriveUntrustedClientProjection([]byte(data), [32]byte{1}); err == nil {
				t.Fatal("unsafe pair accepted")
			}
		})
	}
	if _, err := DeriveUntrustedClientProjection(clientProjectionRecord(), [32]byte{}); err == nil {
		t.Fatal("zero build digest")
	}
}
