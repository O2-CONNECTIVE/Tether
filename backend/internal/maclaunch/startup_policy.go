package maclaunch

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
)

// This private type is minted only after a successful protected policy read,
// before key/factory acquisition. It is not Close, OFF, authorization, or proof
// that every runtime allocation/thread has retired before public return.
// Consumers still require the exact producer's EOF, exit and physical joins.
type startupPolicyRejected struct{}

func (startupPolicyRejected) Error() string { return "resident startup policy rejected" }
func (startupPolicyRejected) Unwrap() error { return ErrResident }

// IsStartupPolicyRejected accepts only the direct concrete loader result.
// Wrapping or joining ANY error loses this positive classification, including
// a join with cleanup uncertainty. Strings and caller-defined types cannot mint it.
func IsStartupPolicyRejected(err error) bool {
	_, ok := err.(startupPolicyRejected)
	return ok
}

// LoadStandbyResident is the strict concrete standby loader. General resident
// loading is unchanged. This path checks the existing fixed scope before keys.
func LoadStandbyResident(ctx context.Context, policyPath string, transport ResidentTransport, budgets agentchannel.Budgets) (*ResidentFactory, error) {
	if !localSupported() || transport == nil {
		return nil, ErrResident
	}
	return loadResidentMode(ctx, policyPath, residentDeps{
		read: agentkeys.ReadTrustConfig, key: agentkeys.LoadCurrentUser,
		runtime: func(c macagent.Config) (agentexec.ProfiledRuntime, error) { return macagent.New(c) },
		client:  transport.NewClient, budgets: budgets,
	}, true)
}
func fixedResidentPolicy(p residentPolicy) bool {
	o, ok := p.operations[ResidentFixedRequestID]
	return len(p.operations) == 1 && ok && o.command == ResidentFixedCommand && o.timeout == 15000
}
