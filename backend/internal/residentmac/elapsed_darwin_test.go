//go:build darwin

package residentmac

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
)

type reconnectSignalConnector struct {
	mu       sync.Mutex
	snapshot agenttransport.ResidentControlSnapshot
	changes  chan struct{}
}

func (c *reconnectSignalConnector) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (c *reconnectSignalConnector) Accept(ctx context.Context) (*agenttransport.ResidentSourceLease, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*reconnectSignalConnector) Stop()                       {}
func (*reconnectSignalConnector) Close(context.Context) error { return nil }
func (c *reconnectSignalConnector) Snapshot() agenttransport.ResidentControlSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}
func (c *reconnectSignalConnector) Changes() <-chan struct{} { return c.changes }
func (c *reconnectSignalConnector) set(snapshot agenttransport.ResidentControlSnapshot) {
	c.mu.Lock()
	c.snapshot = snapshot
	c.mu.Unlock()
	select {
	case c.changes <- struct{}{}:
	default:
	}
}

func TestReconnectSignalRefingerprintsTheNewWaitAfterAddressCollection(t *testing.T) {
	connector := &reconnectSignalConnector{
		snapshot: agenttransport.ResidentControlSnapshot{RetryWaiting: true, RetryWaitID: 1},
		changes:  make(chan struct{}, 1),
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var addresses sync.Once
	var mu sync.Mutex
	currentAddress := "192.0.2.2"
	getAddresses := func() (string, error) {
		called := 0
		addresses.Do(func() { called = 1 })
		if called == 1 {
			return "192.0.2.1", nil
		}
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		mu.Lock()
		defer mu.Unlock()
		return currentAddress, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nudges := make(chan uint64, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		startReconnectSignalsUsing(ctx, connector, getAddresses, time.Millisecond, func(waitID uint64) {
			nudges <- waitID
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("wait A did not begin address observation")
	}
	connector.set(agenttransport.ResidentControlSnapshot{RetryWaiting: true, RetryWaitID: 2})
	close(release)
	select {
	case waitID := <-nudges:
		t.Fatalf("address observation crossing wait IDs woke wait %d", waitID)
	case <-time.After(20 * time.Millisecond):
	}
	mu.Lock()
	currentAddress = "192.0.2.3"
	mu.Unlock()
	select {
	case waitID := <-nudges:
		if waitID != 2 {
			t.Fatalf("address change after re-fingerprinting carried wait ID %d", waitID)
		}
	case <-time.After(time.Second):
		t.Fatal("new wait address change did not reach its tagged nudge")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconnect signal loop did not stop")
	}
}

func TestReconnectSignalRefingerprintsAfterInitialWaitIDChanges(t *testing.T) {
	const poll = 10 * time.Millisecond
	connector := &reconnectSignalConnector{
		snapshot: agenttransport.ResidentControlSnapshot{RetryWaiting: true, RetryWaitID: 1},
		changes:  make(chan struct{}, 1),
	}
	var mu sync.Mutex
	calls, network := 0, "192.0.2.2"
	rebased := make(chan struct{})
	getAddresses := func() (string, error) {
		mu.Lock()
		calls++
		call := calls
		current := network
		mu.Unlock()
		if call == 1 {
			connector.set(agenttransport.ResidentControlSnapshot{RetryWaiting: true, RetryWaitID: 2})
			return "192.0.2.1", nil
		}
		if call == 2 {
			close(rebased)
		}
		return current, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nudges := make(chan uint64, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		startReconnectSignalsUsing(ctx, connector, getAddresses, poll, func(waitID uint64) { nudges <- waitID })
	}()
	select {
	case <-rebased:
	case <-time.After(time.Second):
		t.Fatal("changed wait ID was not fingerprinted again")
	}
	select {
	case waitID := <-nudges:
		t.Fatalf("the initial fingerprint for wait A woke wait %d", waitID)
	case <-time.After(5 * poll):
	}
	mu.Lock()
	network = "192.0.2.3"
	mu.Unlock()
	select {
	case waitID := <-nudges:
		if waitID != 2 {
			t.Fatalf("network change was tagged with wait ID %d, want 2", waitID)
		}
	case <-time.After(time.Second):
		t.Fatal("network change did not nudge the current wait")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconnect signal loop did not stop")
	}
}
