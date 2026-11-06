// Package residentmac composes the protected Mac scope factory with the actual
// single-grant transport. It does not register, dial, enroll or launch on Load.
package residentmac

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
)

type nativeTransport struct{}

func (nativeTransport) NewClient(c maclaunch.ResidentClientConfig, o *maclaunch.Approved) (maclaunch.ResidentClient, error) {
	b := agenttransport.DefaultBudgets()
	b.Cleanup = c.Cleanup
	return agenttransport.NewClient(agenttransport.ClientConfig{Endpoint: c.Binding.Endpoint, HostLabel: c.Binding.HostLabel, PrincipalLabel: c.Binding.PrincipalLabel, HostKey: c.HostKey, Budgets: b, ResidentIntent: c.WireIntent}, o)
}

// The standby-specific loader positively classifies only pre-key policy refusal.
func loadStandbyFactory(ctx context.Context, path string) (*FixedFactory, error) {
	native, err := maclaunch.LoadStandbyResident(ctx, path, nativeTransport{}, agentgateway.ChannelBudgets())
	if err != nil {
		return nil, err
	}
	return &FixedFactory{native: native}, nil
}
