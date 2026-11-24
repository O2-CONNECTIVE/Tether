//go:build tether_development

package agentgateway_test

import (
	"errors"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func TestRoleRescueOperatorCannotPair(t *testing.T) {
	// A pair slot lets its principal replace a host key with its IdP login alone; only a resident owner may hold one.
	for _, tc := range []struct {
		name  string
		roles []agentgateway.Role
		want  error
	}{
		{"rescue operator", rescueOnly, agentgateway.ErrConfig},
		{"resident owner", residentOnly, nil},
		{"both roles", bothRoles, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := roleRig(t, tc.roles)
			err := r.gw.ConfigureHosts([]agentgateway.HostEntry{{Label: "slot-1", Principals: []string{label(0)}, Paired: true}})
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("paired slot for %s = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
	r := roleRig(t, residentOnly, rescueOnly)
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{{Label: "slot-1", Principals: []string{label(0)}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.ConfigurePairing(agentgateway.PairingConfig{Verifier: pairingVerifier(t), Endpoint: "https://gateway.example.invalid" + agentgateway.PairPath, RegistrationEndpoint: residentEndpoint}); err != nil {
		t.Fatal(err)
	}
	if rr := pairRequest(t, r.gw, fixtureSigner(t, 0x31), subjects[1]); rr.Code != 403 {
		t.Fatalf("rescue operator pair status = %d, want 403", rr.Code)
	}
}
