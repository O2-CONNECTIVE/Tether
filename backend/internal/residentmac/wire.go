package residentmac

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

// FixedFactory is the native outer adapter for the actual resident_fixed wire.
// Only the fixed protected standby loader constructs it. No old residentwork
// signature is converted on this wire; this adapter creates no second Owner.
type FixedFactory struct{ native *maclaunch.ResidentFactory }
type FixedSource struct{ native *maclaunch.ResidentSource }

func (f *FixedFactory) OpenSource(receiver *agenttransport.ResidentReceiver) (*FixedSource, error) {
	if f == nil || f.native == nil {
		return nil, maclaunch.ErrResident
	}
	s, e := f.native.OpenWireSource(receiver)
	if e != nil {
		return nil, e
	}
	return &FixedSource{native: s}, nil
}
func (s *FixedSource) Context() context.Context {
	if s == nil || s.native == nil {
		return nil
	}
	return s.native.Context()
}
func (s *FixedSource) NewRequest(i agenttransport.ResidentFixedIntent) (*workerowner.Request, error) {
	if s == nil || s.native == nil {
		return nil, maclaunch.ErrResident
	}
	return s.native.NewWireRequest(i)
}

// NewRunRequest admits a command run. Only consume calls it, after it has
// checked the run allowance of the current standby cycle.
func (s *FixedSource) NewRunRequest(i agenttransport.ResidentFixedIntent) (*workerowner.Request, error) {
	if s == nil || s.native == nil {
		return nil, maclaunch.ErrResident
	}
	return s.native.NewWireRunRequest(i)
}
func (s *FixedSource) Worker(id string) (*maclaunch.ResidentWorker, error) {
	if s == nil || s.native == nil {
		return nil, maclaunch.ErrResident
	}
	return s.native.Worker(id)
}

// Close revokes the same native source; collection stays with the existing
// workerowner Owner/WaitSource and its original worker capsules.
func (s *FixedSource) Close() {
	if s != nil && s.native != nil {
		s.native.Close()
	}
}
