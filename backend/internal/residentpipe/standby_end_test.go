package residentpipe

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

func statusSnapshot(t *testing.T, s statusEnvelope) map[string]json.RawMessage {
	t.Helper()
	line, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	var snapshot map[string]json.RawMessage
	if json.Unmarshal(line, &root) != nil || json.Unmarshal(root["snapshot"], &snapshot) != nil {
		t.Fatalf("%s", line)
	}
	return snapshot
}

// Both dialects show when standby ends (#399, #451): the wall-clock end the
// GUI shows as "until hh:mm", the warning from ten minutes before it, and that
// the last cycle ended because standby ran out. The host GUI reads the JSON
// (product) line, so it carries the same three keys as the foreground one.
func TestStatusCarriesTheStandbyEnd(t *testing.T) {
	end := time.UnixMilli(1_900_000_000_000)
	on := fixtureOff()
	on.Revision, on.Standby, on.Registration, on.AdmissionSealed, on.CanRequestOn, on.CanRequestOff = 2, "on", "authenticated", false, false, true
	on.StandbyUntil, on.StandbyEndingSoon = end, true

	var m mapper
	s, err := m.observe(on, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := statusSnapshot(t, s)
	if !exactKeys(got, "protocolVersion", "source", "state", "revision", "validForMs", "standby", "registration", "caller",
		"work", "cleanup", "canRequestOn", "canRequestOff", "transition", "standbyUntil", "standbyEndingSoon", "standbyExpired", "sleepPrevention") {
		t.Fatal("status keys", got)
	}
	if string(got["standbyUntil"]) != "1900000000000" || string(got["standbyEndingSoon"]) != "true" || string(got["standbyExpired"]) != "false" || string(got["standby"]) != `"on"` {
		t.Fatal("status lost the standby end", got)
	}

	// After an expiry the end is gone and the reason stays until the next ON.
	off := fixtureOff()
	off.Revision, off.StandbyExpired = 3, true
	if s, err = m.observe(off, 20); err != nil {
		t.Fatal(err)
	}
	got = statusSnapshot(t, s)
	if string(got["standbyUntil"]) != "null" || string(got["standbyEndingSoon"]) != "false" || string(got["standbyExpired"]) != "true" || string(got["standby"]) != `"off"` {
		t.Fatal("status lost the expiry", got)
	}
}

// Unknown hides when standby ends, a claim about admission. Why it stopped
// was settled before its cleanup went unknown, so the status keeps it.
func TestPoisonedStatusShowsNoStandbyEnd(t *testing.T) {
	o := fixtureOff()
	o.Revision, o.Standby, o.Work, o.Cleanup, o.StandbyExpired = 2, "stopping", "stopping", "pending", true
	var m mapper
	s, err := m.observe(o, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusSnapshot(t, s); string(got["standbyExpired"]) != "true" {
		t.Fatal("stopping status lost why standby stopped", got)
	}
	o.Revision, o.Standby, o.Work, o.Cleanup = 3, "unknown", "unknown", "unknown"
	o.StandbyUntil, o.StandbyEndingSoon = time.UnixMilli(1_900_000_000_000), true
	if s, err = m.observe(o, 20); err != nil {
		t.Fatal(err)
	}
	if got := statusSnapshot(t, s); string(got["standbyUntil"]) != "null" || string(got["standbyEndingSoon"]) != "false" || string(got["standbyExpired"]) != "true" || string(got["standby"]) != `"unknown"` {
		t.Fatal("unknown status showed a standby end or lost why standby stopped", got)
	}
}

// The warning without an end, or an end the wire cannot carry, is not a
// valid owner observation.
func TestStandbyEndObservationBounds(t *testing.T) {
	for name, change := range map[string]func(*residentmac.StandbyObservation){
		"warning without end": func(o *residentmac.StandbyObservation) { o.StandbyEndingSoon = true },
		"end before epoch":    func(o *residentmac.StandbyObservation) { o.StandbyUntil = time.UnixMilli(-1) },
		"end beyond integer":  func(o *residentmac.StandbyObservation) { o.StandbyUntil = time.UnixMilli(int64(MaxInteger) + 1) },
	} {
		o := fixtureOff()
		change(&o)
		var m mapper
		if _, err := m.observe(o, 10); err != ErrProtocol {
			t.Fatal(name, err)
		}
	}
	o := fixtureOff()
	o.StandbyUntil = time.UnixMilli(int64(MaxInteger))
	var m mapper
	if _, err := m.observe(o, 10); err != nil {
		t.Fatal("largest wire end refused", err)
	}
}
