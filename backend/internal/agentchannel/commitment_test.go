package agentchannel

import (
	"encoding/hex"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func TestCommitmentIncludesIndependentConnectionGeneration(t *testing.T) {
	b := Binding{
		Grant: supervisor.Binding{Generation: 7, Identity: supervisor.Identity{
			LaunchID: "launch", Epoch: "epoch", PolicyDigest: "policy",
		}},
		ConnectionGeneration: 1,
		KeySetDigest:         [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		Profile:              agentexec.ProfileMacCurrentUser,
	}
	hostKey := []byte("host")
	for _, tc := range []struct {
		generation uint64
		golden     string
	}{
		{1, "80fb34d81fe26be8f977c48e9cbeb38b7e9b1a9e0d834bcd906cb8653c1a1876"},
		{2, "415b8771948fe074dec3f6f24c31029a7eb4de2f79e14dc4bf9f2a61c1341922"},
	} {
		b.ConnectionGeneration = tc.generation
		digest := commitment(b, hostKey)
		if got := hex.EncodeToString(digest[:]); got != tc.golden {
			t.Fatalf("commitment with ConnectionGeneration %d = %s; want %s", tc.generation, got, tc.golden)
		}
	}
	b.ConnectionGeneration = 1
	grantChanged := b
	grantChanged.Grant.Generation++
	if commitment(b, hostKey) == commitment(grantChanged, hostKey) {
		t.Fatal("changing Grant.Generation did not change the commitment")
	}
}
