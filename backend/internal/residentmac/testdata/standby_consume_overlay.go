// Compiler-overlay-only composition. Never included by go build ./... .
package residentmac

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

// Uses a real protected factory and actual connector; creates no receiver,
// signature, authorization marker, native Session or production entry point.
func ReplayStandby(factory *maclaunch.ResidentFactory, connector *agenttransport.ResidentClient) *Standby {
	s := newStandby()
	s.factory = &FixedFactory{native: factory}
	s.makeConnector = func(context.Context) (residentConnector, error) { return connector, nil }
	return s
}

func ReplayStandbyOriginal(s *Standby) (*FixedSource, *agenttransport.ResidentSourceLease, *workerowner.Owner, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cycle
	if c == nil {
		return nil, nil, nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.source, c.lease, c.owner, c.done
}
