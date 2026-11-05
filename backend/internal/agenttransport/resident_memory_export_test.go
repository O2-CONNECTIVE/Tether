package agenttransport

import (
	"net/http"
	"testing"
	"time"
)

// These helpers exist only in the package's test variant. They replace observed
// I/O with the existing memory HTTP fixture; they cannot mint a receiver, intent,
// signature, enrollment, or authenticated flag.
type ExportResidentMemory struct{ memory *memTransport }

func ExportBindResidentMemory(c *ResidentClient, handler http.Handler) *ExportResidentMemory {
	m := newMemTransport(handler)
	c.transport = func(owner acquirer) http.RoundTripper {
		m.own, m.track = owner, owner.attach
		return m
	}
	c.retryDelay = time.Millisecond
	return &ExportResidentMemory{memory: m}
}

func ExportResidentTestBudgets() Budgets { return testBudgets() }

// Assert original owners closed both memory streams and the server handler
// returned. On failure, still release these same fixture streams before exit.
func (m *ExportResidentMemory) CheckStopped(t *testing.T) {
	t.Helper()
	if m.memory.trips.Load() == 0 {
		t.Error("the real memory HTTP handshake was not attempted")
	}
	for {
		select {
		case pair := <-m.memory.pairs:
			if !pair.client.isClosed() || !pair.server.isClosed() {
				t.Error("an original memory stream outlived its owner")
			}
			_ = pair.client.Close()
			_ = pair.server.Close()
			select {
			case <-pair.served:
			case <-time.After(time.Second):
				t.Error("the same memory server handler did not finish")
			}
		default:
			return
		}
	}
}
