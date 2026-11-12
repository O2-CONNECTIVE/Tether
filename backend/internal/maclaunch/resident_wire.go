package maclaunch

import (
	"context"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
	"golang.org/x/crypto/ssh"
)

const ResidentFixedCommand = agentgateway.ResidentFixedCommand
const ResidentFixedRequestID = agentgateway.ResidentFixedID

// NewWireConnector uses only this loaded protected enrollment. It neither
// exports the signer nor accepts a UI identity/configuration override.
func (f *ResidentFactory) NewWireConnector(ctx context.Context) (*agenttransport.ResidentClient, error) {
	if !f.FixedWirePolicy() {
		return nil, ErrResident
	}
	if e := f.recheck(ctx); e != nil {
		return nil, e
	}
	b := agenttransport.DefaultBudgets()
	return agenttransport.NewResidentConnector(agenttransport.ResidentClientConfig{Endpoint: f.policy.binding.Endpoint, HostLabel: f.policy.binding.HostLabel, PrincipalLabel: f.policy.binding.PrincipalLabel, HostKey: &pinnedSigner{signer: f.signer, public: f.signer.PublicKey()}, PrincipalKey: f.policy.peer, Budgets: b}, 30*time.Second)
}

// FixedWirePolicy is a restrictive scope check, not execution authorization.
func (f *ResidentFactory) FixedWirePolicy() bool {
	if f == nil || f.self != f || f.blocked.Load() {
		return false
	}
	return fixedResidentPolicy(f.policy)
}

// OpenWireSource accepts the actual opaque authenticated receiver, never a
// caller-implemented metadata interface or an old-proposal proof translation.
// Native admission/issuer/runtime/cleanup ownership remains in this factory.
func (f *ResidentFactory) OpenWireSource(receiver *agenttransport.ResidentReceiver) (*ResidentSource, error) {
	if !f.matchesWire(receiver) {
		return nil, ErrResident
	}
	read, stop := context.WithTimeout(receiver.Context(), 10*time.Second)
	e := f.recheck(read)
	stop()
	if e != nil {
		return nil, e
	}
	if e := receiver.ClaimNativeSource(); e != nil {
		return nil, ErrResident
	}
	s, e := f.openSource(receiver.Context())
	if e != nil {
		return nil, e
	}
	s.wireReceiver = receiver
	return s, nil
}
func (f *ResidentFactory) matchesWire(r *agenttransport.ResidentReceiver) bool {
	if f == nil || f.self != f || f.blocked.Load() || r == nil || r.Context() == nil || r.Context().Err() != nil || r.Generation() == "" {
		return false
	}
	p := f.policy
	op, ok := p.operations[ResidentFixedRequestID]
	return len(p.operations) == 1 && ok && op.command == ResidentFixedCommand && op.timeout == 15000 && r.Endpoint() == p.binding.Endpoint && r.HostLabel() == p.binding.HostLabel && r.PrincipalLabel() == p.binding.PrincipalLabel && r.HostKeyFingerprint() == p.key.Fingerprint && r.PrincipalKeyFingerprint() == ssh.FingerprintSHA256(p.peer)
}

// NewWireRequest retains the actual received work context and monotonic deadline.
// Outer request correlation never becomes the canonical FixedStart request ID.
// It admits the fixed operation only, never a command run.
func (s *ResidentSource) NewWireRequest(i agenttransport.ResidentFixedIntent) (*workerowner.Request, error) {
	if s == nil || s.self != s || i.Operation() != ResidentFixedRequestID || i.TimeoutMillis() != 15000 {
		return nil, ErrResident
	}
	op := s.factory.policy.operations[ResidentFixedRequestID]
	return s.newWireRequest(i, agentexec.FixedStartConstraint{RequestID: ResidentFixedRequestID, Command: op.command, TimeoutMillis: op.timeout})
}

// NewWireRunRequest admits one signed command run. The protected policy names
// no command for it: the gateway's signature over the command hash, directory
// and timeout is the scope, and the worker is held to exactly those values.
// The caller must first check the local run allowance, which this package
// cannot see; that is why NewWireRequest never admits a command run.
func (s *ResidentSource) NewWireRunRequest(i agenttransport.ResidentFixedIntent) (*workerowner.Request, error) {
	if s == nil || s.self != s || i.Operation() != agenttransport.ResidentRunOperation || i.CommandSHA256() == "" || i.Cwd() == "" || i.TimeoutMillis() == 0 {
		return nil, ErrResident
	}
	return s.newWireRequest(i, agentexec.FixedStartConstraint{RequestID: agenttransport.ResidentRunOperation, CommandSHA256: i.CommandSHA256(), Dir: i.Cwd(), TimeoutMillis: i.TimeoutMillis()})
}

func (s *ResidentSource) newWireRequest(i agenttransport.ResidentFixedIntent, fixed agentexec.FixedStartConstraint) (*workerowner.Request, error) {
	if !finiteResident(i.OriginalContext()) || i.Receiver() == nil || i.Receiver() != s.wireReceiver || !s.factory.matchesWire(i.Receiver()) || i.HostKeyFingerprint() != s.factory.policy.key.Fingerprint || i.PrincipalKeyFingerprint() != ssh.FingerprintSHA256(s.factory.policy.peer) || i.Generation() != s.wireReceiver.Generation() || fixed.Validate() != nil {
		return nil, ErrResident
	}
	deadline := i.Deadline()
	original := i.OriginalContext()
	if d, _ := original.Deadline(); d.Before(deadline) {
		deadline = d
	}
	if !time.Now().Before(deadline) {
		return nil, ErrResident
	}
	id := i.RequestID()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.liveLocked() || s.entries[id] != nil || len(s.entries) >= maxResidentIntents {
		return nil, ErrResident
	}
	copy := i
	entry := &residentEntry{wireIntent: &copy}
	s.entries[id] = entry
	return &workerowner.Request{SourceContext: original, Deadline: deadline, Reply: make(chan error, 1), Approve: func(ctx context.Context) (*workerowner.Worker, error) {
		return s.approveWithID(ctx, original, entry, id, fixed, deadline)
	}}, nil
}
