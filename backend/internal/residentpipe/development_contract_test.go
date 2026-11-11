package residentpipe

import (
	"bytes"
	"os"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

// This public golden is consumed unchanged by Swift DevelopmentChecks. It is
// produced by the real mapper and Encode, not a handwritten consumer snapshot.
// No transport, credentials, host, or command execution is involved.
func TestDevelopmentPendingContractGolden(t *testing.T) {
	var m mapper
	var lines []byte
	appendFrame := func(v any) {
		b, err := Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, b...)
	}
	observe := func(o residentmac.StandbyObservation) {
		s, err := m.observe(o, 1000)
		if err != nil {
			t.Fatal(err)
		}
		appendFrame(s)
	}
	o := fixtureOff()
	observe(o)
	appendFrame(receiptEnvelope{1, "receipt", Receipt{1, 1, "on", "accepted"}})
	o.Revision++
	o.Standby = "on"
	o.Registration = "connecting"
	o.AdmissionSealed = false
	o.CanRequestOn = false
	o.CanRequestOff = true
	o.Transition = &residentmac.StandbyTransition{RequestID: 1, Intent: "on", Outcome: "pending"}
	observe(o)
	observe(o) // Heartbeat republishes the same owner observation with a fresh view revision.
	o.Revision++
	o.Registration = "authenticated"
	o.Transition.Outcome = "applied"
	observe(o)
	appendFrame(receiptEnvelope{1, "receipt", Receipt{1, 2, "off", "accepted"}})
	o = fixtureOff()
	o.Revision = 4
	o.Transition = &residentmac.StandbyTransition{RequestID: 2, Intent: "off", Outcome: "applied"}
	observe(o)
	golden, err := os.ReadFile("../../../ui/mac-development/Tests/DevelopmentChecks/Fixtures/mapper-pending.jsonl")
	if err != nil || !bytes.Equal(golden, lines) {
		t.Fatalf("public mapper golden mismatch: %v\nGOLDEN_BEGIN\n%sGOLDEN_END", err, lines)
	}
}
