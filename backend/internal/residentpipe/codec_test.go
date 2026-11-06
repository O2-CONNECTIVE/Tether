package residentpipe

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"strings"
	"testing"
)

const goodIntent = `{"protocolVersion":1,"type":"intent","request":{"protocolVersion":1,"requestId":1,"expectedRevision":0,"intent":"on"}}` + "\n"

func TestStrictIntent(t *testing.T) {
	if _, e := Decode([]byte(goodIntent)); e != nil {
		t.Fatal(e)
	}
	bad := []string{
		strings.Replace(goodIntent, `"protocolVersion"`, `"ProtocolVersion"`, 1),
		strings.Replace(goodIntent, `"type"`, `"Type"`, 1),
		strings.Replace(goodIntent, `"expectedRevision"`, `"ExpectedRevision"`, 1),
		strings.Replace(goodIntent, `"requestId"`, `"RequestId"`, 1),
		strings.Replace(goodIntent, `"requestId":1`, `"requestId":1,"requestId":2`, 1),
		strings.Replace(goodIntent, `"requestId":1`, `"requestId":1.0`, 1),
		strings.Replace(goodIntent, `"requestId":1`, `"requestId":1e0`, 1),
		strings.Replace(goodIntent, `"requestId":1`, `"requestId":-0`, 1),
		strings.Replace(goodIntent, `"requestId":1`, `"requestId":9007199254740992`, 1),
		strings.Replace(goodIntent, `"requestId":1`, `"requestId":null`, 1),
		strings.Replace(goodIntent, `"expectedRevision":0,`, "", 1),
		strings.Replace(goodIntent, `"expectedRevision":0`, `"expectedRevision":null`, 1),
		strings.Replace(goodIntent, `"intent":"on"`, `"intent":"on","path":"/tmp/x"`, 1),
		goodIntent + "{}", strings.TrimSuffix(goodIntent, "\n"), strings.TrimSuffix(goodIntent, "\n") + "\r\n",
	}
	for _, b := range bad {
		if _, e := Decode([]byte(b)); e == nil {
			t.Errorf("accepted %s", b)
		}
	}
	exact := strings.TrimSuffix(goodIntent, "\n") + strings.Repeat(" ", MaxLine-len(goodIntent)) + "\n"
	if len(exact) != MaxLine {
		t.Fatal("fixture")
	}
	if _, e := Decode([]byte(exact)); e != nil {
		t.Fatal(e)
	}
	if _, e := Decode([]byte(" " + exact)); e == nil {
		t.Fatal("accepted 4097")
	}
}
func fixtureOff() residentmac.StandbyObservation {
	return residentmac.StandbyObservation{Revision: 1, Standby: "off", Registration: "disconnected", Work: "idle", Cleanup: "confirmed", AdmissionSealed: true, CanRequestOn: true}
}
func TestMappingHeartbeatAndNativeProof(t *testing.T) {
	var m mapper
	o := fixtureOff()
	first, e := m.observe(o, 100)
	if e != nil || !first.Snapshot.CanRequestOn {
		t.Fatal(first, e)
	}
	second, e := m.observe(o, 1100)
	if e != nil || second.Snapshot.Revision != 2 || m.raw.Revision != 1 {
		t.Fatal(second, e)
	}
	o.Revision++
	o.Standby = "on"
	o.AdmissionSealed = false
	o.Registration = "connecting"
	o.CanRequestOn = false
	o.CanRequestOff = true
	o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
	m.pending = &Intent{RequestID: 1, Intent: "on"}
	s, e := m.observe(o, 1200)
	if e != nil || s.Snapshot.Standby != "unknown" || s.Snapshot.Transition.Outcome != "pending" || s.Snapshot.CanRequestOff {
		t.Fatal(s, e)
	}
	o.Revision++
	o.Registration = "authenticated"
	s, e = m.observe(o, 1300)
	if e != nil || s.Snapshot.Standby != "on" || s.Snapshot.Transition.Outcome != "applied" {
		t.Fatal(s, e)
	}
	o.Revision++
	o.Standby = "off"
	o.AdmissionSealed = true
	o.Owned = 1
	o.Transition = &residentmac.StandbyTransition{RequestID: 2, Intent: "off", Outcome: "applied"}
	m.pending = &Intent{RequestID: 2, Intent: "off"}
	s, e = m.observe(o, 1400)
	if e != nil || s.Snapshot.Standby != "unknown" || s.Snapshot.Transition.Outcome != "pending" {
		t.Fatal(s, e)
	}
}
func TestMappingPoisonAndRegression(t *testing.T) {
	var m mapper
	o := fixtureOff()
	_, _ = m.observe(o, 10)
	o.Work = "running"
	if _, e := m.observe(o, 20); e == nil {
		t.Fatal("same revision mutated")
	}
	o = fixtureOff()
	o.Revision = 2
	o.Cleanup = "unknown"
	_, _ = m.observe(o, 20)
	o.Revision++
	o.Cleanup = "confirmed"
	s, e := m.observe(o, 30)
	if e != nil || s.Snapshot.CanRequestOn || s.Snapshot.Cleanup != "unknown" {
		t.Fatal(s, e)
	}
	if _, e = m.observe(o, 29); e == nil {
		t.Fatal("backwards clock")
	}
}

func TestObservedPendingAndHighWater(t *testing.T) {
	var m mapper
	o := fixtureOff()
	o.Transition = &residentmac.StandbyTransition{RequestID: 50, Intent: "on", Outcome: "pending"}
	s, e := m.observe(o, 10)
	if e != nil || s.Snapshot.CanRequestOn || m.pending == nil || m.highID != 50 {
		t.Fatal(s, e)
	}
	o.Revision++
	o.Transition = nil
	s, e = m.observe(o, 20)
	if e != nil || s.Snapshot.CanRequestOn || m.pending == nil {
		t.Fatal(s, e)
	}
	o.Revision++
	o.Transition = &residentmac.StandbyTransition{RequestID: 50, Intent: "on", Outcome: "rejected"}
	_, e = m.observe(o, 30)
	if e != nil || m.pending != nil {
		t.Fatal(e)
	}
	r := m.submit(nil, Intent{RequestID: 50, Intent: "on"}, 30)
	if r.Disposition != "stale" {
		t.Fatal(r)
	}
}
func TestPendingMismatchPoisons(t *testing.T) {
	var m mapper
	o := fixtureOff()
	o.Transition = &residentmac.StandbyTransition{RequestID: 50, Intent: "on", Outcome: "pending"}
	_, _ = m.observe(o, 10)
	o.Revision++
	o.Transition = &residentmac.StandbyTransition{RequestID: 51, Intent: "on", Outcome: "rejected"}
	s, e := m.observe(o, 20)
	if e != nil || !m.poisoned || m.pending == nil || s.Snapshot.CanRequestOn {
		t.Fatal(s, e)
	}
}

func TestUnknownCleanupCannotApplyLater(t *testing.T) {
	var m mapper
	o := fixtureOff()
	o.Cleanup = "unknown"
	o.Transition = &residentmac.StandbyTransition{RequestID: 4, Intent: "off", Outcome: "unknown"}
	_, _ = m.observe(o, 10)
	o.Revision++
	o.Cleanup = "confirmed"
	o.Transition.Outcome = "applied"
	s, e := m.observe(o, 20)
	if e != nil || s.Snapshot.Cleanup != "unknown" || s.Snapshot.Transition.Outcome != "unknown" || m.pending == nil {
		t.Fatal(s, e)
	}
}

func TestSecondPendingIntentRejectedWithoutPoison(t *testing.T) {
	m := mapper{pending: &Intent{RequestID: 1, Intent: "on"}, highID: 1}
	r := m.submit(nil, Intent{RequestID: 2, Intent: "off"}, 10)
	if r.Disposition != "rejected" || m.poisoned {
		t.Fatal(r)
	}
}

func TestMissedAuthenticationEventRetainedAcrossReconnect(t *testing.T) {
	for _, generation := range []string{"", "actual-retained-generation"} {
		var m mapper
		m.pending = &Intent{RequestID: 1, Intent: "on"}
		o := fixtureOff()
		o.Standby = "on"
		o.AdmissionSealed = false
		o.Registration = "connecting"
		o.CanRequestOn = false
		o.CanRequestOff = true
		o.TransportGeneration = generation
		o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
		s, e := m.observe(o, 10)
		if e != nil {
			t.Fatal(e)
		}
		if s.Snapshot.Registration != "connecting" {
			t.Fatal("invented current authentication")
		}
		if generation == "" {
			if s.Snapshot.Standby != "unknown" || s.Snapshot.Transition.Outcome != "pending" || m.pending == nil {
				t.Fatal(s)
			}
		} else {
			if s.Snapshot.Standby != "on" || s.Snapshot.Transition.Outcome != "applied" || m.pending != nil || !s.Snapshot.CanRequestOff {
				t.Fatal(s)
			}
		}
	}
}
func TestUnknownRegistrationCannotUseHistoricalEvent(t *testing.T) {
	var m mapper
	m.pending = &Intent{RequestID: 1, Intent: "on"}
	o := fixtureOff()
	o.Standby = "on"
	o.AdmissionSealed = false
	o.Registration = "unknown"
	o.TransportGeneration = "retained"
	o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
	s, e := m.observe(o, 10)
	if e != nil || s.Snapshot.Standby != "unknown" || s.Snapshot.Transition.Outcome == "applied" || m.pending == nil {
		t.Fatal(s, e)
	}
}
