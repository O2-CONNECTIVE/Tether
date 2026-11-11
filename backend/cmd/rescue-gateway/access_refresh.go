package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// startAccessRefresh is the sole scheduler for the explicit file-mode opt-in.
// The first bounded fetch must finish before listen. Later attempts are serial,
// with a full MinInterval after completion (not a ticker with queued ticks).
// No request, assertion, kid or failure can accelerate the next attempt.
func startAccessRefresh(parent context.Context, cfg agentgateway.FileConfig, verifier *agentgateway.AccessVerifier, diagnostic io.Writer, d deps) (func() error, error) {
	if cfg.AccessKeySource == "" {
		return func() error { return nil }, nil
	}
	if parent == nil || parent.Err() != nil || verifier == nil || d.refreshAccessKeys == nil || d.waitAccessRefresh == nil {
		return nil, agentgateway.ErrAccessKeyRefresh
	}
	policy := agentgateway.ConfiguredAccessKeyRefreshPolicy()
	ctx, cancel := context.WithCancel(parent)
	refresh := func() error {
		bounded, stop := context.WithTimeout(ctx, policy.Timeout)
		defer stop()
		err := d.refreshAccessKeys(bounded, verifier)
		if err != nil || bounded.Err() != nil {
			return agentgateway.ErrAccessKeyRefresh
		}
		return nil
	}
	if err := refresh(); err != nil {
		cancel()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for d.waitAccessRefresh(ctx, policy.MinInterval) && ctx.Err() == nil {
			if err := refresh(); err != nil && ctx.Err() == nil {
				report(diagnostic, "Access公開鍵を更新できませんでした。既存の有効期限は延長しません")
			}
		}
	}()
	return func() error { return joinAccessRefresh(cancel, done, 2*shutdownBudget) }, nil
}

// The native fetch honors cancellation and its 5s total timeout. Join also
// has its own finite bound; an unknown end is never reported as success.
func joinAccessRefresh(cancel context.CancelFunc, done <-chan struct{}, budget time.Duration) error {
	cancel()
	join := time.NewTimer(budget)
	defer join.Stop()
	select {
	case <-done:
		return nil
	case <-join.C:
		return errors.New("Access refresh shutdown could not be confirmed")
	}
}

func waitAccessRefresh(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

// The owner can report a refresh failure while serve is shutting down. One
// wrapper serializes those fixed diagnostics, including non-thread-safe sinks.
type lockedDiagnostic struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *lockedDiagnostic) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}

func lockDiagnostic(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &lockedDiagnostic{out: w}
}
