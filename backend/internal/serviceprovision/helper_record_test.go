package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// Synthetic configuration only. No service/account or credential is created.
const helperSupplierJSON = `{"serviceName":"SyntheticAuth","serviceSid":"S-1-5-80-6-7-8-9-10","serviceCommandLine":"\"C:\\auth.exe\" --auth-supplier-internal","image":{"path":"C:\\auth.exe","sha256":"0200000000000000000000000000000000000000000000000000000000000000","maxBytes":"1024","volume":{"root":"C:\\","filesystem":"NTFS","serial":"7"},"nodes":[{"path":"C:\\","ownerSid":"S-1-5-18","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-18"}]},{"path":"C:\\auth.exe","ownerSid":"S-1-5-18","acl":[{"type":"0","flags":"0","mask":"2032127","sid":"S-1-5-18"}]}]},"serviceSecurity":{"ownerSid":"S-1-5-18","protected":true,"acl":[{"type":"0","flags":"0","mask":"983551","sid":"S-1-5-18"}]},"processSecurity":{"ownerSid":"S-1-5-18","protected":true,"acl":[{"type":"0","flags":"0","mask":"2097151","sid":"S-1-5-18"}]},"tokenSecurity":{"ownerSid":"S-1-5-18","protected":false,"acl":[{"type":"0","flags":"0","mask":"983551","sid":"S-1-5-18"}]},"childProcessSddl":"D:P(A;;GA;;;S-1-5-18)(A;;GA;;;S-1-5-80-1-2-3-4-5)(A;;0x120400;;;S-1-5-80-6-7-8-9-10)","childThreadSddl":"D:P(A;;GA;;;S-1-5-18)(A;;GA;;;S-1-5-80-1-2-3-4-5)(A;;0x120040;;;S-1-5-80-6-7-8-9-10)","cleanupNs":"1000000000","runBudgetNs":"1000000000"}`

func helperRecordJSON() string {
	base := strings.ReplaceAll(validJSON, "S-1-5-21-1-2-3-1000", "S-1-5-18")
	return strings.TrimSuffix(base, "}") + `,"helperSupplier":` + helperSupplierJSON + "}"
}

func TestProtectedRecordDecodesExplicitHelperSupplier(t *testing.T) {
	r, err := DecodeRecord([]byte(helperRecordJSON()))
	if err != nil || r.HelperSupplier == nil || r.HelperSupplier.ServiceName != "SyntheticAuth" || r.HelperSupplier.Image.SHA256 != ([32]byte{2}) || r.HelperSupplier.RunBudget != time.Second {
		t.Fatalf("explicit fixed helper supplier rejected: %v", err)
	}
	legacy, err := DecodeRecord([]byte(validJSON))
	if err != nil || legacy.HelperSupplier != nil {
		t.Fatal("absent supplier was defaulted or legacy record changed", err)
	}
}

func TestHelperRecordRejectsNullPartialUnknownAndUnsafeConfiguration(t *testing.T) {
	valid := helperRecordJSON()
	for name, data := range map[string]string{
		"null":             strings.Replace(valid, helperSupplierJSON, "null", 1),
		"empty":            strings.Replace(valid, helperSupplierJSON, "{}", 1),
		"missing":          strings.Replace(valid, `,"runBudgetNs":"1000000000"`, "", 1),
		"unknown":          strings.Replace(valid, `"serviceName":"SyntheticAuth"`, `"serviceName":"SyntheticAuth","token":"not-allowed"`, 1),
		"duplicate":        strings.Replace(valid, `"serviceName":"SyntheticAuth"`, `"serviceName":"SyntheticAuth","serviceName":"Other"`, 1),
		"case":             strings.Replace(valid, `"helperSupplier":`, `"HelperSupplier":`, 1),
		"same service":     strings.Replace(valid, `"serviceName":"SyntheticAuth"`, `"serviceName":"SyntheticService"`, 1),
		"shared principal": strings.Replace(valid, `"serviceSid":"S-1-5-80-6-7-8-9-10"`, `"serviceSid":"S-1-5-19"`, 1),
		"guest writer":     strings.Replace(valid, `"tokenSecurity":{"ownerSid":"S-1-5-18","protected":false,"acl":[{"type":"0","flags":"0","mask":"983551","sid":"S-1-5-18"}]}`, `"tokenSecurity":{"ownerSid":"S-1-5-18","protected":false,"acl":[{"type":"0","flags":"0","mask":"983551","sid":"S-1-5-21-1-2-3-1002"}]}`, 1),
		"budget numeric":   strings.Replace(valid, `"runBudgetNs":"1000000000"`, `"runBudgetNs":1000000000`, 1),
		"budget exceeded":  strings.Replace(valid, `"runBudgetNs":"1000000000"`, `"runBudgetNs":"60000000001"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if data == valid {
				t.Fatal("mutation missing")
			}
			if got, err := DecodeRecord([]byte(data)); err == nil || !reflect.DeepEqual(got, Record{}) {
				t.Fatal("invalid supplier returned a record", err)
			}
		})
	}
}

func TestHelperRecordDigestIncludesSupplierAndHasNoMutableAlias(t *testing.T) {
	raw := []byte(helperRecordJSON())
	subject := Subject{ProcessUserSID: "S-1-5-18", ServiceSID: rootServiceSID, ServiceName: "SyntheticService"}
	first, r, err := resolvePreviewRecord(raw, subject, time.Now())
	if err != nil || first.ConfigDigest != sha256.Sum256(raw) {
		t.Fatal("supplier outside exact digest", err)
	}
	r.HelperSupplier.Image.Nodes[0].ACL[0].SID = "changed"
	next, unchanged, err := resolvePreviewRecord(raw, subject, time.Now())
	if err != nil || first.ConfigDigest != next.ConfigDigest || unchanged.HelperSupplier.Image.Nodes[0].ACL[0].SID == "changed" {
		t.Fatal("record aliased supplier", err)
	}
	mutated := []byte(strings.Replace(string(raw), `"runBudgetNs":"1000000000"`, `"runBudgetNs":"2000000000"`, 1))
	next, _, err = resolvePreviewRecord(mutated, subject, time.Now())
	if err != nil || first.ConfigDigest == next.ConfigDigest {
		t.Fatal("supplier policy change omitted from approval digest", err)
	}
}

// This runs the real fixed-record decoder and original Execution composition;
// only root OS observations and the existing scope seam are in-memory models.
func TestOriginalOwnerComposesHeldHelperRecordWithoutNativeAcquisition(t *testing.T) {
	a := rootAnchor()
	k := provisionRecordKernel(a)
	k.identity.user = "S-1-5-18"
	raw := []byte(helperRecordJSON())
	k.files[a.RecordPath].data = raw
	k.files[a.RecordPath].state.size = int64(len(raw))
	digest := sha256.Sum256(raw)
	k.files[a.DigestPath].data = []byte(hex.EncodeToString(digest[:]) + "\n")
	r, _ := NewNativeRoot(a)
	r.api = k
	loaded, err := r.Open(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	p := &Prepared{loaded: loaded, scope: &previewMemoryScope{}}
	p.accepted.Store(true)
	// The mutable cache cannot inject or replace the held Root's supplier.
	p.record.HelperSupplier = nil
	e, err := NewNativePreviewExecution(previewExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	stage := &nativeExecutionStage{cfg: e.cfg, preview: e.preview, calls: nativeExecutionCalls{prepare: func(context.Context, Anchor) (*Prepared, error) { return p, nil }}}
	ready := make(chan executionReady, 1)
	go e.own(rootContext(t), func() executionStage { return stage }, ready)
	ctx := rootContext(t)
	var state executionReady
	select {
	case state = <-ready:
	case <-ctx.Done():
		t.Fatal("owner did not return")
	}
	if state.err != nil || state.service == nil {
		t.Fatal(state.err)
	}
	// The closed acquisition gate registers no supplier Service.
	if stage.supplier != nil {
		t.Fatal("closed gate held a supplier registration")
	}
	policy, err := p.operatorSIDPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.service.BindOperatorSupplier(e, policy, *mustHelperRecord(t).HelperSupplier); !errors.Is(err, servicehost.ErrAlreadyUsed) {
		t.Fatal("record composition not single use", err)
	}
	if err = state.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.cleaned:
	case <-ctx.Done():
		t.Fatal("root owner not joined")
	}
	if stage.prepared != nil || k.token.closes != 1 {
		t.Fatal("held root lost cleanup")
	}
}

func mustHelperRecord(t *testing.T) Record {
	t.Helper()
	r, err := DecodeRecord([]byte(helperRecordJSON()))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
