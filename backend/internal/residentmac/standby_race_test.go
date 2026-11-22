package residentmac

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
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

func TestNeedsActionCannotRestartBeforeOriginalCycleFinishes(t *testing.T) {
	for _, end := range []string{"off", "expiry"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newStandby()
				s.makeConnector = func(context.Context) (residentConnector, error) {
					return terminalRefusalConnector(), nil
				}
				clock := &stepClock{base: time.Now()}
				s.clock = clock.now
				finishing, resume := make(chan struct{}), make(chan struct{})
				reachedFinish := sync.OnceFunc(func() { close(finishing) })
				release := sync.OnceFunc(func() { close(resume) })
				defer func() {
					release()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := s.Close(ctx); err != nil {
						t.Error("close standby:", err)
					}
				}()

				if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
					t.Fatal(r)
				}
				settle()
				before := s.Snapshot()
				if before.Registration != "needs_action" {
					t.Fatal("standby did not reach needs action", before)
				}
				expired := end == "expiry"
				var cancels atomic.Int32
				s.mu.Lock()
				original := s.cycle
				cancelExpiry := original.expiryCancel
				original.expiryCancel = func() {
					// Explicit OFF cancels the monitor first. The run's own
					// cancellation, outside s.mu, precedes its final join.
					firstOffCancel := !expired && cancels.Add(1) == 1
					cancelExpiry()
					if firstOffCancel {
						return
					}
					reachedFinish()
					<-resume
				}
				s.mu.Unlock()
				transition := StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
				if expired {
					clock.awake(2 * time.Hour)
				} else {
					if r := s.RequestOff(2, before.Revision); r.Disposition != "accepted" {
						t.Fatal("OFF was not accepted", r)
					}
					transition = StandbyTransition{RequestID: 2, Intent: "off", Outcome: "applied"}
				}
				settle()
				select {
				case <-finishing:
				default:
					t.Fatal("old cycle did not reach its final join")
				}
				off := s.Snapshot()
				if off.Standby != "off" || off.Cleanup != "confirmed" || off.StandbyExpired != expired || off.Transition == nil || *off.Transition != transition {
					t.Fatal("the original cycle did not publish its final state", off)
				}
				if r := s.RequestOn(3, off.Revision, 2*time.Hour); r.Disposition != "rejected" || off.CanRequestOn {
					t.Fatal("next ON was available before the original cycle finished", off, r)
				}

				release()
				<-original.done
				ready := s.Snapshot()
				if !ready.CanRequestOn || ready.Revision <= off.Revision || ready.Transition == nil || *ready.Transition != transition {
					t.Fatal("completion did not publish readiness without changing the transition", ready)
				}
				if r := s.RequestOn(4, ready.Revision, 2*time.Hour); r.Disposition != "accepted" {
					t.Fatal("next ON after completion was not accepted", r)
				}
			})
		})
	}
}
