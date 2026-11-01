package mcpadapter_test

import (
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gateOutput struct {
	out                     io.Writer
	block                   atomic.Bool
	once                    sync.Once
	entered, release, ended chan struct{}
}

func (g *gateOutput) Write(p []byte) (int, error) {
	if !g.block.Load() {
		return g.out.Write(p)
	}
	g.once.Do(func() { close(g.entered) })
	<-g.release
	close(g.ended)
	return len(p), nil
}
func TestAsyncOutputFailureWakesServeAndReportsUnknown(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(map[bool]string{false: "writer deadline with live backend", true: "backend loss preserves unknown"}[loss], func(t *testing.T) {
			gate := &gateOutput{entered: make(chan struct{}), release: make(chan struct{}), ended: make(chan struct{})}
			h := newHarness(t, options{allowOutputUnknown: true, output: func(out io.Writer) io.Writer { gate.out = out; return gate }})
			h.initialize()
			st, _ := h.start("output-failure", "echo fake")
			id := commandID(t, st)
			gate.block.Store(true)
			h.send(map[string]any{"id": 9000, "method": "tools/call", "params": map[string]any{"name": "rescue_status", "arguments": map[string]any{"command_id": id}}})
			select {
			case <-gate.entered:
			case <-time.After(time.Second):
				t.Fatal("reply writer not reached")
			}
			defer func() { close(gate.release); <-gate.ended }()
			if loss {
				_ = h.client.Close()
			}
			select {
			case err := <-h.done:
				h.done <- err
				if !errors.Is(err, mcpadapter.ErrOutputUnconfirmed) {
					t.Fatal("missing output ownership error", err)
				}
				if loss && !errors.Is(err, mcpadapter.ErrChannelClosed) {
					t.Fatal("missing channel loss", err)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("async failed writer did not wake Serve")
			}
		})
	}
}
