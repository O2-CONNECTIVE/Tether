package agentgateway_test

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// lp is the length-prefixed concatenation every rescue transcript uses.
func lp(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = binary.BigEndian.AppendUint32(out, uint32(len(p)))
		out = append(out, p...)
	}
	return out
}

func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// The host (#424) verifies these exact bytes with its own copy of the
// layout. A field that moved, dropped or changed its domain would let one
// side accept what the other never signed.
func TestRescueRequestTranscriptLayout(t *testing.T) {
	got := agentgateway.RescueRequestTranscript("https://gw.example/tether-agent/v1/rescue", []byte("R"), []byte("K"), []byte("N"), []byte("C"), "山田（サポート）", "O2 CONNECTIVE", agentgateway.RescueProfile)
	want := lp([]byte("rescue-host.agentgateway.rescue-request\x00v1\x00"), []byte("https://gw.example/tether-agent/v1/rescue"),
		[]byte("R"), []byte("K"), []byte("N"), []byte("C"), []byte("山田（サポート）"), []byte("O2 CONNECTIVE"), []byte("mac_current_user"))
	if !bytes.Equal(got, want) {
		t.Fatalf("RescueRequestTranscript = %x, want %x", got, want)
	}
}

// Only an allow carries the dispatch: the host signs the grant it issued, so
// the gateway connects with exactly that Binding, deadline and host key.
func TestRescueAnswerTranscriptLayout(t *testing.T) {
	d := agentgateway.Dispatch{
		Binding:  agentchannel.Binding{ConnectionGeneration: 2, KeySetDigest: [32]byte{1, 2}, Profile: agentexec.ProfileMacCurrentUser},
		Deadline: time.Unix(0, 1_700_000_000_123_456_789), HostKey: []byte("K"),
	}
	d.Binding.Grant.Generation = 7
	d.Binding.Grant.Identity.LaunchID, d.Binding.Grant.Identity.Epoch, d.Binding.Grant.Identity.PolicyDigest = "L", "E", "P"
	head := [][]byte{[]byte("rescue-host.agentgateway.rescue-answer\x00v1\x00"), []byte("R"), []byte("N"), []byte("D")}

	for _, answer := range []string{agentgateway.RescueDeny, agentgateway.RescueTimeout} {
		if got, want := agentgateway.RescueAnswerTranscript([]byte("R"), []byte("N"), []byte("D"), answer, d), lp(append(head, []byte(answer))...); !bytes.Equal(got, want) {
			t.Errorf("%s transcript = %x, want %x (no dispatch)", answer, got, want)
		}
	}
	allow := append(head, []byte("allow"), u64(7), u64(2), []byte("L"), []byte("E"), []byte("P"), d.Binding.KeySetDigest[:],
		[]byte{byte(agentexec.ProfileMacCurrentUser)}, u64(uint64(d.Deadline.UnixNano())), []byte("K"))
	if got, want := agentgateway.RescueAnswerTranscript([]byte("R"), []byte("N"), []byte("D"), agentgateway.RescueAllow, d), lp(allow...); !bytes.Equal(got, want) {
		t.Fatalf("allow transcript = %x, want %x", got, want)
	}
}
