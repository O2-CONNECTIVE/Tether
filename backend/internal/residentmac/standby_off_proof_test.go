package residentmac

import (
	"testing"
	"testing/synctest"
	"time"
)

func offWhileProofPending(t *testing.T) (*Standby, *idleConnector, chan struct{}) {
	t.Helper()
	s, c := standbyTurnedOn(t)
	proved := make(chan struct{})
	s.cycle.mu.Lock()
	s.cycle.proved = proved
	s.cycle.mu.Unlock()
	if r := s.RequestOff(2, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if o := s.Snapshot(); o.Standby != "stopping" || !o.AdmissionSealed {
		t.Fatal("OFF did not seal admission at once", o)
	}
	return s, c, proved
}

// The gateway refuses every later run while one run's cleanup is unknown, and
// the Mac's cleanup proof cannot travel on a closed control. Standby OFF during
// a run therefore keeps the control until that proof was sent.
func TestStandbyOffKeepsControlUntilCleanupProofWasSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, proved := offWhileProofPending(t)
		time.Sleep(standbyProofWait - time.Second)
		synctest.Wait()
		select {
		case <-c.done:
			t.Fatal("control closed before the cleanup proof was sent")
		default:
		}
		close(proved)
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}

// A proof that never comes must not keep the control open: OFF still ends the
// control in bounded time.
func TestStandbyOffEndsControlWhenCleanupProofNeverComes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, _ := offWhileProofPending(t)
		close(c.gate)
		time.Sleep(standbyProofWait)
		synctest.Wait()
		select {
		case <-c.done:
		default:
			t.Fatal("control stayed open without a cleanup proof")
		}
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}
