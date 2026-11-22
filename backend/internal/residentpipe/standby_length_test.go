package residentpipe

import (
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

func jsonIntentLine(intent, extra string) []byte {
	return []byte(`{"protocolVersion":1,"type":"intent","request":{"protocolVersion":1,"requestId":1,"expectedRevision":0,"intent":"` + intent + `"` + extra + `}}` + "\n")
}

// The host GUI picks how long standby stays on and extends it before it ends
// (#406, #451), so the product's JSON names the length too, but only as one of
// the listed hours: no other number can turn into a listed one. An ON that
// names none keeps the default two hours, so existing callers are unchanged.
// EXTEND must name one, and OFF names none.
func TestJSONIntentNamesTheStandbyLength(t *testing.T) {
	for hours, want := range map[string]time.Duration{"2": 2 * time.Hour, "4": 4 * time.Hour, "8": 8 * time.Hour, "12": 12 * time.Hour, "24": 24 * time.Hour} {
		for _, intent := range []string{"on", "extend"} {
			c, err := decodeControl(jsonIntentLine(intent, `,"standbyHours":`+hours), jsonInput)
			if err != nil || c.kind != controlIntent || c.intent.Intent != intent || c.intent.StandbyFor != want {
				t.Fatal(intent, hours, c.intent, err)
			}
		}
	}
	if c, err := decodeControl(jsonIntentLine("on", ""), jsonInput); err != nil || c.intent.StandbyFor != residentmac.DefaultStandbyFor {
		t.Fatal("on without a length", c.intent, err)
	}
	if c, err := decodeControl(jsonIntentLine("off", ""), jsonInput); err != nil || c.intent.StandbyFor != 0 {
		t.Fatal("off", c.intent, err)
	}
	for name, line := range map[string][]byte{
		"unlisted length":       jsonIntentLine("on", `,"standbyHours":3`),
		"zero length":           jsonIntentLine("on", `,"standbyHours":0`),
		"wrapped length":        jsonIntentLine("on", `,"standbyHours":9007199254740991`),
		"string length":         jsonIntentLine("on", `,"standbyHours":"2"`),
		"null length":           jsonIntentLine("on", `,"standbyHours":null`),
		"duplicate length":      jsonIntentLine("on", `,"standbyHours":2,"standbyHours":4`),
		"extend without length": jsonIntentLine("extend", ""),
		"off with a length":     jsonIntentLine("off", `,"standbyHours":2`),
		"run allowance":         jsonIntentLine("allow-run", ""),
	} {
		if _, err := decodeControl(line, jsonInput); err == nil {
			t.Error("accepted", name)
		}
	}
}
