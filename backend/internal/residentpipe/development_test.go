//go:build tether_development

package residentpipe

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDevelopmentLifetimeBound(t *testing.T) {
	before := time.Now()
	var held context.Context
	sentinel := errors.New("owned outcome")
	err := runDevelopmentLifetime(context.Background(), func(ctx context.Context) error {
		held = ctx
		d, ok := ctx.Deadline()
		if !ok || d.Before(before.Add(developmentLifetime)) || d.After(time.Now().Add(developmentLifetime)) {
			t.Fatal("deadline")
		}
		return sentinel
	})
	if err != sentinel || held.Err() != context.Canceled {
		t.Fatal("outcome/cancel")
	}
}
func TestDevelopmentLifetimeKeepsEarlierDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, _ := parent.Deadline()
	_ = runDevelopmentLifetime(parent, func(ctx context.Context) error {
		actual, _ := ctx.Deadline()
		if actual != d {
			t.Fatal("extended")
		}
		return nil
	})
}
func TestDevelopmentLifetimeRejectsInvalidParent(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	for _, p := range []context.Context{nil, dead} {
		if runDevelopmentLifetime(p, func(context.Context) error { t.Fatal("called"); return nil }) == nil {
			t.Fatal("accepted")
		}
	}
	if runDevelopmentLifetime(context.Background(), nil) == nil {
		t.Fatal("nil callback")
	}
}
func TestDevelopmentLifetimePropagatesCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	if err := runDevelopmentLifetime(parent, func(ctx context.Context) error { cancel(); <-ctx.Done(); return ctx.Err() }); err != context.Canceled {
		t.Fatal(err)
	}
}
