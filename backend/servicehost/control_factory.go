package servicehost

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// serviceNativeFactories is private construction provenance, not authentication
// or readiness evidence. Both concrete factories are immutable after creation;
// no external Factory/Scope or setter can substitute for this pair's members.
type serviceNativeFactories struct {
	operatorSID, guestSID string // immutable copy of this composition's fixed profile
	self                  *serviceNativeFactories
	ready                 *serviceready.Factory
	auth                  *bootstrapauth.Factory
	batch                 *operatorauth.EndpointFactory // independently protected operator policy remains unconfigured
	profile               bootstrapauth.Profile
	primaryToken          uintptr // borrowed for same-Service verification, never a helper launch token
}

func newServiceNativeFactories(c serviceready.Config) (*serviceNativeFactories, error) {
	return buildServiceNativeFactories(c, serviceNativeConstructors{ready: serviceready.NewNative, auth: bootstrapauth.NewNativeFactory})
}

// Constructor replacement is a package-private static construction test seam.
// The actual composition above always supplies the two real constructors.
type serviceNativeConstructors struct {
	ready func(serviceready.Config) (*serviceready.Factory, error)
	auth  func(bootstrapauth.Profile) (*bootstrapauth.Factory, error)
}

func buildServiceNativeFactories(c serviceready.Config, constructors serviceNativeConstructors) (*serviceNativeFactories, error) {
	if constructors.ready == nil || constructors.auth == nil {
		return nil, ErrProvisioningUnconfigured
	}
	c = serviceNativeSnapshot(c)
	ready, err := constructors.ready(c)
	if err != nil || ready == nil {
		return nil, ErrProvisioningRejected
	}
	auth, err := constructors.auth(c.Profile)
	if err != nil || auth == nil {
		return nil, ErrProvisioningRejected
	}
	p := &serviceNativeFactories{ready: ready, auth: auth, operatorSID: c.Profile.Keys.OperatorSID, guestSID: c.Profile.Keys.GuestSID, profile: c.Profile, primaryToken: c.PrimaryToken}
	p.self = p
	return p, nil
}

// Snapshot every mutable configuration field before either constructor runs.
// Both real constructors retain their existing deep copies as well. Keep this
// typed copy in step with Config/Profile/KeyProfile/NodePolicy field changes.
func serviceNativeSnapshot(c serviceready.Config) serviceready.Config {
	c.ServiceSecurity.ACL = append([]protectedkey.ACE(nil), c.ServiceSecurity.ACL...)
	c.ProcessSecurity.ACL = append([]protectedkey.ACE(nil), c.ProcessSecurity.ACL...)
	c.Profile.SupervisorImage.Nodes = serviceNativeNodes(c.Profile.SupervisorImage.Nodes)
	c.Profile.ChildImage.Nodes = serviceNativeNodes(c.Profile.ChildImage.Nodes)
	if c.Profile.Keys.Catalog != nil {
		catalog := make(map[string]protectedkey.KeyProfile, len(c.Profile.Keys.Catalog))
		for ref, key := range c.Profile.Keys.Catalog {
			key.Nodes = serviceNativeNodes(key.Nodes)
			catalog[ref] = key
		}
		c.Profile.Keys.Catalog = catalog
	}
	return c
}

func serviceNativeNodes(nodes []protectedkey.NodePolicy) []protectedkey.NodePolicy {
	out := append([]protectedkey.NodePolicy(nil), nodes...)
	for i := range out {
		out[i].ACL = append([]protectedkey.ACE(nil), out[i].ACL...)
	}
	return out
}

func (p *serviceNativeFactories) canonical() bool {
	return p != nil && p.self == p && p.ready != nil && p.auth != nil
}

// These wrappers preserve partial owners and native errors. A successful pair
// constructor alone does not perform either query or prepare a physical pipe.
func (p *serviceNativeFactories) prepareWitness(ctx context.Context) (*serviceready.ServiceWitness, error) {
	if !p.canonical() {
		return nil, ErrProvisioningUnconfigured
	}
	return p.ready.PrepareServiceWitness(ctx)
}
func (p *serviceNativeFactories) prepareEndpoint(ctx context.Context) (*serviceready.Endpoint, error) {
	if !p.canonical() {
		return nil, ErrProvisioningUnconfigured
	}
	return p.ready.PrepareEndpoint(ctx)
}
