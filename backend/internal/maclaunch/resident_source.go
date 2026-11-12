package maclaunch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"sort"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentwork"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
	"golang.org/x/crypto/ssh"
)

const maxResidentIntents = 32

type ResidentAdvertisement struct {
	Binding                   residentwork.Binding
	SourceNonce, PolicySHA256 [32]byte
	HostFingerprint           string
	Operations                []residentwork.Operation
}
type residentEntry struct {
	started, done bool
	worker        *ResidentWorker
	result        error
	wireIntent    *agenttransport.ResidentFixedIntent
}

// ResidentSource is one held standby/control generation, not a granted Session.
// Context/advertisement alone proves no remote identity; each work intent must
// carry the enrolled principal's signature over this source's fresh nonces.
type ResidentSource struct {
	self                               *ResidentSource
	mu                                 sync.Mutex
	factory                            *ResidentFactory
	ctx                                context.Context
	cancel                             context.CancelFunc
	nonce, server                      [32]byte
	proofAttempted, proofReady, closed bool
	entries                            map[string]*residentEntry
	wireReceiver                       *agenttransport.ResidentReceiver
}

func (f *ResidentFactory) openSource(host context.Context) (*ResidentSource, error) {
	if f == nil || f.self != f || f.blocked.Load() || host == nil || host.Err() != nil {
		return nil, ErrResident
	}
	ctx, cancel := context.WithCancel(host)
	s := &ResidentSource{factory: f, ctx: ctx, cancel: cancel, entries: make(map[string]*residentEntry)}
	s.self = s
	if _, e := rand.Read(s.nonce[:]); e != nil || s.nonce == ([32]byte{}) {
		cancel()
		return nil, ErrResident
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-f.stopped:
		}
		s.Close()
	}()
	return s, nil
}
func (s *ResidentSource) Context() context.Context {
	if s == nil || s.self != s {
		return nil
	}
	return s.ctx
}
func (s *ResidentSource) Advertisement() ResidentAdvertisement {
	if s == nil || s.self != s {
		return ResidentAdvertisement{}
	}
	f := s.factory
	a := ResidentAdvertisement{Binding: f.policy.binding, SourceNonce: s.nonce, PolicySHA256: f.digest, HostFingerprint: f.policy.key.Fingerprint}
	for _, o := range f.policy.operations {
		a.Operations = append(a.Operations, residentwork.Operation{ID: o.id, CommandSHA256: sha256.Sum256([]byte(o.command)), TimeoutMillis: o.timeout})
	}
	sort.Slice(a.Operations, func(i, j int) bool { return a.Operations[i].ID < a.Operations[j].ID })
	return a
}
func (s *ResidentSource) liveLocked() bool {
	return s != nil && s.self == s && !s.closed && !s.factory.blocked.Load() && s.ctx.Err() == nil
}

// HostProof signs only the fixed control-host domain. There is one attempt per
// source, no caller-provided signing bytes or reusable approval result.
func (s *ResidentSource) hostProof(server [32]byte) ([]byte, error) {
	if s == nil || s.self != s {
		return nil, ErrResident
	}
	s.mu.Lock()
	if !s.liveLocked() || s.proofAttempted {
		s.mu.Unlock()
		return nil, ErrResident
	}
	s.proofAttempted = true
	s.server = server
	f := s.factory
	data, e := residentwork.HostProofTranscript(f.policy.binding, s.nonce, server, f.digest)
	s.mu.Unlock()
	if e != nil {
		return nil, ErrResident
	}
	read, stop := context.WithTimeout(s.ctx, 10*time.Second)
	err := f.recheck(read)
	stop()
	if err != nil {
		return nil, err
	}
	signer := &pinnedSigner{signer: f.signer, public: f.signer.PublicKey()}
	sig, e := signer.Sign(rand.Reader, data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e != nil || sig == nil || sig.Format != ssh.KeyAlgoED25519 || len(sig.Blob) != 64 || !s.liveLocked() {
		return nil, ErrResident
	}
	s.proofReady = true
	return append([]byte(nil), sig.Blob...), nil
}
func (s *ResidentSource) newRequest(original context.Context, i residentwork.Intent) (*workerowner.Request, error) {
	if s == nil || s.self != s || !finiteResident(original) || !residentwork.ValidRequestID(i.RequestID) || !residentwork.ValidOperationID(i.OperationID) {
		return nil, ErrResident
	}
	f := s.factory
	s.mu.Lock()
	valid := s.liveLocked() && s.proofReady && i.SourceNonce == s.nonce && i.ServerNonce == s.server && i.PolicySHA256 == f.digest
	_, replay := s.entries[i.RequestID]
	capacity := len(s.entries) < maxResidentIntents
	s.mu.Unlock()
	if !valid || replay || !capacity || len(i.Signature) != 64 {
		return nil, ErrResident
	}
	data, e := residentwork.IntentTranscript(f.policy.binding, i)
	if e != nil || f.policy.peer.Verify(data, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: i.Signature}) != nil {
		return nil, ErrResident
	}
	op, ok := f.policy.operations[i.OperationID]
	now := time.Now()
	deadline := time.UnixMilli(i.ExpiresUnixMilli)
	if !ok || !deadline.After(now) || deadline.Sub(now) > time.Duration(op.timeout)*time.Millisecond {
		return nil, ErrResident
	}
	// Preserve receipt-time elapsed lifetime before any queued admission. Unix
	// expiry alone is wall-only and would be renewed by a wall-clock rollback.
	deadline = now.Add(deadline.Sub(now))
	if d, _ := original.Deadline(); d.Before(deadline) {
		deadline = d
	}
	if !deadline.After(now) {
		return nil, ErrResident
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.liveLocked() || s.entries[i.RequestID] != nil || len(s.entries) >= maxResidentIntents {
		return nil, ErrResident
	}
	entry := &residentEntry{}
	s.entries[i.RequestID] = entry
	return &workerowner.Request{SourceContext: original, Deadline: deadline, Reply: make(chan error, 1), Approve: func(ctx context.Context) (*workerowner.Worker, error) {
		return s.approve(ctx, original, entry, i.RequestID, op, deadline)
	}}, nil
}
func (s *ResidentSource) approve(ctx, original context.Context, e *residentEntry, id string, op residentOperation, deadline time.Time) (*workerowner.Worker, error) {
	return s.approveWithID(ctx, original, e, id, agentexec.FixedStartConstraint{RequestID: id, Command: op.command, TimeoutMillis: op.timeout}, deadline)
}

// approveWithID starts the one worker of entry id. fixed is the whole admission
// scope of that worker; its request ID is canonical and never the outer id.
func (s *ResidentSource) approveWithID(ctx, original context.Context, e *residentEntry, id string, fixed agentexec.FixedStartConstraint, deadline time.Time) (owned *workerowner.Worker, result error) {
	s.mu.Lock()
	if !s.liveLocked() || e.started || s.entries[id] != e {
		s.mu.Unlock()
		return nil, ErrResident
	}
	e.started = true
	w := newResidentWorker()
	e.worker = w
	s.mu.Unlock()
	owned = &workerowner.Worker{Session: w}
	defer func() {
		if recover() != nil {
			result = ErrResidentUnknown
			s.factory.block()
			w.markUncertain(result)
			w.revoke()
		}
		s.mu.Lock()
		e.done = true
		e.result = result
		s.mu.Unlock()
	}()
	if !finiteResident(ctx) || !finiteResident(original) {
		return owned, ErrResident
	}
	if d, _ := ctx.Deadline(); d.Before(deadline) {
		deadline = d
	}
	operation, cancel := context.WithDeadline(original, deadline)
	stopCaller := context.AfterFunc(ctx, cancel)
	stopSource := context.AfterFunc(s.ctx, cancel)
	w.attachLifetime(operation, cancel, func() { stopCaller(); stopSource() })
	ctx = operation
	f := s.factory
	if err := f.recheck(ctx); err != nil {
		w.markUncertain(err)
		return owned, err
	}
	runtime, err := f.deps.runtime(f.policy.runtime)
	if err != nil {
		return owned, ErrResident
	}
	b := f.deps.budgets
	b.Cleanup = f.policy.cleanup
	p, err := prepare(Config{HostKey: f.signer, AllowedKeys: []ssh.PublicKey{f.policy.peer}, Runtime: f.policy.runtime, Deadline: deadline, CleanupTimeout: f.policy.cleanup, Budgets: b, FixedStart: &fixed}, runtime)
	if err != nil {
		return owned, ErrResident
	}
	w.attachIssuer(p.issuer)
	s.mu.Lock()
	live := s.liveLocked()
	s.mu.Unlock()
	if err := f.recheck(ctx); err != nil {
		w.markUncertain(err)
		w.revoke()
		return owned, err
	}
	if !live || w.isRevoked() || p.take() != nil {
		w.revoke()
		return owned, ErrResident
	}
	a, err := p.completeApproved(ctx)
	w.attachApproved(a)
	if err != nil {
		w.revoke()
		return owned, ErrResident
	}
	c, err := f.deps.client(ResidentClientConfig{Binding: f.policy.binding, HostKey: f.signer, Cleanup: f.policy.cleanup, WireIntent: e.wireIntent}, a)
	w.attachClient(c)
	if err != nil || c == nil {
		w.revoke()
		return owned, ErrResident
	}
	s.mu.Lock()
	live = s.liveLocked()
	s.mu.Unlock()
	if err := f.recheck(ctx); err != nil {
		w.markUncertain(err)
		w.revoke()
		return owned, err
	}
	if !live || w.isRevoked() {
		w.revoke()
		return owned, ErrResident
	}
	if !w.ready() || original.Err() != nil || ctx.Err() != nil {
		w.revoke()
		return owned, ErrResident
	}
	return owned, nil
}

// Worker returns the SAME held capsule, including a partial capsule with error.
// Its Dispatch stays empty until successful admission and after any revocation.
func (s *ResidentSource) Worker(id string) (*ResidentWorker, error) {
	if s == nil || s.self != s {
		return nil, ErrResident
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil {
		return nil, ErrResident
	}
	if !e.done {
		return nil, ErrResidentPending
	}
	return e.worker, e.result
}

// Close seals new scope issuance, revokes all issued work, then cancels source.
// Collection remains with the exact workerowner Owner; no cleanup/retry here.
func (s *ResidentSource) Close() {
	if s == nil || s.self != s {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	var held []*ResidentWorker
	for _, e := range s.entries {
		if e.worker != nil {
			held = append(held, e.worker)
		}
	}
	s.mu.Unlock()
	for _, w := range held {
		w.revoke()
	}
	s.cancel()
}
