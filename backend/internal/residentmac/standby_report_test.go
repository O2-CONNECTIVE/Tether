package residentmac

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
)

type reportSent struct {
	lease  *agenttransport.ResidentSourceLease
	report agentgateway.ResidentStandby
}
type reportLog struct {
	mu   sync.Mutex
	sent []reportSent
}

func (l *reportLog) record(lease *agenttransport.ResidentSourceLease, r agentgateway.ResidentStandby) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, reportSent{lease, r})
}
func (l *reportLog) all() []reportSent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]reportSent(nil), l.sent...)
}

// holdLease stands in for the lease of an accepted control. The monitor only
// hands it to reportStandby, which the test replaces.
func holdLease(s *Standby) *agenttransport.ResidentSourceLease {
	s.mu.Lock()
	c := s.cycle
	s.mu.Unlock()
	lease := new(agenttransport.ResidentSourceLease)
	c.mu.Lock()
	c.lease = lease
	c.mu.Unlock()
	return lease
}

// The gateway tells the AI what the Mac's standby says (#412), so the Mac sends
// its own observation unchanged: the end it shows the person, by #419's rule
// (the earlier of elapsed time and the wall clock, never reopened). It sends
// only when that changes or the control is new.
func TestStandbyMonitorReportsTheObservedEndUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, _ := idleStandby(t)
		clock := &stepClock{base: time.Now()}
		s.clock = clock.now
		log := &reportLog{}
		s.reportStandby = log.record
		if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
			t.Fatal(r)
		}
		c.connected.Store(true)
		settle()
		if n := len(log.all()); n != 0 {
			t.Fatalf("reported %d times without a control to carry it", n)
		}
		first := holdLease(s)
		settle()
		settle()
		check := func(n int, lease *agenttransport.ResidentSourceLease, standby string, expired bool) agentgateway.ResidentStandby {
			t.Helper()
			sent := log.all()
			if len(sent) != n {
				t.Fatalf("reports = %d; want %d: %+v", len(sent), n, sent)
			}
			last := sent[n-1]
			o := s.Snapshot()
			if last.lease != lease || last.report.Standby != standby || !last.report.Until.Equal(o.StandbyUntil) || last.report.Expired != expired || last.report.Revision == 0 || last.report.Revision > o.Revision {
				t.Fatalf("report %+v does not copy %+v", last.report, o)
			}
			if n > 1 && last.report.Revision <= sent[n-2].report.Revision {
				t.Fatal("revision did not grow", sent)
			}
			return last.report
		}
		on := check(1, first, "on", false)
		if _, wall := clock.now(); !on.Until.Equal(wall.Add(2 * time.Hour)) {
			t.Fatal("end", on.Until)
		}
		extend(t, s, 2, 4*time.Hour)
		settle()
		if r := check(2, first, "on", false); !r.Until.Equal(on.Until.Add(2 * time.Hour)) {
			t.Fatal("extended end", r.Until)
		}
		// A new control learns the current end at once.
		second := holdLease(s)
		settle()
		check(3, second, "on", false)
		// After the wall clock is set back, standby ends by elapsed time first:
		// the end sent is the one the person now reads.
		clock.setBack(24 * time.Hour)
		settle()
		if _, wall := clock.now(); !check(4, second, "on", false).Until.Equal(wall.Add(4 * time.Hour)) {
			t.Fatal("set-back end")
		}
		// Hold the cycle while the last proof is pending, so the control is
		// still there when standby runs out.
		s.mu.Lock()
		cycle := s.cycle
		s.mu.Unlock()
		proved := make(chan struct{})
		cycle.mu.Lock()
		cycle.proved = proved
		cycle.mu.Unlock()
		clock.awake(4 * time.Hour)
		settle()
		if r := check(5, second, "stopping", true); !r.Until.IsZero() {
			t.Fatal("stopping standby sent an end", r.Until)
		}
		close(proved)
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
		if n := len(log.all()); n != 5 {
			t.Fatalf("reported %d times after standby left on and stopping", n)
		}
	})
}
