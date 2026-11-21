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

func TestReconnectSignalForWaitAIsTaggedBeforeAddressChangeAndCannotWakeWaitB(t *testing.T) {
	connector := &reconnectSignalConnector{
		snapshot: agenttransport.ResidentControlSnapshot{RetryWaiting: true, RetryWaitID: 1},
		changes:  make(chan struct{}, 1),
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var addresses sync.Once
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
		return "192.0.2.2", nil
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
		if waitID != 1 {
			t.Fatalf("address observation for wait A carried wait ID %d", waitID)
		}
	case <-time.After(time.Second):
		t.Fatal("wait A address change did not reach its tagged nudge")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconnect signal loop did not stop")
	}
}
