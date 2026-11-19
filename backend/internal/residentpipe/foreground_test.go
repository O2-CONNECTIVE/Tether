package residentpipe

import (
	"strings"
	"testing"
	"time"
)

func TestForegroundCommands(t *testing.T) {
	for _, tc := range []struct {
		line   string
		kind   controlKind
		intent Intent
	}{
		{"status\n", controlStatus, Intent{}},
		{"close\n", controlClose, Intent{}},
		// The person at the Mac picks how long standby stays on, and extends it,
		// in one of the listed lengths in hours (#399).
		{"on 1 0 2\n", controlIntent, Intent{1, 1, 0, "on", 2 * time.Hour}},
		{"on 1 0 4\n", controlIntent, Intent{1, 1, 0, "on", 4 * time.Hour}},
		{"on 1 0 8\n", controlIntent, Intent{1, 1, 0, "on", 8 * time.Hour}},
		{"on 1 0 12\n", controlIntent, Intent{1, 1, 0, "on", 12 * time.Hour}},
		{"on 1 0 24\n", controlIntent, Intent{1, 1, 0, "on", 24 * time.Hour}},
		{"extend 5 7 2\n", controlIntent, Intent{1, 5, 7, "extend", 2 * time.Hour}},
		{"extend 5 7 24\n", controlIntent, Intent{1, 5, 7, "extend", 24 * time.Hour}},
		{"off 9007199254740991 9007199254740991\n", controlIntent, Intent{1, MaxInteger, MaxInteger, "off", 0}},
		// The command-run allowance has no length of its own: it lasts until
		// standby turns off.
		{"allow 3 7\n", controlIntent, Intent{1, 3, 7, "allow-run", 0}},
		{"deny 4 7\n", controlIntent, Intent{1, 4, 7, "deny-run", 0}},
	} {
		got, err := decodeControl([]byte(tc.line), foregroundInput)
		if err != nil || got.kind != tc.kind || got.intent != tc.intent {
			t.Fatalf("%q: %#v %v", tc.line, got, err)
		}
	}
}

func TestForegroundIntentKeepsMapperUnknown(t *testing.T) {
	m := mapper{poisoned: true}
	for _, line := range []string{"on 1 1 2\n", "off 2 1\n", "allow 3 1\n", "deny 4 1\n", "extend 5 1 2\n"} {
		command, err := decodeControl([]byte(line), foregroundInput)
		if err != nil {
			t.Fatal(err)
		}
		// nil cannot supply authority. Poisoned state must refuse before any
		// owner method, retaining the same mapper instead of creating a new one.
		r := m.submit(nil, command.intent, 10)
		if r.Disposition != "unknown" || !m.poisoned {
			t.Fatal("CLI input cleared unknown", r)
		}
	}
}

func TestForegroundCommandsRejectAmbiguity(t *testing.T) {
	for _, line := range []string{
		"", "status", "status\r\n", " status\n", "status \n", "STATUS\n", "status 1\n",
		"on\n", "on 1\n", "on 0 1 2\n", "on 01 1 2\n", "on +1 1 2\n", "on 1 -1 2\n",
		"on 1 01 2\n", "on 1 1.0 2\n", "on 1 1e2 2\n", "on\t1 1 2\n", "on  1 1 2\n",
		"on 9007199254740992 1 2\n", "on 1 9007199254740992 2\n", "close 1\n",
		"extend\n", "extend 1\n", "extend 0 1 2\n", "EXTEND 1 1 2\n", "ON 1 1 2\n",
		"allow\n", "allow 1\n", "allow 0 1\n", "allow-run 1 1\n", "deny-run 1 1\n", "deny 1\n", "ALLOW 1 1\n",
		// No standby without a length, and no free length: only the listed
		// hours, spelled one way. A huge number must not wrap around into a
		// listed length either.
		"on 1 1\n", "extend 1 1\n",
		"on 1 1 900\n", "on 1 1 0\n", "on 1 1 1\n", "on 1 1 3\n", "on 1 1 25\n", "on 1 1 48\n",
		"on 1 1 02\n", "on 1 1 +2\n", "on 1 1 2.0\n", "on 1 1 2h\n", "on 1 1 -2\n", "on 1 1  2\n", "on 1 1 2 2\n",
		"on 1 1 2251799813685250\n", "on 1 1 9007199254740991\n",
		"extend 1 1 0\n", "extend 1 1 3\n", "extend 1 1 02\n", "extend 1 1 2h\n", "extend 1 1 2251799813685250\n",
		// The allowance has no length to name, and neither has anything else.
		"allow 1 1 2\n", "deny 1 1 2\n", "off 1 1 2\n",
		"start\n", "cancel 1\n", "policy /tmp/policy\n", "on 1 1 2\nclose\n",
		"status\x00\n", "\xff\n", strings.Repeat("x", MaxLine) + "\n", goodIntent,
	} {
		if _, err := decodeControl([]byte(line), foregroundInput); err == nil {
			t.Fatalf("accepted ambiguous foreground input %q", line)
		}
	}
}

func TestControlDialectsRemainSeparate(t *testing.T) {
	// The product's JSON on cannot name a length until #410; it gets the
	// default two hours, so product standby has an end too (#399).
	got, err := decodeControl([]byte(goodIntent), jsonInput)
	if err != nil || got.kind != controlIntent || got.intent.RequestID != 1 || got.intent.Intent != "on" || got.intent.StandbyFor != 2*time.Hour {
		t.Fatal(got, err)
	}
	off := strings.Replace(goodIntent, `"intent":"on"`, `"intent":"off"`, 1)
	if got, err = decodeControl([]byte(off), jsonInput); err != nil || got.intent.Intent != "off" || got.intent.StandbyFor != 0 {
		t.Fatal(got, err)
	}
	// Nor can it extend standby or open the allowance: those stay with the
	// person at the Mac.
	for _, intent := range []string{"extend", "allow-run", "deny-run"} {
		if _, err := decodeControl([]byte(strings.Replace(goodIntent, `"intent":"on"`, `"intent":"`+intent+`"`, 1)), jsonInput); err == nil {
			t.Fatal("JSON entry accepted", intent)
		}
	}
	for _, line := range []string{"status\n", "close\n", "on 1 2\n", "on 1 2 2\n"} {
		if _, err := decodeControl([]byte(line), jsonInput); err == nil {
			t.Fatal("existing JSON entry accepted CLI input")
		}
	}
	if _, err := decodeControl([]byte(goodIntent), inputDialect(255)); err == nil {
		t.Fatal("unknown dialect accepted")
	}
}

// A refused allowance or extension is not a standby transition. If it were
// reported as one, the status feed would show an ON/OFF change that never
// happened, and a stale allow could block the next real ON behind a pending
// request.
func TestStaleLocalActionLeavesNoTransitionEvidence(t *testing.T) {
	for _, intent := range []string{"allow-run", "deny-run", "extend"} {
		var m mapper
		o := fixtureOff()
		_, _ = m.observe(o, 100)
		r := m.submit(nil, Intent{RequestID: 2, Intent: intent, ExpectedRevision: 99}, 100)
		if r.Disposition != "stale" || r.Intent != intent || m.refused != nil || m.pending != nil || m.poisoned {
			t.Fatalf("%s: %+v refused=%v pending=%v", intent, r, m.refused, m.pending)
		}
	}
}
