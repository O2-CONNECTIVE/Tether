package agentwire_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

type contractScope func() (context.Context, context.CancelFunc)

// The original fixture's two-second limit applies to each independently
// asserted RPC, not to unrelated barriers or prior RPCs. Grant, process,
// status-loop and owner cleanup deadlines remain owned by their existing code.
func contractOperationScope() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func contractRPC[T any](scope contractScope, invoke func(context.Context) (T, error)) (T, error) {
	ctx, cancel := scope()
	defer cancel()
	return invoke(ctx)
}

func TestSharedContractCancelUsesIndependentOperationBudgets(t *testing.T) {
	for _, profile := range sharedProfiles {
		t.Run(agentwire.ProfileName(profile), func(t *testing.T) {
			var previous context.Context
			calls := 0
			scope := func() (context.Context, context.CancelFunc) {
				if previous != nil && !errors.Is(previous.Err(), context.Canceled) {
					t.Fatal("previous operation context was not canceled before the next operation")
				}
				before := time.Now()
				ctx, cancel := contractOperationScope()
				after := time.Now()
				deadline, bounded := ctx.Deadline()
				if !bounded || deadline.Before(before.Add(2*time.Second)) || deadline.After(after.Add(2*time.Second)) || ctx.Err() != nil || ctx == previous {
					cancel()
					t.Fatal("operation did not receive its own unchanged finite budget")
				}
				previous = ctx
				calls++
				return ctx, cancel
			}
			// Exercise the original full scenario and all its authority, replay,
			// cancellation, output and same-owner cleanup assertions. An ended
			// old scope cannot be accidentally reused by a subsequent assertion.
			contractCancelScenario(t, profile, scope)
			if calls != 6 {
				t.Fatalf("independent RPC scopes = %d, want 6", calls)
			}
			if previous == nil || !errors.Is(previous.Err(), context.Canceled) {
				t.Fatal("final operation context was not promptly canceled")
			}
		})
	}
}
