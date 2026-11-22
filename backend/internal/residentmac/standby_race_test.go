package residentmac

import (
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

// #420: the resident pipe looks at standby when it changes, but can miss a
// step. If the Mac sleeps right after standby authenticated and wakes past its
// end, the pipe may see the ON pending and next see standby stopping. The owner
// keeps the ON's applied outcome to the end, and standby never comes back on for
// that request; closing the request is the pipe's job
// (TestOnUnseenUntilItsCycleEndedIsRejected in residentpipe).
func TestAppliedOnOutlivesItsCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t, 2*time.Hour)
		applied := StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
		if s.StandbyExpired() {
			t.Fatal("new standby cycle already marked expired")
		}
		clock.asleep(2 * time.Hour)
		settle()
		if o := s.Snapshot(); o.Standby != "stopping" || !o.StandbyExpired || o.Transition == nil || *o.Transition != applied {
			t.Fatal("the end rewrote the ON or did not stop standby", o)
		}
		if !s.StandbyExpired() {
			t.Fatal("expiry was not available before cleanup")
		}
		close(c.gate)
		eventually(t, func() bool {
			o := s.Snapshot()
			return o.Standby == "off" && o.Cleanup == "confirmed" && o.Transition != nil && *o.Transition == applied
		})
	})
}

// #420: the pipe checks a request against the owner's snapshot, then sends it.
// Standby can run out between the two, and the owner then finds the OFF stale.
// The pipe reports that refusal as final, a rejected transition with the OFF's
// own ID (TestOwnerStaleRefusalHasCorrelatedRejection in residentpipe). That
// holds only because a stale request changes nothing.
func TestStaleOffAfterStandbyRanOutChangesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t, 2*time.Hour)
		checked := s.Snapshot()
		clock.asleep(2 * time.Hour)
		settle()
		before := s.Snapshot()
		if before.Revision == checked.Revision || before.Standby != "stopping" {
			t.Fatal("standby did not run out between the check and the request", before)
		}
		if r := s.RequestOff(2, checked.Revision); r.Disposition != "stale" {
			t.Fatal(r)
		}
		if after := s.Snapshot(); !reflect.DeepEqual(after, before) {
			t.Fatal("a stale OFF changed standby", before, after)
		}
		close(c.gate)
		eventually(t, func() bool { o := s.Snapshot(); return o.Standby == "off" && o.Transition.Intent == "on" })
	})
}
