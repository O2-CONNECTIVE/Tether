package serviceprovision

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

// AgentPurposeDescriptor is an inert candidate for a future agent-specific local
// confirmation. It is not the SSH PreviewDescriptor, authentication or authority.
type AgentPurposeDescriptor struct {
	Purpose, PolicyDigest, HostFingerprint, LocalOperatorSID string
	ConfigDigest, KeySetDigest                               [32]byte
	OperatorKeys, OperatorFingerprints                       []string
	MaxCommands                                              uint32
	HardDeadline                                             time.Time
}

// AgentPurposeOwner owns only its purpose Root. Prepared and runtime guest
// configuration are borrowed and must outlive it on the original owner thread.
// Do not copy it. Closing or failing this owner can never create an approval.
type AgentPurposeOwner struct {
	self                       *AgentPurposeOwner
	prepared                   *Prepared
	guest                      *GuestConfiguration
	loaded                     *Loaded
	record                     agentPurposeRecord
	purposeDigest              [32]byte
	view                       AgentPurposeDescriptor
	loadedAt                   time.Time
	now                        func() time.Time
	mu                         sync.Mutex
	accepted, closed, poisoned atomic.Bool
}

func agentPurposeAnchor(base Anchor) (Anchor, error) {
	return guestAnchorPair(base, "agent-purpose-v1.json", "agent-purpose-v1.sha256")
}

// LoadAgentPurpose reads a fixed sibling through the existing concrete native
// Root, before a launch/image lease borrows the Prepared scope. No path, digest,
// key, boolean approval or request value can select this input at runtime.
func (p *Prepared) LoadAgentPurpose(ctx context.Context, guest *GuestConfiguration) (*AgentPurposeOwner, error) {
	if !agentPurposeParents(p, guest) || !rootLive(ctx) {
		return nil, ErrRootDenied
	}
	anchor, err := agentPurposeAnchor(p.loaded.anchor)
	if err != nil {
		return nil, err
	}
	root, err := NewNativeRoot(anchor)
	if err != nil {
		return nil, err
	}
	return p.loadAgentPurpose(ctx, guest, root)
}
func agentPurposeParents(p *Prepared, g *GuestConfiguration) bool {
	return p != nil && p.loaded != nil && p.scope != nil && p.accepted.Load() && !p.poisoned.Load() &&
		g != nil && g.valid() && g.prepared == p && g.runtimeGuest && g.accepted.Load() && !g.closed.Load() && !g.poisoned.Load()
}
func (p *Prepared) loadAgentPurpose(ctx context.Context, g *GuestConfiguration, root *Root) (owner *AgentPurposeOwner, result error) {
	if !agentPurposeParents(p, g) || !rootLive(ctx) || root == nil {
		return nil, ErrRootDenied
	}
	expected, err := agentPurposeAnchor(p.loaded.anchor)
	if err != nil || root.anchor != expected {
		return nil, ErrRootConfig
	}
	// No other parent gate is held while checking the original image scope.
	if err = p.scope.Recheck(ctx); err != nil {
		return nil, p.reject(err)
	}
	if _, err = g.Digest(ctx); err != nil {
		return nil, err
	}
	owner = &AgentPurposeOwner{prepared: p, guest: g, now: time.Now}
	owner.self = owner
	defer func() {
		if recover() != nil {
			result = owner.reject(ErrRootFatal)
		}
	}()
	owner.loaded, err = root.Open(ctx)
	if err != nil {
		return owner, owner.reject(err)
	}
	raw, err := owner.loaded.Record()
	if err != nil {
		return owner, owner.reject(err)
	}
	defer clear(raw)
	owner.record, err = decodeAgentPurpose(raw)
	if err != nil {
		return owner, owner.reject(err)
	}
	owner.purposeDigest = sha256.Sum256(raw)
	if err = owner.checkInputs(ctx); err != nil {
		return owner, owner.reject(err)
	}
	serviceData, err := p.loaded.Record()
	if err != nil {
		return owner, owner.reject(err)
	}
	defer clear(serviceData)
	serviceRecord, err := DecodeRecord(serviceData)
	if err != nil {
		return owner, owner.reject(err)
	}
	host, ok := serviceRecord.Profile.Keys.Catalog[serviceRecord.HostKeyRef]
	if !ok || host.Fingerprint == "" {
		return owner, owner.reject(ErrRecord)
	}
	// Bind exact retained bytes, not a re-marshaled form or a caller's digest.
	bundle := append([]byte("rescue-windows-agent-purpose-bundle/1\x00"), owner.record.serviceDigest[:]...)
	bundle = append(bundle, owner.record.runtimeGuestDigest[:]...)
	bundle = append(bundle, owner.purposeDigest[:]...)
	owner.loadedAt = time.Now() // preserves Go's local monotonic component
	owner.view = AgentPurposeDescriptor{
		Purpose: agentPurposeName, PolicyDigest: agentexec.FullPolicyDigest(),
		HostFingerprint: host.Fingerprint, LocalOperatorSID: serviceRecord.Profile.Keys.OperatorSID,
		ConfigDigest: sha256.Sum256(bundle), KeySetDigest: owner.record.keyDigest,
		OperatorKeys: append([]string(nil), owner.record.keys...), OperatorFingerprints: append([]string(nil), owner.record.fingerprints...),
		MaxCommands: 1, HardDeadline: owner.loadedAt.Add(owner.record.maximum),
	}
	if !rootLive(ctx) || !agentPurposeParents(p, g) {
		return owner, owner.reject(ErrRootDenied)
	}
	owner.accepted.Store(true)
	return owner, nil
}
func (o *AgentPurposeOwner) reject(err error) error {
	o.accepted.Store(false)
	if errors.Is(err, ErrRootFatal) {
		o.poisoned.Store(true)
	}
	if o.poisoned.Load() {
		return errors.Join(ErrRootFatal, err)
	}
	return err
}

// checkInputs holds only this owner's gate. Parent and Root methods acquire and
// release their own gates sequentially, never recursively. Rechecks inspect held
// native identities/metadata and retained bytes; they are not a new disk read.
func (o *AgentPurposeOwner) checkInputs(ctx context.Context) error {
	if !agentPurposeParents(o.prepared, o.guest) || o.loaded == nil || !rootLive(ctx) {
		return ErrRootDenied
	}
	p, g := o.prepared, o.guest
	runtimeDigest, err := g.Digest(ctx)
	if err != nil {
		return err
	}
	if runtimeDigest != o.record.runtimeGuestDigest {
		return ErrRecord
	}
	serviceData, err := p.loaded.Record()
	if err != nil {
		return err
	}
	serviceDigest := sha256.Sum256(serviceData)
	clear(serviceData)
	if serviceDigest != o.record.serviceDigest {
		return ErrRecord
	}
	if err = guestRecheckRoot(ctx, o.loaded); err != nil {
		return err
	}
	raw, err := o.loaded.Record()
	if err != nil {
		return err
	}
	actual := sha256.Sum256(raw)
	clear(raw)
	if actual != o.purposeDigest {
		return ErrRecord
	}
	subject, err := p.loaded.Subject()
	if err != nil {
		return err
	}
	for _, loaded := range []*Loaded{g.loaded, o.loaded} {
		current, err := loaded.Subject()
		if err != nil {
			return err
		}
		if current != subject {
			return ErrRecord
		}
	}
	layout, err := g.Layout(ctx)
	if err != nil {
		return err
	}
	leaves := 0
	for _, held := range o.loaded.held {
		if held.leaf {
			leaves++
			if !layout.IncludesConfiguration(held.path, held.before.volume, held.before.id) {
				return ErrRecord
			}
		}
	}
	if leaves != 2 || !rootLive(ctx) {
		return ErrRecord
	}
	return nil
}

// Descriptor is a defensive copy of the same pre-launch candidate. Expiry,
// closure, failed validation and changed inputs cannot refresh its lifetime.
func (o *AgentPurposeOwner) Descriptor(ctx context.Context) (AgentPurposeDescriptor, error) {
	reject := func(err error) (AgentPurposeDescriptor, error) { return AgentPurposeDescriptor{}, o.reject(err) }
	if o == nil || o.self != o {
		return AgentPurposeDescriptor{}, ErrRootDenied
	}
	if o.poisoned.Load() {
		return reject(ErrRootFatal)
	}
	if !o.accepted.Load() || o.closed.Load() || !rootLive(ctx) || !o.mu.TryLock() {
		return reject(ErrRootDenied)
	}
	defer o.mu.Unlock()
	if o.now == nil {
		return reject(ErrRootDenied)
	}
	now := o.now()
	if now.Before(o.loadedAt) || !now.Before(o.view.HardDeadline) {
		return reject(ErrRootDenied)
	}
	// This candidate-only entry is deliberately pre-lease. A future consumer
	// must not re-enter a scope it is already borrowing on the same thread.
	if err := o.prepared.scope.Recheck(ctx); err != nil {
		return reject(err)
	}
	if err := o.checkInputs(ctx); err != nil {
		return reject(err)
	}
	checkedAt := o.now()
	if !o.accepted.Load() || !rootLive(ctx) || checkedAt.Before(now) || !checkedAt.Before(o.view.HardDeadline) {
		return reject(ErrRootDenied)
	}
	view := o.view
	view.OperatorKeys = append([]string(nil), view.OperatorKeys...)
	view.OperatorFingerprints = append([]string(nil), view.OperatorFingerprints...)
	return view, nil
}
func (o *AgentPurposeOwner) Close(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if o.self != o {
		return ErrRootDenied
	}
	o.accepted.Store(false)
	if !rootLive(ctx) || !o.mu.TryLock() {
		return ErrRootUnconfirmed
	}
	defer o.mu.Unlock()
	if o.closed.Load() {
		return nil
	}
	if o.loaded != nil {
		if err := o.loaded.Close(ctx); err != nil {
			return o.reject(err)
		}
	}
	if !rootLive(ctx) {
		return ErrRootUnconfirmed
	}
	o.closed.Store(true)
	return nil
}
