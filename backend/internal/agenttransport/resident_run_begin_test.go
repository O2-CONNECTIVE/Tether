package agenttransport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// The gateway's work owner reaches the Mac only through this port. It must
// bind the command run to the generation the MCP session was created for, as
// the fixed routine does, and deliver the scope unchanged.
func TestBeginResidentRunDeliversTheScopeOnlyForTheCurrentGeneration(t *testing.T) {
	r := connectorRig(t)
	done := residentRun(r.c)
	s := takeSource(t, r)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	scope := agentgateway.ResidentRunScope{CommandSHA256: residentRunWireHash, Cwd: "src/app", TimeoutMillis: 60000}
	generation := residentGeneration(r.h)
	if e := r.h.BeginResidentRun(ctx, hostLabel, member1, strings.Repeat("b", 64), fmt.Sprintf("%032x", 1), residentRunWireDeadline(), scope); !errors.Is(e, agentgateway.ErrResidentWorkUnconfigured) {
		t.Fatal("command run admitted for another generation", e)
	}
	if e := r.h.BeginResidentRun(ctx, hostLabel, member1, generation, fmt.Sprintf("%032x", 2), residentRunWireDeadline(), agentgateway.ResidentRunScope{}); !errors.Is(e, ErrRefused) {
		t.Fatal("command run without a scope became another kind of work", e)
	}
	id := fmt.Sprintf("%032x", 3)
	result := make(chan error, 1)
	go func() {
		result <- r.h.BeginResidentRun(ctx, hostLabel, member1, generation, id, residentRunWireDeadline(), scope)
	}()
	i, e := s.Next()
	if e != nil {
		t.Fatal(e)
	}
	if i.RequestID() != id || i.Operation() != ResidentRunOperation || i.workBinding().Run != scope {
		t.Fatalf("the Mac received %s %s %+v", i.RequestID(), i.Operation(), i.workBinding().Run)
	}
	if e = s.Refuse(i); e != nil {
		t.Fatal(e)
	}
	// The gateway's work owner reads the same outcomes as for the fixed routine.
	if e = await(t, result, 3*time.Second); !errors.Is(e, agentgateway.ErrResidentWorkUnconfigured) {
		t.Fatal(e)
	}
	r.c.Stop()
	if e = finishSource(t, s, nil); e != nil {
		t.Fatal(e)
	}
	if e = await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
