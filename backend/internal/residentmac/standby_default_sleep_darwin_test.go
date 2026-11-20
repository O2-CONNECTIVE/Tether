//go:build darwin && cgo

package residentmac

import (
	"testing"
	"time"
)

func TestDefaultStandbyCanAdmitOwnedWorkWithSystemSleepPrevention(t *testing.T) {
	h := newSleepHarness(t, StandbyConfig{})
	s := newStandby()
	h.standby = s
	session := newSleepTestSession()
	workSleep := submitSleepWork(t, s, h.cycle, h.requests, session, 1, h.cycle.ctx, h.source)
	defer func() {
		if err := s.releaseOwnedWork(workSleep); err != nil {
			t.Errorf("release system sleep assertion: %v", err)
		}
	}()

	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("owned work did not start after acquiring system sleep assertion")
	}
	s.sleepMu.Lock()
	held := s.sleepHeld && s.sleepOwners == 1
	s.sleepMu.Unlock()
	if !held {
		t.Fatal("owned work started without holding its system sleep assertion")
	}

	close(session.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	s.collect(&sleepProofLease{}, sleepBinding(1), nil, workSleep)
	s.sleepMu.Lock()
	held = s.sleepHeld || s.sleepOwners != 0
	s.sleepMu.Unlock()
	if held {
		t.Fatal("confirmed collection did not release the system sleep assertion")
	}
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}
