package mcpadapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type stalledOutput struct {
	entered chan struct{}
	release chan struct{}
	ended   chan struct{}
	once    sync.Once
}

func (s *stalledOutput) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	close(s.ended)
	return len(p), nil
}
func TestBlockedOutputEndsOnChannelLossAndDoesNotWriteAgain(t *testing.T) {
	output := &stalledOutput{entered: make(chan struct{}), release: make(chan struct{}), ended: make(chan struct{})}
	lost := make(chan struct{})
	w := &writer{out: output, ctx: context.Background(), done: lost}
	sent := make(chan struct{})
	go func() { w.send(map[string]any{"jsonrpc": "2.0"}); close(sent) }()
	<-output.entered
	close(lost)
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("output prevented channel-loss shutdown")
	}
	if !errors.Is(w.failed(), ErrOutputUnconfirmed) || !errors.Is(w.failed(), ErrChannelClosed) {
		t.Fatal(w.failed())
	}
	w.send(map[string]any{"must": "not start another write"})
	close(output.release)
	<-output.ended
}
func TestBlockedOutputHasFiniteWriteBudget(t *testing.T) {
	output := &stalledOutput{entered: make(chan struct{}), release: make(chan struct{}), ended: make(chan struct{})}
	w := &writer{out: output, ctx: context.Background(), timeout: time.Millisecond}
	w.send(map[string]any{"jsonrpc": "2.0"})
	if !errors.Is(w.failed(), ErrOutputUnconfirmed) {
		t.Fatal(w.failed())
	}
	close(output.release)
	<-output.ended
}
