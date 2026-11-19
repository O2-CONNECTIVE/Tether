package agentgateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// #408: a rescue operator helps someone else's host; a resident owner uses
// only its own host. The two never borrow each other's path, even when one
// person holds both roles.

var (
	rescueOnly   = []agentgateway.Role{agentgateway.RoleRescueOperator}
	residentOnly = []agentgateway.Role{agentgateway.RoleResidentOwner}
)

// roleRig is a gateway whose four principals have the given roles and the
// same subjects and signers as newModeRig.
func roleRig(t *testing.T, roles ...[]agentgateway.Role) *rig {
	t.Helper()
	cfg := agentgateway.Config{Verifier: verifier(t), Resource: testResource}
	for i, r := range roles {
		cfg.Principals = append(cfg.Principals, agentgateway.Principal{Label: label(i), Subject: subjects[i], Roles: r, Signer: fixtureSigner(t, byte(0x21+i))})
	}
	return newModeRig(t, cfg)
}

// approvedHostKey is the host key r.approve(i) and r.connectApproved(i) present.
func approvedHostKey(t *testing.T, i int) []byte {
	return fixtureSigner(t, byte(0x61+i)).PublicKey().Marshal()
}

// idleControl is a resident control whose host never connects.
type idleControl struct{}

func (idleControl) ResidentGeneration(string, string) string { return "" }
func (idleControl) BeginResidentFixed(context.Context, string, string, string, string, time.Time) error {
	return agentgateway.ErrResidentWorkUnconfigured
}
func (idleControl) BeginResidentRun(context.Context, string, string, string, string, time.Time, agentgateway.ResidentRunScope) error {
	return agentgateway.ErrResidentWorkUnconfigured
}

const residentEndpoint = "wss://gateway.example.invalid/register"

func TestRoleRescueOperatorCannotReachAResidentHost(t *testing.T) {
	// member-1 owns "home"; member-3 has both roles and owns "home-3";
	// member-4 only rescues and has a plain host of its own.
	r := roleRig(t, residentOnly, rescueOnly, bothRoles, rescueOnly)
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{
		{Label: "home", Key: approvedHostKey(t, 0), Principals: []string{label(0)}},
		{Label: "home-3", Key: approvedHostKey(t, 2), Principals: []string{label(2)}},
		{Label: "desk", Key: approvedHostKey(t, 3), Principals: []string{label(3)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.ConfigureResident(residentEndpoint, idleControl{}, []agentgateway.ResidentEnrollment{
		{HostLabel: "home", PrincipalLabel: label(0)}, {HostLabel: "home-3", PrincipalLabel: label(2)},
	}); err != nil {
		t.Fatal(err)
	}
	// A rescue connection to a host that serves rescue still opens; the
	// refusal below is about the resident host, not a broken Connect.
	if _, err := r.connectApproved(3, time.Minute); err != nil {
		t.Fatalf("rescue operator on its rescue host: Connect = %v", err)
	}
	// #404 decision 4: rescue on a Mac that has a resident pair runs with a
	// one-time key, never through the resident host entry, so holding both
	// roles does not open the rescue path to one's own resident host.
	if _, err := r.connectApproved(2, time.Minute); !errors.Is(err, agentgateway.ErrRefused) {
		t.Fatalf("dual-role owner to its own resident host: Connect = %v, want ErrRefused", err)
	}
}

func TestRoleResidentHostServesOnlyItsOwner(t *testing.T) {
	r := roleRig(t, residentOnly, rescueOnly, residentOnly, residentOnly)
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{
		{Label: "shared-rescue", Key: approvedHostKey(t, 0), Principals: []string{label(0), label(1)}},
		{Label: "shared-owners", Key: approvedHostKey(t, 1), Principals: []string{label(2), label(3)}},
		{Label: "other", Key: approvedHostKey(t, 2), Principals: []string{label(1)}},
		{Label: "own", Key: approvedHostKey(t, 3), Principals: []string{label(0)}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		entries []agentgateway.ResidentEnrollment
	}{
		// Anyone else on the resident host could reach the owner's host.
		{"a rescue operator also listed", []agentgateway.ResidentEnrollment{{HostLabel: "shared-rescue", PrincipalLabel: label(0)}}},
		{"two owners of one host", []agentgateway.ResidentEnrollment{{HostLabel: "shared-owners", PrincipalLabel: label(2)}, {HostLabel: "shared-owners", PrincipalLabel: label(3)}}},
		{"a host the owner is not listed on", []agentgateway.ResidentEnrollment{{HostLabel: "other", PrincipalLabel: label(0)}}},
	} {
		if err := r.gw.ConfigureResident(residentEndpoint, idleControl{}, tc.entries); !errors.Is(err, agentgateway.ErrConfig) {
			t.Fatalf("%s: ConfigureResident = %v, want ErrConfig", tc.name, err)
		}
	}
	if err := r.gw.ConfigureResident(residentEndpoint, idleControl{}, []agentgateway.ResidentEnrollment{{HostLabel: "own", PrincipalLabel: label(0)}}); err != nil {
		t.Fatalf("the owner's own host: ConfigureResident = %v", err)
	}
}

func TestRoleResidentEnrollmentAfterAConnectionIsRefused(t *testing.T) {
	// Host use is fixed before any registration: enrolling a host that a
	// rescue route already reached would leave that route on a resident host.
	r := roleRig(t, bothRoles)
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{{Label: "desk", Key: approvedHostKey(t, 0), Principals: []string{label(0)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.connectApproved(0, time.Minute); err != nil {
		t.Fatalf("rescue connection: %v", err)
	}
	if err := r.gw.ConfigureResident(residentEndpoint, idleControl{}, []agentgateway.ResidentEnrollment{{HostLabel: "desk", PrincipalLabel: label(0)}}); !errors.Is(err, agentgateway.ErrRefused) {
		t.Fatalf("ConfigureResident after a connection = %v, want ErrRefused", err)
	}
}

func TestRoleRescueOperatorCannotEnrollResidentWork(t *testing.T) {
	r := roleRig(t, residentOnly, rescueOnly)
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{
		{Label: "home-1", Key: approvedHostKey(t, 0), Principals: []string{label(0)}},
		{Label: "home-2", Key: approvedHostKey(t, 1), Principals: []string{label(1)}},
	}); err != nil {
		t.Fatal(err)
	}
	// Resident work reaches a host only through an enrollment; a rescue
	// operator must never get one, so it can never send resident work.
	if err := r.gw.ConfigureResident(residentEndpoint, idleControl{}, []agentgateway.ResidentEnrollment{
		{HostLabel: "home-1", PrincipalLabel: label(0)}, {HostLabel: "home-2", PrincipalLabel: label(1)},
	}); !errors.Is(err, agentgateway.ErrConfig) {
		t.Fatalf("resident enrollment of a rescue operator = %v, want ErrConfig", err)
	}
	if err := r.gw.ConfigureResident(residentEndpoint, idleControl{}, []agentgateway.ResidentEnrollment{{HostLabel: "home-1", PrincipalLabel: label(0)}}); err != nil {
		t.Fatalf("resident enrollment of the resident owner = %v", err)
	}
}

func TestRoleRescueOperatorCannotPair(t *testing.T) {
	// A pair slot lets its principal replace a host key with its IdP login
	// alone; only a resident owner may hold one.
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
	// With the owner's slot configured, the rescue operator's pair token
	// finds no slot of its own and never reaches the owner's.
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

func TestRoleResidentOwnerCannotRequestRescue(t *testing.T) {
	// Both are listed on a plain host that accepts the rescue connection.
	r := roleRig(t, residentOnly, rescueOnly)
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{
		{Label: "desk-1", Key: approvedHostKey(t, 0), Principals: []string{label(0)}},
		{Label: "desk-2", Key: approvedHostKey(t, 1), Principals: []string{label(1)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.connectApproved(1, time.Minute); err != nil {
		t.Fatalf("rescue operator: Connect = %v", err)
	}
	// A resident owner may use only its own resident host, never another
	// person's host through the rescue connection.
	if _, err := r.connectApproved(0, time.Minute); !errors.Is(err, agentgateway.ErrRefused) {
		t.Fatalf("resident owner: Connect = %v, want ErrRefused", err)
	}
}

func TestRoleConfigRefusesMissingOrUnknownRoles(t *testing.T) {
	withRoles := func(roles any) func(map[string]any) {
		return func(d map[string]any) { d["principals"].([]any)[1].(map[string]any)["roles"] = roles }
	}
	ok, err := agentgateway.ParseConfig(configDoc(t, withRoles([]string{"resident_owner", "rescue_operator"})))
	if err != nil || len(ok.Principals[1].Roles) != 2 || ok.Principals[1].Roles[0] != agentgateway.RoleResidentOwner || ok.Principals[1].Roles[1] != agentgateway.RoleRescueOperator {
		t.Fatalf("both roles = %+v, %v", ok.Principals, err)
	}
	for _, tc := range []struct {
		name   string
		edit   func(map[string]any)
		reason string // the diagnostic names the second principal by position
	}{
		{"missing roles", func(d map[string]any) { delete(d["principals"].([]any)[1].(map[string]any), "roles") }, "principals の 2 番目"},
		{"empty roles", withRoles([]string{}), "principals の 2 番目"},
		{"unknown role", withRoles([]string{"admin"}), "principals の 2 番目"},
		{"display name instead of value", withRoles([]string{"レスキュー担当"}), "principals の 2 番目"},
		{"case variant", withRoles([]string{"Rescue_Operator"}), "principals の 2 番目"},
		{"duplicate role", withRoles([]string{"rescue_operator", "rescue_operator"}), "principals の 2 番目"},
		{"role as string", withRoles("rescue_operator"), "principals の 2 番目"},
		{"null role", withRoles([]any{nil}), ""},
		{"null roles", withRoles(nil), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentgateway.ParseConfig(configDoc(t, tc.edit))
			if !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("ParseConfig = %v, want ErrConfig", err)
			}
			var re *agentgateway.RoleError
			if tc.reason != "" && (!errors.As(err, &re) || !strings.Contains(re.Reason, tc.reason)) {
				t.Fatalf("refusal %v does not name %q", err, tc.reason)
			}
			for _, leak := range []string{"member-", "synthetic"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("refusal leaks %q: %v", leak, err)
				}
			}
		})
	}
	// The same rule holds for a composition that builds Config directly.
	if _, err := agentgateway.New(agentgateway.Config{Verifier: verifier(t), Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0]}}}); !errors.Is(err, agentgateway.ErrConfig) {
		t.Fatalf("New without roles = %v, want ErrConfig", err)
	}
}

func TestRoleAssuranceIsCheckedButMultiFactorRefusedUntilEnforced(t *testing.T) {
	assurance := func(v any) func(map[string]any) { return func(d map[string]any) { d["role_assurance"] = v } }
	c, err := agentgateway.ParseConfig(configDoc(t, assurance(map[string]any{"rescue_operator": "any", "resident_owner": "any"})))
	if err != nil || c.RoleAssurance[agentgateway.RoleRescueOperator] != agentgateway.AssuranceAny || c.RoleAssurance[agentgateway.RoleResidentOwner] != agentgateway.AssuranceAny {
		t.Fatalf("explicit any = %+v, %v", c.RoleAssurance, err)
	}
	if g, err := c.GatewayConfig(); err != nil || g.RoleAssurance[agentgateway.RoleRescueOperator] != agentgateway.AssuranceAny {
		t.Fatalf("GatewayConfig dropped role_assurance: %+v, %v", g.RoleAssurance, err)
	}
	if c, err := agentgateway.ParseConfig(configDoc(t, nil)); err != nil || c.RoleAssurance != nil {
		t.Fatalf("omitted role_assurance = %+v, %v", c.RoleAssurance, err)
	}
	for _, tc := range []struct {
		name   string
		value  any
		reason string
	}{
		// Until #398 checks the token's factors, accepting multi_factor would
		// pass a single-factor token through a role that asked for more.
		{"multi factor for rescue", map[string]any{"rescue_operator": "multi_factor"}, "multi_factor"},
		{"multi factor for resident", map[string]any{"resident_owner": "multi_factor"}, "multi_factor"},
		{"unknown strength", map[string]any{"rescue_operator": "mfa"}, "role_assurance"},
		{"unknown role", map[string]any{"admin": "any"}, ""},
		{"empty object", map[string]any{}, ""},
		{"not an object", []any{"any"}, ""},
		{"non-string strength", map[string]any{"rescue_operator": true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentgateway.ParseConfig(configDoc(t, assurance(tc.value)))
			if !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("ParseConfig = %v, want ErrConfig", err)
			}
			var re *agentgateway.RoleError
			if tc.reason != "" && (!errors.As(err, &re) || !strings.Contains(re.Reason, tc.reason)) {
				t.Fatalf("refusal %v does not name %q", err, tc.reason)
			}
		})
	}
	// Access mode reads the same member.
	if _, err := agentgateway.ParseConfig(accessConfigDoc(t, func(d map[string]any) { d["role_assurance"] = map[string]any{"rescue_operator": "multi_factor"} })); !errors.Is(err, agentgateway.ErrConfig) {
		t.Fatalf("Access mode multi_factor = %v, want ErrConfig", err)
	}
	if _, err := agentgateway.New(agentgateway.Config{Verifier: verifier(t), RoleAssurance: map[agentgateway.Role]agentgateway.Assurance{agentgateway.RoleResidentOwner: agentgateway.AssuranceMultiFactor},
		Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0], Roles: residentOnly}}}); !errors.Is(err, agentgateway.ErrConfig) {
		t.Fatalf("New with multi_factor = %v, want ErrConfig", err)
	}
}

func TestRoleRescueNeverReachesAPairSlot(t *testing.T) {
	// A pair slot is a resident host even before (or without) a resident
	// enrollment: its key came from the owner's pairing, not a rescue consent.
	r := roleRig(t, bothRoles)
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{{Label: "home", Principals: []string{label(0)}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.ConfigurePairing(agentgateway.PairingConfig{Verifier: pairingVerifier(t), Endpoint: "https://gateway.example.invalid" + agentgateway.PairPath, RegistrationEndpoint: residentEndpoint,
		Records: []agentgateway.PairRecord{{HostLabel: "home", HostKey: approvedHostKey(t, 0), Issuer: testIssuer, Subject: subjects[0]}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.connectApproved(0, time.Minute); !errors.Is(err, agentgateway.ErrRefused) {
		t.Fatalf("rescue connection to a pair slot: Connect = %v, want ErrRefused", err)
	}
}
