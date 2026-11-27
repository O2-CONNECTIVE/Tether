package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostpairoauth"
)

// Why: scripts/build_mac_host_pair.py accepts only a narrow allowlist of values. Every value it
// accepts must also pass here, or a signed release would embed a helper that always reports unavailable.
func TestEverythingTheBuilderAcceptsIsAcceptedByTheHelper(t *testing.T) {
	raw, err := os.ReadFile("../../internal/hostpairoauth/testdata/pair_config_values.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Accepted struct{ Gateway, Issuer, ClientID []string }
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Accepted.Gateway) == 0 || len(table.Accepted.Issuer) == 0 || len(table.Accepted.ClientID) == 0 {
		t.Fatal("accepted table is empty")
	}
	for _, gateway := range table.Accepted.Gateway {
		if !validGatewayForm(gateway) {
			t.Errorf("gateway %q rejected", gateway)
		}
	}
	for _, issuer := range table.Accepted.Issuer {
		if !hostpairoauth.ValidClient(issuer, "client") {
			t.Errorf("issuer %q rejected", issuer)
		}
	}
	for _, clientID := range table.Accepted.ClientID {
		if !hostpairoauth.ValidClient("https://issuer.example", clientID) {
			t.Errorf("clientID %q rejected", clientID)
		}
	}
}
