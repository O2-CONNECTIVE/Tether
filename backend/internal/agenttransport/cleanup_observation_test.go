package agenttransport

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCleanupObservationDoesNotRetryOrReplaceOriginalError(t *testing.T) {
	original := errors.New("synthetic cleanup failure")
	o := &stubOwner{results: []error{original, nil}}
	c := &Client{owner: o, finished: make(chan struct{})}
	if _, ok := c.CleanupResult(); ok {
		t.Fatal("unattempted cleanup confirmed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	originalResult := c.Close(ctx)
	if !errors.Is(originalResult, original) {
		t.Fatal(originalResult)
	}
	for i := 0; i < 3; i++ {
		if err, ok := c.CleanupResult(); !ok || err != originalResult {
			t.Fatal(err, ok)
		}
	}
	// Explicit existing Close remains a separate operation, not an observation.
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err, ok := c.CleanupResult(); !ok || err != nil {
		t.Fatal(err, ok)
	}
}
func TestCleanupObservationRequiresRunJoin(t *testing.T) {
	c := &Client{ran: true, finished: make(chan struct{}), cleanupObserved: true}
	if _, ok := c.CleanupResult(); ok {
		t.Fatal("running Run treated as joined")
	}
	close(c.finished)
	if err, ok := c.CleanupResult(); !ok || err != nil {
		t.Fatal(err, ok)
	}
	c.cleanMu.Lock()
	if _, ok := c.CleanupResult(); ok {
		t.Fatal("inflight cleanup observed")
	}
	c.cleanMu.Unlock()
}
