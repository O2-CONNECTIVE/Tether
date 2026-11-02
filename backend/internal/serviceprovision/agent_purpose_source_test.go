package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

func agentPurposeFixture(t *testing.T) (*Prepared, *GuestConfiguration, *Root, *rootFakeKernel) {
	t.Helper()
	prepared, guestRoot, guestKernel, data := runtimeConfigurationFixture(t)
	purposeAnchor, err := agentPurposeAnchor(prepared.loaded.anchor)
	if err != nil {
		t.Fatal(err)
	}
	var layout map[string]any
	if err = json.Unmarshal(data, &layout); err != nil {
		t.Fatal(err)
	}
	files := layout["files"].([]any)
	for index, path := range []string{purposeAnchor.RecordPath, purposeAnchor.DigestPath} {
		id := [16]byte{byte(14 + index)}
		files = append(files, map[string]any{"path": path, "role": "configuration", "directory": false, "volume": "7", "fileID": hex.EncodeToString(id[:]), "ownerSID": "S-1-5-18", "securitySDDL": "O:SYD:P(A;;FA;;;SY)"})
	}
	layout["files"] = files
	data, err = json.Marshal(layout)
	if err != nil {
		t.Fatal(err)
	}
	setGuestFixtureData(guestKernel, guestRoot.anchor, data)
	guest, err := prepared.loadRuntimeGuestConfiguration(rootContext(t), guestRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guest.Close(rootContext(t)) })
	guestDigest, err := guest.Digest(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	base, err := prepared.loaded.Record()
	if err != nil {
		t.Fatal(err)
	}
	serviceDigest := sha256.Sum256(base)
	purposeKernel := rootFixture(purposeAnchor)
	purposeKernel.identity.user = "S-1-5-18"
	purposeKernel.files[purposeAnchor.RecordPath].state.id = [16]byte{14}
	purposeKernel.files[purposeAnchor.DigestPath].state.id = [16]byte{15}
	var purpose map[string]any
	if err = json.Unmarshal(purposeRecordFixture(t), &purpose); err != nil {
		t.Fatal(err)
	}
	purpose["serviceDigest"] = hex.EncodeToString(serviceDigest[:])
	purpose["runtimeGuestDigest"] = hex.EncodeToString(guestDigest[:])
	purposeData, err := json.Marshal(purpose)
	if err != nil {
		t.Fatal(err)
	}
	setGuestFixtureData(purposeKernel, purposeAnchor, purposeData)
	root, err := NewNativeRoot(purposeAnchor)
	if err != nil {
		t.Fatal(err)
	}
	root.api = purposeKernel
	return prepared, guest, root, purposeKernel
}

func TestAgentPurposeLoadsConcreteProtectedInputsWithoutApproval(t *testing.T) {
	prepared, guest, root, kernel := agentPurposeFixture(t)
	owner, err := prepared.loadAgentPurpose(rootContext(t), guest, root)
	if err != nil || owner == nil {
		t.Fatalf("protected purpose load: %v", err)
	}
	defer owner.Close(rootContext(t))
	view, err := owner.Descriptor(rootContext(t))
	if err != nil || view.Purpose != "single-guest-command" || view.MaxCommands != 1 || len(view.OperatorFingerprints) != 1 {
		t.Fatalf("candidate: %+v %v", view, err)
	}
	if len(kernel.opened) != 4 {
		t.Fatalf("native Root path omitted: %v", kernel.opened)
	}
	if view.PolicyDigest == "" || view.ConfigDigest == ([32]byte{}) || view.KeySetDigest == ([32]byte{}) {
		t.Fatal("missing derived candidate")
	}
}

func TestAgentPurposeClockRollbackDuringRecheckRejectsCandidate(t *testing.T) {
	p, g, root, _ := agentPurposeFixture(t)
	owner, err := p.loadAgentPurpose(rootContext(t), g, root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(rootContext(t))
	calls := 0
	owner.now = func() time.Time {
		calls++
		if calls == 1 {
			return owner.loadedAt.Add(time.Second)
		}
		return owner.loadedAt.Add(-time.Second)
	}
	if _, err := owner.Descriptor(rootContext(t)); err == nil {
		t.Fatal("rollback during recheck returned candidate")
	}
}

func TestAgentPurposeRejectsForeignParentBeforeOpening(t *testing.T) {
	for _, fault := range []string{"nil-parent", "copied-guest", "foreign-parent", "legacy-guest", "closed-guest", "wrong-anchor", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			p, g, root, k := agentPurposeFixture(t)
			ctx := rootContext(t)
			switch fault {
			case "nil-parent":
				p = nil
			case "copied-guest":
				g = &GuestConfiguration{self: g}
			case "foreign-parent":
				p = &Prepared{loaded: p.loaded, scope: p.scope}
			case "legacy-guest":
				g.runtimeGuest = false
			case "closed-guest":
				if err := g.Close(ctx); err != nil {
					t.Fatal(err)
				}
			case "wrong-anchor":
				root.anchor.RecordPath = `C:\safe\other.json`
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			owner, err := p.loadAgentPurpose(ctx, g, root)
			if err == nil || owner != nil || len(k.opened) != 0 {
				t.Fatalf("invalid parent reached root: %v %v", owner, err)
			}
		})
	}
}

func TestAgentPurposeRequiresExactDigestAndBothPhysicalConfigurationFiles(t *testing.T) {
	for _, fault := range []string{"service-digest", "guest-digest", "json-id", "digest-id", "volume", "subject", "record-digest", "unsafe-acl"} {
		t.Run(fault, func(t *testing.T) {
			p, g, root, k := agentPurposeFixture(t)
			switch fault {
			case "service-digest", "guest-digest":
				var object map[string]any
				if err := json.Unmarshal(k.files[root.anchor.RecordPath].data, &object); err != nil {
					t.Fatal(err)
				}
				field := "serviceDigest"
				if fault == "guest-digest" {
					field = "runtimeGuestDigest"
				}
				object[field] = strings.Repeat("03", 32)
				data, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				setGuestFixtureData(k, root.anchor, data)
			case "json-id":
				k.files[root.anchor.RecordPath].state.id = [16]byte{90}
			case "digest-id":
				k.files[root.anchor.DigestPath].state.id = [16]byte{91}
			case "volume":
				k.files[root.anchor.RecordPath].state.volume = 9
			case "subject":
				k.identity.user = rootUserSID
			case "record-digest":
				k.files[root.anchor.DigestPath].data = []byte(strings.Repeat("00", 32) + "\n")
			case "unsafe-acl":
				k.files[root.anchor.RecordPath].state.acl = append(k.files[root.anchor.RecordPath].state.acl, protectedkey.ACE{SID: "S-1-1-0", Mask: 0x1f01ff})
			}
			owner, err := p.loadAgentPurpose(rootContext(t), g, root)
			if err == nil {
				t.Fatal("unsafe protected purpose accepted")
			}
			if owner != nil {
				if _, err := owner.Descriptor(rootContext(t)); err == nil {
					t.Fatal("partial owner produced descriptor")
				}
				_ = owner.Close(rootContext(t))
			}
		})
	}
}

func TestAgentPurposeDeadlineIsFixedAndQueriesAreDefensiveCopies(t *testing.T) {
	p, g, root, _ := agentPurposeFixture(t)
	owner, err := p.loadAgentPurpose(rootContext(t), g, root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(rootContext(t))
	now := owner.loadedAt
	owner.now = func() time.Time { return now }
	first, err := owner.Descriptor(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if first.HardDeadline.Sub(owner.loadedAt) != 5*time.Minute {
		t.Fatal("wrong candidate cap")
	}
	first.OperatorKeys[0] = "mutated"
	first.OperatorFingerprints[0] = "mutated"
	now = now.Add(time.Minute)
	second, err := owner.Descriptor(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if !second.HardDeadline.Equal(first.HardDeadline) || second.OperatorKeys[0] == "mutated" || second.OperatorFingerprints[0] == "mutated" {
		t.Fatal("candidate refreshed or aliased")
	}
	now = first.HardDeadline
	if _, err := owner.Descriptor(rootContext(t)); err == nil {
		t.Fatal("expired candidate survived")
	}
	now = owner.loadedAt
	if _, err := owner.Descriptor(rootContext(t)); err == nil {
		t.Fatal("expired candidate revived")
	}
}

func TestAgentPurposeChangedHeldIdentityAndParentFailureStayRejected(t *testing.T) {
	for _, fault := range []string{"file-id", "thread", "parent-close", "scope-error", "runtime-close"} {
		t.Run(fault, func(t *testing.T) {
			p, g, root, k := agentPurposeFixture(t)
			owner, err := p.loadAgentPurpose(rootContext(t), g, root)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close(rootContext(t))
			switch fault {
			case "file-id":
				k.files[root.anchor.RecordPath].state.id = [16]byte{99}
			case "thread":
				k.thread++
			case "parent-close":
				if err := p.Close(rootContext(t)); err != nil {
					t.Fatal(err)
				}
			case "scope-error":
				p.scope.(*previewMemoryScope).err = errors.New("fixture subject rejected")
			case "runtime-close":
				if err := g.Close(rootContext(t)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := owner.Descriptor(rootContext(t)); err == nil {
				t.Fatal("changed owner produced candidate")
			}
			k.thread = 7
			k.files[root.anchor.RecordPath].state.id = [16]byte{14}
			p.scope.(*previewMemoryScope).err = nil
			if _, err := owner.Descriptor(rootContext(t)); err == nil {
				t.Fatal("rejected owner revived")
			}
		})
	}
}

func TestAgentPurposeCloseAndCopyNeverRestoreDescriptor(t *testing.T) {
	p, g, root, k := agentPurposeFixture(t)
	owner, err := p.loadAgentPurpose(rootContext(t), g, root)
	if err != nil {
		t.Fatal(err)
	}
	copied := &AgentPurposeOwner{self: owner}
	if _, err := copied.Descriptor(rootContext(t)); err == nil {
		t.Fatal("copied capability accepted")
	}
	if err := copied.Close(rootContext(t)); err == nil {
		t.Fatal("copied capability closed original")
	}
	canceled, cancel := context.WithCancel(rootContext(t))
	cancel()
	if err := owner.Close(canceled); !errors.Is(err, ErrRootUnconfirmed) {
		t.Fatal(err)
	}
	if _, err := owner.Descriptor(rootContext(t)); err == nil {
		t.Fatal("failed close restored candidate")
	}
	if err := owner.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	for _, file := range k.files {
		if file.closes != 1 {
			t.Fatalf("purpose handle not exactly closed: %d", file.closes)
		}
	}
	if _, err := g.Digest(rootContext(t)); err != nil {
		t.Fatalf("child closed borrowed parent: %v", err)
	}
}

func TestAgentPurposePendingNativeReadRetainsPartialOwner(t *testing.T) {
	p, g, root, k := agentPurposeFixture(t)
	op := &rootFakeOp{done: false}
	k.files[root.anchor.RecordPath].op = op
	// Cancel only after the fake native read has started. A short wall-clock
	// deadline can expire during acquisition, when no pending operation exists.
	ctx, cancel := context.WithCancel(rootContext(t))
	defer cancel()
	k.files[root.anchor.RecordPath].onRead = cancel
	owner, err := p.loadAgentPurpose(ctx, g, root)
	if err == nil || owner == nil {
		t.Fatalf("pending read lost partial owner: %v %v", owner, err)
	}
	if k.files[root.anchor.RecordPath].reads != 1 || !op.cancelled {
		t.Fatal("fixture did not enter and cancel the pending read")
	}
	if _, err := owner.Descriptor(rootContext(t)); err == nil {
		t.Fatal("partial read approved candidate")
	}
	if err := owner.Close(rootContext(t)); err == nil {
		t.Fatal("unjoined read reported cleanup")
	}
	if k.files[root.anchor.RecordPath].closes != 0 || op.released {
		t.Fatal("unjoined native resource released")
	}
	op.done = true
	if err := owner.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if !op.released {
		t.Fatal("completed native operation not released")
	}
}

func TestAgentPurposeFixedSiblingCannotOverlapAndNativeEntryRefusesCanceled(t *testing.T) {
	p, g, _, _ := agentPurposeFixture(t)
	a, err := agentPurposeAnchor(p.loaded.anchor)
	if err != nil {
		t.Fatal(err)
	}
	if a.RecordPath != `C:\safe\agent-purpose-v1.json` || a.DigestPath != `C:\safe\agent-purpose-v1.sha256` {
		t.Fatal(a)
	}
	for _, path := range []string{a.RecordPath, a.DigestPath} {
		base := p.loaded.anchor
		base.RecordPath = path
		if _, err := agentPurposeAnchor(base); err == nil {
			t.Fatal("overlap accepted")
		}
	}
	ctx, cancel := context.WithCancel(rootContext(t))
	cancel()
	if owner, err := p.LoadAgentPurpose(ctx, g); err == nil || owner != nil {
		t.Fatal("canceled public entry opened native root")
	}
}

func TestAgentPurposeRejectsLayoutDigestInsteadOfRuntimeBundle(t *testing.T) {
	p, g, root, k := agentPurposeFixture(t)
	layout, err := g.Layout(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	digest := layout.Digest()
	var object map[string]any
	if err = json.Unmarshal(k.files[root.anchor.RecordPath].data, &object); err != nil {
		t.Fatal(err)
	}
	object["runtimeGuestDigest"] = hex.EncodeToString(digest[:])
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	setGuestFixtureData(k, root.anchor, data)
	owner, err := p.loadAgentPurpose(rootContext(t), g, root)
	if owner != nil {
		defer owner.Close(rootContext(t))
	}
	if err == nil {
		t.Fatal("layout-only digest substituted for protected runtime bundle")
	}
}

func TestAgentPurposeHashGraphPinsFileIdentityWithoutContentCycle(t *testing.T) {
	p, g, root, k := agentPurposeFixture(t)
	before, err := g.Digest(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.loadAgentPurpose(rootContext(t), g, root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := first.Descriptor(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	// Finite fixture changes contents in the same predeclared file IDs, before
	// loading a new owner. It does not mutate or refresh an accepted owner.
	var object map[string]any
	if err = json.Unmarshal(k.files[root.anchor.RecordPath].data, &object); err != nil {
		t.Fatal(err)
	}
	object["maximumDurationNs"] = "240000000000"
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	setGuestFixtureData(k, root.anchor, data)
	secondRoot, err := NewNativeRoot(root.anchor)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot.api = k
	second, err := p.loadAgentPurpose(rootContext(t), g, secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(rootContext(t))
	next, err := second.Descriptor(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	after, err := g.Digest(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if before != after || view.ConfigDigest == next.ConfigDigest {
		t.Fatal("file identity and content hashes conflated")
	}
	if next.HardDeadline.Sub(second.loadedAt) != 4*time.Minute {
		t.Fatal("new owner's candidate cap was not derived")
	}
	if _, err = first.Descriptor(rootContext(t)); err == nil {
		t.Fatal("closed owner revived")
	}
}

func TestAgentPurposeCancelBeforeReadHasNoPendingOperation(t *testing.T) {
	p, g, root, k := agentPurposeFixture(t)
	op := &rootFakeOp{done: false}
	file := k.files[root.anchor.RecordPath]
	file.op = op
	ctx, cancel := context.WithCancel(rootContext(t))
	defer cancel()
	k.onPrincipal = func(uintptr) { cancel() }
	owner, err := p.loadAgentPurpose(ctx, g, root)
	if err == nil || owner == nil {
		t.Fatalf("cancelled acquisition: %v %v", owner, err)
	}
	if file.reads != 0 || op.cancelled || op.released {
		t.Fatal("unstarted operation changed")
	}
	if err := owner.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Descriptor(rootContext(t)); err == nil {
		t.Fatal("cancelled acquisition approved candidate")
	}
}
