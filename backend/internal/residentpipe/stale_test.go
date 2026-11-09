package residentpipe

import (
	"reflect"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

// Pure mapper boundary: no loaded/native owner, command, authentication or I/O.
func TestPreOwnerStaleHasCorrelatedRejection(t *testing.T) {
	var m mapper
	o := fixtureOff()
	o.Standby, o.Registration, o.AdmissionSealed = "on", "authenticated", false
	o.CanRequestOn, o.CanRequestOff = false, true
	o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
	_, _ = m.observe(o, 100)
	_, _ = m.observe(o, 200) // heartbeat overtakes an already admitted OFF
	r := m.submit(nil, Intent{RequestID: 2, Intent: "off", ExpectedRevision: 1}, 200)
	if r.Disposition != "stale" {
		t.Fatal(r)
	}
	s, err := m.observe(o, 200)
	want := &Transition{RequestID: 2, Intent: "off", Outcome: "rejected"}
	if err != nil || s.Snapshot.Revision != 3 || !reflect.DeepEqual(s.Snapshot.Transition, want) || !s.Snapshot.CanRequestOff {
		t.Fatal("missing pre-owner refusal evidence", s, err)
	}
	if !reflect.DeepEqual(m.raw, o) || m.pending != nil || m.poisoned {
		t.Fatal("owner evidence changed")
	}
	// Duplicate replay must never manufacture another completion.
	r = m.submit(nil, Intent{RequestID: 2, Intent: "off", ExpectedRevision: 3}, 200)
	s, err = m.observe(o, 200)
	if r.Disposition != "stale" || err != nil || s.Snapshot.Transition.RequestID != 1 {
		t.Fatal(r, s, err)
	}
}

func TestStaleRefusalCannotReplaceUnresolvedOrNewerOwnerEvidence(t *testing.T) {
	for _, mode := range []string{"pending", "unknown", "cleanup", "newer"} {
		t.Run(mode, func(t *testing.T) {
			var m mapper
			o := fixtureOff()
			_, _ = m.observe(o, 100)
			r := m.submit(nil, Intent{RequestID: 2, Intent: "on", ExpectedRevision: 0}, 100)
			if r.Disposition != "stale" {
				t.Fatal(r)
			}
			o.Revision++
			o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: mode}
			if mode == "cleanup" {
				o.Cleanup = "unknown"
				o.Transition.Outcome = "rejected"
			}
			if mode == "newer" {
				o.Transition.RequestID = 3
				o.Transition.Outcome = "rejected"
			}
			s, err := m.observe(o, 100)
			if err != nil || s.Snapshot.Transition.RequestID == 2 {
				t.Fatal("refusal replaced owner evidence", s, err)
			}
			if mode != "newer" && (s.Snapshot.CanRequestOn || s.Snapshot.CanRequestOff) {
				t.Fatal("unsafe admission", s)
			}
		})
	}
}

func TestExistingPendingOrUnknownDoesNotBecomeRefusal(t *testing.T) {
	for _, outcome := range []string{"pending", "unknown"} {
		var m mapper
		o := fixtureOff()
		o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: outcome}
		_, _ = m.observe(o, 100)
		r := m.submit(nil, Intent{RequestID: 2, Intent: "off", ExpectedRevision: 0}, 100)
		s, err := m.observe(o, 100)
		want := "rejected"
		if outcome == "unknown" {
			want = "unknown"
		}
		if r.Disposition != want || err != nil || s.Snapshot.Transition.RequestID != 1 || m.pending == nil {
			t.Fatal(r, s, err)
		}
	}
}
