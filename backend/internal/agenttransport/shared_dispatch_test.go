package agenttransport

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Existing real approval owner keeps its method contract; the common transport
// requires only channel metadata, never a Mac approval or Windows producer.
// Real approval-owner conformance is asserted in maclaunch tests to keep
// transport fixtures dependent only on the shared metadata port.

func TestSharedDispatchKeepsCanonicalRegistrationAndTeardown(t *testing.T) {
	// Reuse the existing memory-only canonical Session/Host/registration fixture.
	// It is not native Windows provenance, approval, or a new factory.
	p := newComposed(t, nil)
	var common agentchannel.Dispatch = p.owner.Dispatch()
	var gateway agentgateway.Dispatch = common
	if !sameDispatch(gateway, p.ownerDispatch()) {
		t.Fatal("shared metadata changed the approved dispatch")
	}
	done, ends := p.connected(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	p.ended(t, done, ends)
}
