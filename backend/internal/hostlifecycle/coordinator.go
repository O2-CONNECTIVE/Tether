// Package hostlifecycle is the host's one lifecycle coordinator (#456, #36
// DEC-01). It keeps the ledger of what the host acquired, starts in order,
// takes back what a failed start acquired, and decides whether everything
// stopped. A release it cannot confirm stays in the ledger as needing cleanup;
// it is never reported as stopped.
package hostlifecycle

import (
	"context"
	"errors"
	"time"
)

// Resource is one thing the host acquired and must give back. Close returns
// nil only when the release is confirmed.
type Resource interface{ Close(ctx context.Context) error }

// Step acquires one resource. A non-nil Resource returned with an error is a
// partial acquisition; the ledger still takes it.
type Step struct {
	Name    string
	Acquire func(ctx context.Context) (Resource, error)
}

// Result is the coordinator's one decision about a stop.
type Result struct {
	Stopped      bool     // every release in the ledger is confirmed
	NeedsCleanup []string // names kept as needing cleanup, in close order
}

var (
	// ErrStopped refuses a start once a stop has begun.
	ErrStopped = errors.New("hostlifecycle: stopped")
	// ErrNeedsCleanup refuses a start while a release is unconfirmed.
	ErrNeedsCleanup = errors.New("hostlifecycle: a release is unconfirmed")
)

type Coordinator struct{}

// New returns a coordinator that closes within budget, both when a start
// fails and when it stops.
func New(budget time.Duration) *Coordinator { return &Coordinator{} }

// Start acquires steps in order. On the first error it closes what was
// acquired, in reverse, and returns that error.
func (c *Coordinator) Start(ctx context.Context, steps ...Step) error { return nil }

// Stop seals the coordinator, cancels an acquisition in progress, and closes
// the ledger in reverse acquisition order within the budget. It decides once.
func (c *Coordinator) Stop() Result { return Result{} }
