package residentpipe

import (
	"encoding/json"
	"os"
	"testing"
)

// Frozen framing vectors test Go framing, not current Rust equivalence or
// status authority. No Rust-to-Go runtime interoperability is claimed.
func TestSharedBoundaryVectors(t *testing.T) {
	b, e := os.ReadFile("testdata/resident-duplex-boundaries.json")
	if e != nil {
		t.Fatal(e)
	}
	var vectors []struct {
		Name         string `json:"name"`
		Frame        string `json:"frame"`
		PayloadBytes int    `json:"payloadBytes"`
		Accepted     bool   `json:"accepted"`
	}
	if e = json.Unmarshal(b, &vectors); e != nil {
		t.Fatal(e)
	}
	if len(vectors) != 6 {
		t.Fatal("six vectors required")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			line := []byte(v.Frame)
			n := len(line)
			if n > 0 && line[n-1] == '\n' {
				n--
			}
			if n != v.PayloadBytes {
				t.Fatal("wrong fixture byte count")
			}
			if (validateFrame(line) == nil) != v.Accepted {
				t.Fatal("framing mismatch")
			}
		})
	}
}
