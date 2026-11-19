package residentpipe

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

var productSnapshotKeys = []string{"protocolVersion", "source", "state", "revision", "validForMs", "standby", "registration", "caller", "work", "cleanup", "canRequestOn", "canRequestOff", "transition"}

func statusSnapshot(t *testing.T, m *mapper, s statusEnvelope) map[string]json.RawMessage {
	t.Helper()
	line, err := Encode(m.frame(s))
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

// Only the foreground (development) dialect has an allow entry, so only its
// status line shows the allowance (#399): the wall-clock end the GUI shows as
// "until hh:mm", the warning from ten minutes before it, and that the last
// cycle ended because the allowance ran out. The JSON (product) line keeps its
// exact keys, so its consumers are unchanged.
func TestForegroundStatusCarriesTheRunAllowance(t *testing.T) {
	end := time.UnixMilli(1_900_000_000_000)
	on := fixtureOff()
	on.Revision, on.Standby, on.Registration, on.AdmissionSealed, on.CanRequestOn, on.CanRequestOff = 2, "on", "authenticated", false, false, true
	on.RunUntil, on.RunEndingSoon = end, true

	m := mapper{foreground: true}
	s, err := m.observe(on, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := statusSnapshot(t, &m, s)
	if len(got) != len(productSnapshotKeys)+3 || !exactKeys(got, append(append([]string{}, productSnapshotKeys...), "runUntil", "runEndingSoon", "runExpired")...) {
		t.Fatal("foreground status keys", got)
	}
	if string(got["runUntil"]) != "1900000000000" || string(got["runEndingSoon"]) != "true" || string(got["runExpired"]) != "false" || string(got["standby"]) != `"on"` {
		t.Fatal("foreground status lost the allowance", got)
	}

	// After an expiry the end is gone and the reason stays until the next ON.
	off := fixtureOff()
	off.Revision, off.RunExpired = 3, true
	if s, err = m.observe(off, 20); err != nil {
		t.Fatal(err)
	}
	got = statusSnapshot(t, &m, s)
	if string(got["runUntil"]) != "null" || string(got["runEndingSoon"]) != "false" || string(got["runExpired"]) != "true" || string(got["standby"]) != `"off"` {
		t.Fatal("foreground status lost the expiry", got)
	}

	var product mapper
	if s, err = product.observe(on, 10); err != nil {
		t.Fatal(err)
	}
	if got = statusSnapshot(t, &product, s); !exactKeys(got, productSnapshotKeys...) {
		t.Fatal("product status changed shape", got)
	}
}

// Unknown hides every claim, including when the allowance ends.
func TestPoisonedForegroundStatusShowsNoAllowanceEnd(t *testing.T) {
	o := fixtureOff()
	o.Revision, o.Standby, o.Registration, o.AdmissionSealed, o.CanRequestOn, o.CanRequestOff = 2, "on", "authenticated", false, false, true
	o.RunUntil = time.UnixMilli(1_900_000_000_000)
	o.Cleanup = "unknown"
	m := mapper{foreground: true}
	s, err := m.observe(o, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusSnapshot(t, &m, s); string(got["runUntil"]) != "null" || string(got["standby"]) != `"unknown"` {
		t.Fatal("unknown status still showed an allowance end", got)
	}
}

// The warning without an end, or an end the wire cannot carry, is not a
// valid owner observation.
func TestAllowanceObservationBounds(t *testing.T) {
	for name, change := range map[string]func(*residentmac.StandbyObservation){
		"warning without end": func(o *residentmac.StandbyObservation) { o.RunEndingSoon = true },
		"end before epoch":    func(o *residentmac.StandbyObservation) { o.RunUntil = time.UnixMilli(-1) },
		"end beyond integer":  func(o *residentmac.StandbyObservation) { o.RunUntil = time.UnixMilli(int64(MaxInteger) + 1) },
	} {
		o := fixtureOff()
		change(&o)
		var m mapper
		if _, err := m.observe(o, 10); err != ErrProtocol {
			t.Fatal(name, err)
		}
	}
	o := fixtureOff()
	o.RunUntil = time.UnixMilli(int64(MaxInteger))
	var m mapper
	if _, err := m.observe(o, 10); err != nil {
		t.Fatal("largest wire end refused", err)
	}
}
