package agenttransport

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func TestResident32IntentDrainRotatesOnlyExactLastCollectedRequest(t *testing.T) {
	r := newResidentRig(t, nil) // real mutual proof; connection-only host signs refusals
	done := residentRun(r.c)
	residentReady(t, r)
	first := residentGeneration(r.h)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	for n := 1; n <= 32; n++ {
		id := fmt.Sprintf("%032x", n)
		if e := r.h.BeginResidentFixed(ctx, hostLabel, member1, first, id, time.Now().Add(time.Second).Truncate(time.Millisecond)); !errors.Is(e, agentgateway.ErrResidentWorkUnconfigured) {
			t.Fatal(n, e)
		}
	}
	if gen := r.h.ResidentGeneration(hostLabel, member1); gen != "" {
		t.Fatal("full generation still admits", gen)
	}
	r.h.RotateResident(hostLabel, member1, first, fmt.Sprintf("%032x", 31))
	if !r.c.Connected() || residentGeneration(r.h) != first {
		t.Fatal("late31st completion closed32nd")
	}
	r.h.RotateResident(hostLabel, member1, first, fmt.Sprintf("%032x", 32))
	verticalEventually(t, func() bool {
		return r.c.Connected() && r.h.ResidentGeneration(hostLabel, member1) != "" && residentGeneration(r.h) != first
	})
	if r.rec.count.Load() != 0 || r.m.trips.Load() != 2 {
		t.Fatal("rotation resumed/acquired work", r.rec.count.Load(), r.m.trips.Load())
	}
	r.h.RotateResident(hostLabel, member1, first, fmt.Sprintf("%032x", 32))
	if r.h.ResidentGeneration(hostLabel, member1) == "" {
		t.Fatal("old callback closed replacement")
	}
	if e := r.c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	_ = await(t, done, time.Second)
}
