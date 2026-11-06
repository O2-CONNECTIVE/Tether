package agenttransport

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ResidentSourceLease is exclusive source ownership, not an authority/grant.
// Only Accept returns it, after the actual observed transport and full mutual
// handshake. The connector never reads fixed frames after handing it off.
type ResidentSourceLease struct {
	self                *ResidentSourceLease
	receiver            *ResidentReceiver
	link                *closer
	mu                  sync.Mutex
	reading             chan struct{}
	finishing, finished bool
	result              error
	readErr             error // original source end cause; never a cleanup result
	done                chan struct{}
	replies             map[string]bool
	collections         map[string]bool
}

// NewResidentConnector opts into EXCLUSIVE owning handoff. The existing
// NewResidentClient stays connection-only/refuse-by-default. No callback or
// caller-supplied verified flag can mint a source/intent.
func NewResidentConnector(cfg ResidentClientConfig, consumerCleanup time.Duration) (*ResidentClient, error) {
	if consumerCleanup < cfg.Budgets.Cleanup || consumerCleanup > time.Minute {
		return nil, ErrConfig
	}
	c, e := NewResidentClient(cfg)
	if e != nil {
		return nil, e
	}
	c.sources = make(chan *ResidentSourceLease)
	c.consumerBudget = consumerCleanup
	return c, nil
}

// Accept transfers one source to the single consumer. It starts no connection.
// A concurrent Accept or a second Accept before Finish is refused.
func (c *ResidentClient) Accept(ctx context.Context) (*ResidentSourceLease, error) {
	if c == nil || ctx == nil || c.sources == nil {
		return nil, ErrConfig
	}
	c.mu.Lock()
	if c.accepting || (c.held != nil && !c.held.completed()) {
		c.mu.Unlock()
		return nil, ErrUsed
	}
	c.accepting = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.accepting = false; c.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	case lease := <-c.sources:
		c.mu.Lock()
		c.held = lease
		c.mu.Unlock()
		return lease, nil
	}
}

// Stop seals reconnect immediately; Close additionally observes the original
// source/transport completion. Neither is native work collection by itself.
func (c *ResidentClient) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.started {
		c.started, c.finished, c.cleanupObserved = true, true, true
		c.changedLocked()
		close(c.done)
		c.mu.Unlock()
		return
	}
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *ResidentSourceLease) completed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.finished }
func (s *ResidentSourceLease) Context() context.Context {
	if s == nil || s.self != s {
		return nil
	}
	return s.receiver.Context()
}
func (s *ResidentSourceLease) Receiver() *ResidentReceiver {
	if s == nil || s.self != s {
		return nil
	}
	return s.receiver
}

// Next is the only public fixed-frame reader of a handed-off source.
func (s *ResidentSourceLease) Next() (ResidentFixedIntent, error) {
	if s == nil || s.self != s {
		return ResidentFixedIntent{}, ErrConfig
	}
	s.mu.Lock()
	if s.finishing || s.reading != nil {
		s.mu.Unlock()
		return ResidentFixedIntent{}, ErrUsed
	}
	read := make(chan struct{})
	s.reading = read
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.reading = nil; close(read); s.mu.Unlock() }()
	i, err := s.receiver.readFixed()
	if err != nil {
		s.mu.Lock()
		if s.readErr == nil {
			s.readErr = err
		}
		s.mu.Unlock()
	}
	return i, err
}

// Refuse is the unchanged fail-closed response and ends only that work lease.
func (s *ResidentSourceLease) Refuse(i ResidentFixedIntent) error { return s.reply(i, false) }

// Pending reports only common-owner request admission, NOT native approval,
// command start/result or OFF completion. Call only after actual admission.
func (s *ResidentSourceLease) Pending(i ResidentFixedIntent) error { return s.reply(i, true) }
func (s *ResidentSourceLease) reply(i ResidentFixedIntent, pending bool) error {
	if s == nil || s.self != s || i.Receiver() != s.receiver || i.OriginalContext() == nil || i.OriginalContext().Err() != nil {
		return ErrRefused
	}
	s.mu.Lock()
	if s.finishing || s.replies[i.RequestID()] {
		s.mu.Unlock()
		return ErrRefused
	}
	s.replies[i.RequestID()] = true
	s.mu.Unlock()
	var msg []byte
	var e error
	if pending {
		msg, e = residentEncodePending(s.receiver.binding, i, s.receiver.cfg.HostKey)
	} else {
		msg, e = residentEncodeRefusal(s.receiver.binding, i, s.receiver.cfg.HostKey)
	}
	if e == nil {
		e = residentWrite(i.OriginalContext(), s.receiver.ws, msg)
	}
	if !pending || e != nil {
		s.receiver.Release(i)
	}
	return e
}

// ReleaseWork observes that the consumer no longer owns that work scope. It
// does not close a native worker; the consumer retains/collects that same owner.
func (s *ResidentSourceLease) ReleaseWork(i ResidentFixedIntent) {
	if s != nil && s.self == s {
		s.receiver.Release(i)
	}
}

// Finish is single-use original-result publication, not a cleanup retry. The
// consumer must first revoke/collect its SAME native/common owners. This joins
// any current Next, cancels the exact source and returns its original result.
func (s *ResidentSourceLease) Finish(ctx context.Context, consumerResult error) error {
	if s == nil || s.self != s || ctx == nil {
		return ErrConfig
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrConfig
	}
	s.mu.Lock()
	if s.finishing {
		done := s.done
		s.mu.Unlock()
		if e := waitFor(ctx, done); e != nil {
			return e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.result
	}
	s.finishing = true
	read := s.reading
	s.mu.Unlock()
	s.receiver.Close()
	s.link.start()
	e := consumerResult
	if read != nil {
		e = errors.Join(e, waitFor(ctx, read))
	}
	if e != nil {
		e = errors.Join(ErrCleanupUnknown, e)
	}
	s.mu.Lock()
	s.result = e
	s.finished = true
	close(s.done)
	s.mu.Unlock()
	return e
}
func (c *ResidentClient) handoff(ctx context.Context, r *ResidentReceiver, link *closer) error {
	lease := &ResidentSourceLease{receiver: r, link: link, done: make(chan struct{}), replies: map[string]bool{}, collections: map[string]bool{}}
	lease.self = lease
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.sources <- lease:
	}
	// After transfer, cancellation is never assumed to mean consumer collection.
	select {
	case <-lease.done:
	case <-ctx.Done():
		cleanup, stop := context.WithTimeout(context.Background(), c.consumerBudget)
		e := waitFor(cleanup, lease.done)
		stop()
		if e != nil {
			return errors.Join(ErrCleanupUnknown, e)
		}
	case <-r.Context().Done():
		cleanup, stop := context.WithTimeout(context.Background(), c.consumerBudget)
		e := waitFor(cleanup, lease.done)
		stop()
		if e != nil {
			return errors.Join(ErrCleanupUnknown, e)
		}
	}
	lease.mu.Lock()
	e := lease.result
	cause := lease.readErr
	lease.mu.Unlock()
	if e != nil {
		return e
	}
	if cause != nil {
		return cause
	}
	return ErrClosed
}
