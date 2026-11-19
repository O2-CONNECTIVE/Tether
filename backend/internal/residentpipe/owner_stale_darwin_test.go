//go:build darwin && cgo

package residentpipe

import (
	"reflect"
	"testing"
	"time"
)

// #420: the owner can find a request stale after the mapper's own check
// passed, when the owner changed in between (standby ran out, or work changed
// while standby was on). Rust keeps an admitted request pending, and refuses
// every later one, until a status with the same ID says applied or rejected.
// The owner refuses a stale request before it changes anything
// (TestStaleOffAfterStandbyRanOutChangesNothing in residentmac), so this
// refusal is as final as the mapper's own and earns the same evidence.
//
// The idle owner here finds the request stale because it has already used a
// larger ID; the residentmac test reproduces the race itself. No ON is started.
func TestOwnerStaleRefusalHasCorrelatedRejection(t *testing.T) {
	owner := idleProtectedOwner(t)
	if r := owner.RequestOff(9, owner.Snapshot().Revision); r.Disposition != "rejected" {
		t.Fatal(r)
	}
	var m mapper
	s, err := m.observe(owner.Snapshot(), 100)
	if err != nil || !s.Snapshot.CanRequestOn {
		t.Fatal(s, err)
	}
	r := m.submit(owner, Intent{RequestID: 5, Intent: "on", ExpectedRevision: s.Snapshot.Revision, StandbyFor: 2 * time.Hour}, 100)
	if r.Disposition != "stale" || m.pending != nil || m.poisoned {
		t.Fatal(r, m.pending, m.poisoned)
	}
	// serveLoop publishes this status before the stale receipt.
	s, err = m.observe(owner.Snapshot(), 100)
	want := &Transition{RequestID: 5, Intent: "on", Outcome: "rejected"}
	if err != nil || !reflect.DeepEqual(s.Snapshot.Transition, want) || !s.Snapshot.CanRequestOn {
		t.Fatal("owner stale refusal left no matching rejection", s, err)
	}
	if owner.Snapshot().Standby != "off" {
		t.Fatal("stale ON changed the owner")
	}
}

// An allowance or an extension is not a standby transition. Reporting the
// owner's stale refusal of one as a transition would show an ON/OFF change
// that never happened (TestStaleLocalActionLeavesNoTransitionEvidence).
func TestOwnerStaleLocalActionLeavesNoTransitionEvidence(t *testing.T) {
	owner := idleProtectedOwner(t)
	if r := owner.RequestOff(9, owner.Snapshot().Revision); r.Disposition != "rejected" {
		t.Fatal(r)
	}
	for i, intent := range []string{"allow-run", "deny-run", "extend"} {
		var m mapper
		s, err := m.observe(owner.Snapshot(), 100)
		if err != nil {
			t.Fatal(err)
		}
		r := m.submit(owner, Intent{RequestID: uint64(5 + i), Intent: intent, ExpectedRevision: s.Snapshot.Revision, StandbyFor: 2 * time.Hour}, 100)
		if r.Disposition != "stale" || m.refused != nil || m.pending != nil || m.poisoned {
			t.Fatalf("%s: %+v refused=%v", intent, r, m.refused)
		}
	}
}
