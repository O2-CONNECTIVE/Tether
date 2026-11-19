package residentpipe

import (
	"reflect"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

// The owner's observations around one ON, as the owner publishes them
// (TestAppliedOnOutlivesItsCycle in residentmac).
func onPending() residentmac.StandbyObservation {
	o := fixtureOff()
	o.Revision, o.Standby, o.Registration, o.AdmissionSealed = 2, "on", "connecting", false
	o.CanRequestOn, o.CanRequestOff = false, true
	o.StandbyUntil = time.UnixMilli(1_900_000_000_000)
	o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "pending"}
	return o
}
func onAuthenticated() residentmac.StandbyObservation {
	o := onPending()
	o.Revision, o.Registration, o.TransportGeneration = 3, "authenticated", "generation"
	o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
	return o
}

// Standby ran out: the owner stops it and keeps the ON's applied outcome.
func onStopping() residentmac.StandbyObservation {
	o := onAuthenticated()
	o.Revision, o.Standby, o.AdmissionSealed, o.Work, o.Cleanup = 4, "stopping", true, "stopping", "pending"
	o.CanRequestOff, o.StandbyUntil, o.StandbyExpired = false, time.Time{}, true
	return o
}
func onEnded() residentmac.StandbyObservation {
	o := onStopping()
	o.Revision, o.Standby, o.Registration, o.Work, o.Cleanup, o.CanRequestOn = 5, "off", "disconnected", "idle", "confirmed", true
	return o
}

// #420: the pipe looks at the owner when it changes, but can miss a step. If
// the Mac sleeps right after standby authenticated and wakes past its end, or
// the native source fails to open right after authentication, the pipe sees
// the ON pending and next sees its cycle over. Standby never comes back on for
// that request, so the ON can never be shown applied. The request must still
// end: Rust and the product GUI keep it pending, and refuse every later request,
// until a status with the same ID says applied or rejected.
func TestOnUnseenUntilItsCycleEndedIsRejected(t *testing.T) {
	rejected := &Transition{RequestID: 1, Intent: "on", Outcome: "rejected"}
	for name, seen := range map[string][]residentmac.StandbyObservation{
		"stopping then off": {onStopping(), onEnded()},
		"off only":          {onEnded()},
	} {
		t.Run(name, func(t *testing.T) {
			var m mapper
			now := uint64(100)
			observe := func(o residentmac.StandbyObservation) statusEnvelope {
				t.Helper()
				now += 100
				s, err := m.observe(o, now)
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			observe(fixtureOff())
			if s := observe(onPending()); s.Snapshot.Transition.Outcome != "pending" {
				t.Fatal(s)
			}
			var s statusEnvelope
			for _, o := range seen {
				s = observe(o)
				if !reflect.DeepEqual(s.Snapshot.Transition, rejected) || s.Snapshot.Standby != o.Standby || s.Snapshot.CanRequestOff {
					t.Fatal("an ON whose cycle ended stayed pending", s)
				}
			}
			if !s.Snapshot.CanRequestOn || m.pending != nil || m.poisoned {
				t.Fatal("standby off did not admit the next ON", s)
			}
			// The GUI polls the latest status, so it may have missed the first
			// rejection. Every later status keeps it until a newer request.
			if s = observe(onEnded()); !reflect.DeepEqual(s.Snapshot.Transition, rejected) || !s.Snapshot.CanRequestOn {
				t.Fatal("the heartbeat dropped the rejection", s)
			}
		})
	}
}

// The pipe never contradicts itself: an ON it showed applied is not closed
// again as rejected when its cycle ends.
func TestOnShownAppliedIsNotRejectedLater(t *testing.T) {
	var m mapper
	for i, o := range []residentmac.StandbyObservation{fixtureOff(), onPending(), onAuthenticated(), onStopping(), onEnded()} {
		s, err := m.observe(o, uint64(100*(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		if o.Standby == "on" && o.Registration == "authenticated" && s.Snapshot.Transition.Outcome != "applied" {
			t.Fatal("authenticated ON not shown applied", s)
		}
		if o.Standby != "on" && i > 0 && s.Snapshot.Transition != nil {
			t.Fatal("an ON shown applied got a second outcome", s)
		}
	}
	if !m.view.CanRequestOn || m.pending != nil || m.poisoned {
		t.Fatal(m.view)
	}
}

// While the owner still says standby is on, the ON may yet be shown applied:
// it stays pending, and nothing new is admitted.
func TestOnStaysPendingWhileItsCycleLasts(t *testing.T) {
	var m mapper
	_, _ = m.observe(fixtureOff(), 100)
	_, _ = m.observe(onPending(), 200)
	o := onPending()
	o.Revision, o.Transition = 3, &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
	s, err := m.observe(o, 300) // applied, but not yet authenticated as far as the pipe can show
	if err != nil || s.Snapshot.Transition.Outcome != "pending" || s.Snapshot.CanRequestOn || s.Snapshot.CanRequestOff || m.pending == nil {
		t.Fatal("an ON closed while its cycle lasts", s, err)
	}
}
