package agenttransport

import (
	"context"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"sync"
	"sync/atomic"
	"time"
)

// ResidentReceiver is the actual mutually authenticated message receiver. It
// owns source cancellation, not the raw transport. The caller retains/closes
// that original transport. There is no field-only/boolean constructor for an
// authenticated intent; the constructor performs the complete fresh handshake.
type ResidentReceiver struct {
	self                                  *ResidentReceiver
	ctx                                   context.Context
	cancel                                context.CancelFunc
	binding                               residentBinding
	cfg                                   ResidentClientConfig
	ws                                    *websocket.Conn
	hostFingerprint, principalFingerprint string
	next                                  uint64
	seen                                  map[string]bool
	workMu                                sync.Mutex
	work                                  map[string]context.CancelFunc
	nativeClaimed                         atomic.Bool
}

// ClaimNativeSource irreversibly reserves this already-authenticated receiver
// for one native source, across factories and after source closure. It creates
// no authentication, grant, execution, or cleanup proof. Failed construction
// after a successful claim must not replace or reset this original source.
func (r *ResidentReceiver) ClaimNativeSource() error {
	if r == nil || r.self != r || r.ctx.Err() != nil {
		return ErrClosed
	}
	if !r.nativeClaimed.CompareAndSwap(false, true) {
		return ErrUsed
	}
	return nil
}

func newResidentReceiver(source context.Context, cfg ResidentClientConfig, ws *websocket.Conn) (*ResidentReceiver, error) {
	if source == nil || source.Err() != nil || ws == nil {
		return nil, ErrConfig
	}
	validator, e := NewResidentClient(cfg)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(source)
	auth, stop := context.WithTimeout(ctx, cfg.Budgets.Registration)
	b, e := validator.authenticate(auth, ws)
	stop()
	if e != nil {
		cancel()
		return nil, e
	}
	host, e := rawEd25519(b.hostKey)
	if e != nil {
		cancel()
		return nil, e
	}
	principal, e := rawEd25519(b.principalKey)
	if e != nil {
		cancel()
		return nil, e
	}
	r := &ResidentReceiver{ctx: ctx, cancel: cancel, binding: b, cfg: validator.cfg, ws: ws, hostFingerprint: ssh.FingerprintSHA256(host), principalFingerprint: ssh.FingerprintSHA256(principal), next: 1, seen: map[string]bool{}, work: map[string]context.CancelFunc{}}
	r.self = r
	return r, nil
}
func (r *ResidentReceiver) Context() context.Context {
	if r == nil || r.self != r {
		return nil
	}
	return r.ctx
}
func (r *ResidentReceiver) Generation() string {
	if r == nil || r.self != r {
		return ""
	}
	return r.binding.generation
}
func (r *ResidentReceiver) Endpoint() string {
	if r == nil || r.self != r {
		return ""
	}
	return r.binding.endpoint
}
func (r *ResidentReceiver) HostLabel() string {
	if r == nil || r.self != r {
		return ""
	}
	return r.binding.host
}
func (r *ResidentReceiver) PrincipalLabel() string {
	if r == nil || r.self != r {
		return ""
	}
	return r.binding.principal
}
func (r *ResidentReceiver) HostKeyFingerprint() string {
	if r == nil || r.self != r {
		return ""
	}
	return r.hostFingerprint
}
func (r *ResidentReceiver) PrincipalKeyFingerprint() string {
	if r == nil || r.self != r {
		return ""
	}
	return r.principalFingerprint
}

// ReadFixed has one reader, as required by WebSocket. It verifies signature,
// sequence, generation, replay and the fixed or command-run scope before
// minting native bookkeeping.
func (r *ResidentReceiver) readFixed() (ResidentIntent, error) {
	if r == nil || r.self != r || r.ctx.Err() != nil {
		return nil, ErrClosed
	}
	msg, e := residentRead(r.ctx, r.ws)
	if e != nil {
		r.Close()
		return nil, e
	}
	now := time.Now()
	i, e := residentDecodeIntent(r.binding, msg, r.cfg.PrincipalKey, now, r.next, r.seen)
	var intent ResidentIntent
	if e == nil {
		intent = i
	}
	if e != nil {
		// A command-run intent shares this reader's sequence and replay set.
		var run ResidentRunIntent
		run, e = residentDecodeRun(r.binding, msg, r.cfg.PrincipalKey, now, r.next, r.seen)
		if e == nil {
			intent = run
		}
	}
	if e != nil {
		r.Close()
		return nil, e
	}
	c := intent.Correlation()
	// Signed Unix serialization stays unchanged; the native deadline retains
	// receipt-time monotonic elapsed lifetime and is never reconstructed later.
	c.deadline = now.Add(c.deadline.Sub(now))
	original, cancel := context.WithDeadline(r.ctx, c.deadline)
	if original.Err() != nil {
		cancel()
		r.Close()
		return nil, ErrClosed
	}
	c.original = original
	c.receiver = r
	c.hostFingerprint = r.hostFingerprint
	c.principalFingerprint = r.principalFingerprint
	switch v := intent.(type) {
	case ResidentFixedIntent:
		v.residentCorrelation = c
		intent = v
	case ResidentRunIntent:
		v.residentCorrelation = c
		intent = v
	}
	r.workMu.Lock()
	if r.ctx.Err() != nil {
		r.workMu.Unlock()
		cancel()
		return nil, ErrClosed
	}
	r.work[c.requestID] = cancel
	r.workMu.Unlock()
	r.seen[c.requestID] = true
	r.next++
	return intent, nil
}

// Release ends only this received work lease, not the control source.
func (r *ResidentReceiver) Release(c ResidentCorrelation) {
	if c.receiver != r {
		return
	}
	r.release(c.requestID)
}
func (r *ResidentReceiver) release(id string) {
	if r == nil || r.self != r {
		return
	}
	r.workMu.Lock()
	cancel := r.work[id]
	delete(r.work, id)
	r.workMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close cancels the same source/work leases. Native OFF must revoke its actual
// work owner first; this signal alone is not an atomic authority barrier.
func (r *ResidentReceiver) Close() {
	if r == nil || r.self != r {
		return
	}
	r.cancel()
	r.workMu.Lock()
	for id, cancel := range r.work {
		cancel()
		delete(r.work, id)
	}
	r.workMu.Unlock()
}
