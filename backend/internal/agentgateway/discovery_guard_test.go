package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type discoveryControl struct{}

func (discoveryControl) ResidentGeneration(string, string) string { panic("control queried") }
func (discoveryControl) BeginResidentFixed(context.Context, string, string, string, string, time.Time) error {
	panic("control invoked")
}

func TestDiscoveryResidentGuardPrecedesOtherwiseValidEnrollment(t *testing.T) {
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x32}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	entries := []ResidentEnrollment{{HostLabel: "host-1", PrincipalLabel: "member-1", HostKey: host.PublicKey().Marshal()}}
	// White-box defense-in-depth test: New forbids this signer in discovery.
	// Identical otherwise-valid composition succeeds only in ordinary mode.
	for _, discovery := range []bool{false, true} {
		g := &Gateway{discoveryOnly: discovery, byLabel: map[string]*principal{"member-1": {label: "member-1", signer: signer}}}
		err := g.ConfigureResident("wss://gateway.example.invalid/tether-agent/v1/register", discoveryControl{}, entries)
		if discovery && (err == nil || g.resident != nil) {
			t.Fatal("discovery resident configuration admitted")
		}
		if !discovery && err != nil {
			t.Fatal("ordinary control setup regressed", err)
		}
	}
}
