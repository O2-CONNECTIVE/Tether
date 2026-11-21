package residentpipe

import (
	"encoding/json"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

func TestTerminalUnknownStatusProjectsLastObservation(t *testing.T) {
	var m mapper
	caller := "operator"
	o := fixtureOff()
	o.Standby = "on"
	o.Registration = "needs_action"
	o.RegistrationReason = residentmac.RegistrationReasonIdentityRefused
	o.Caller = &caller
	o.CanRequestOn = false
	o.CanRequestOff = true
	o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "applied"}
	initial, err := m.observe(o, 100)
	if err != nil {
		t.Fatal(err)
	}

	terminal, err := m.terminalUnknownStatus(200)
	if err != nil {
		t.Fatal(err)
	}
	s := terminal.Snapshot
	if s.Revision != initial.Snapshot.Revision+1 || s.Standby != "unknown" || s.Registration != "unknown" || s.RegistrationReason != "" || s.Work != "unknown" || s.Cleanup != "unknown" || s.CanRequestOn || s.CanRequestOff {
		t.Fatal("terminal cleanup was not projected as unknown", s)
	}
	if s.Caller == nil || *s.Caller != caller || s.Transition == nil || s.Transition.Outcome != "unknown" {
		t.Fatal("last validated caller or transition was not preserved safely", s)
	}
	b, err := Encode(m.frame(terminal))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Snapshot map[string]json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope.Snapshot["registrationReason"]; ok {
		t.Fatal("terminal unknown registration retained a reason", string(b))
	}
}
