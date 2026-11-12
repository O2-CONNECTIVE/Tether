package residentpipe

import (
	"strings"
	"testing"
)

func TestForegroundCommands(t *testing.T) {
	for _, tc := range []struct {
		line   string
		kind   controlKind
		intent Intent
	}{
		{"status\n", controlStatus, Intent{}},
		{"close\n", controlClose, Intent{}},
		{"on 1 0\n", controlIntent, Intent{1, 1, 0, "on"}},
		{"off 9007199254740991 9007199254740991\n", controlIntent, Intent{1, MaxInteger, MaxInteger, "off"}},
		// The person at the Mac opens and closes the command-run allowance here.
		// The words carry no duration: the owner's fixed limit is the only one.
		{"allow 3 7\n", controlIntent, Intent{1, 3, 7, "allow-run"}},
		{"deny 4 7\n", controlIntent, Intent{1, 4, 7, "deny-run"}},
	} {
		got, err := decodeControl([]byte(tc.line), foregroundInput)
		if err != nil || got.kind != tc.kind || got.intent != tc.intent {
			t.Fatalf("%q: %#v %v", tc.line, got, err)
		}
	}
}

func TestForegroundIntentKeepsMapperUnknown(t *testing.T) {
	m := mapper{poisoned: true}
	for _, line := range []string{"on 1 1\n", "off 2 1\n", "allow 3 1\n", "deny 4 1\n"} {
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
		"on\n", "on 1\n", "on 1 2 3\n", "on 0 1\n", "on 01 1\n", "on +1 1\n", "on 1 -1\n",
		"on 1 01\n", "on 1 1.0\n", "on 1 1e2\n", "on\t1 1\n", "on  1 1\n",
		"on 9007199254740992 1\n", "on 1 9007199254740992\n", "close 1\n",
		"allow\n", "allow 1\n", "allow 1 1 900\n", "allow 0 1\n", "allow-run 1 1\n", "deny-run 1 1\n", "deny 1\n", "ALLOW 1 1\n",
		"start\n", "cancel 1\n", "policy /tmp/policy\n", "on 1 1\nclose\n",
		"status\x00\n", "\xff\n", strings.Repeat("x", MaxLine) + "\n", goodIntent,
	} {
		if _, err := decodeControl([]byte(line), foregroundInput); err == nil {
			t.Fatalf("accepted ambiguous foreground input %q", line)
		}
	}
}

func TestControlDialectsRemainSeparate(t *testing.T) {
	got, err := decodeControl([]byte(goodIntent), jsonInput)
	if err != nil || got.kind != controlIntent || got.intent.RequestID != 1 {
		t.Fatal(got, err)
	}
	for _, line := range []string{"status\n", "close\n", "on 1 2\n"} {
		if _, err := decodeControl([]byte(line), jsonInput); err == nil {
			t.Fatal("existing JSON entry accepted CLI input")
		}
	}
	if _, err := decodeControl([]byte(goodIntent), inputDialect(255)); err == nil {
		t.Fatal("unknown dialect accepted")
	}
}

// A refused allowance is not a standby transition. If it were reported as one,
// the status feed would show an ON/OFF change that never happened, and a stale
// allow could block the next real ON behind a pending request.
func TestStaleAllowanceLeavesNoTransitionEvidence(t *testing.T) {
	for _, intent := range []string{"allow-run", "deny-run"} {
		var m mapper
		o := fixtureOff()
		_, _ = m.observe(o, 100)
		r := m.submit(nil, Intent{RequestID: 2, Intent: intent, ExpectedRevision: 99}, 100)
		if r.Disposition != "stale" || r.Intent != intent || m.refused != nil || m.pending != nil || m.poisoned {
			t.Fatalf("%s: %+v refused=%v pending=%v", intent, r, m.refused, m.pending)
		}
	}
}
