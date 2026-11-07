//go:build darwin && cgo && ci_actual_producer

// Compiler-overlay composition only; protected native authority is unchanged.
package residentmac

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
)

type ciProducerTransport struct {
	trust *agenttransport.CIProducerTrust
}

func (t ciProducerTransport) NewClient(cfg maclaunch.ResidentClientConfig, owner *maclaunch.Approved) (maclaunch.ResidentClient, error) {
	budgets := agenttransport.DefaultBudgets()
	budgets.Cleanup = cfg.Cleanup
	client, err := agenttransport.NewClient(agenttransport.ClientConfig{Endpoint: cfg.Binding.Endpoint, HostLabel: cfg.Binding.HostLabel, PrincipalLabel: cfg.Binding.PrincipalLabel, HostKey: cfg.HostKey, Budgets: budgets, ResidentIntent: cfg.WireIntent}, owner)
	if err != nil {
		return nil, err
	}
	if err = t.trust.BindClient(client); err != nil {
		return nil, err
	}
	return client, nil
}

// CILoadProducer uses the SAME strict reader, key loader, policy, actual native
// runtime, signed receiver and worker capsule; only transport trust is scoped.
func CILoadProducer(ctx context.Context, policy string, trust *agenttransport.CIProducerTrust) (*Standby, error) {
	if trust == nil {
		return nil, ErrStandbyUnavailable
	}
	native, err := maclaunch.LoadStandbyResident(ctx, policy, ciProducerTransport{trust}, agentgateway.ChannelBudgets())
	if err != nil {
		return nil, err
	}
	if !native.FixedWirePolicy() {
		return nil, ErrStandbyUnavailable
	}
	f := &FixedFactory{native: native}
	s := newStandby()
	s.factory = f
	s.makeConnector = func(ctx context.Context) (residentConnector, error) {
		client, err := f.native.NewWireConnector(ctx)
		if err != nil {
			return nil, err
		}
		if err = trust.BindResident(client); err != nil {
			return nil, err
		}
		return client, nil
	}
	return s, nil
}
