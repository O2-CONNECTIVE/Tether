package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

// helperLaunchFixed is compiled into the helper image, whose path and SHA-256
// the supervisor's root-protected Record pins before SCM or CreateProcess maps
// it. Service names only locate SCM records; images and SIDs are expectations.
type helperLaunchFixed struct {
	parentService, supplierService             string
	parentImage, helperImage, helperServiceSID string
	parentServiceSID                           string
	runBudget, cleanup                         time.Duration
}

// matchesSupplier is the supervisor-side cross-check against its Record. It
// only narrows acceptance; it never fills a Record value from the image.
func (f helperLaunchFixed) matchesSupplier(parent bootstrapauth.Profile, c HelperSupplierConfig) bool {
	return f.valid() && f.parentService == parent.ServiceName && f.parentImage == parent.SupervisorImage.Path && f.parentServiceSID == parent.Keys.ServiceSID &&
		f.supplierService == c.ServiceName && f.helperImage == c.Image.Path && f.helperServiceSID == c.ServiceSID && f.runBudget == c.RunBudget && f.cleanup == c.Cleanup
}

// supplierLifetime bounds the supplier service even when no owner stops it.
func (f helperLaunchFixed) supplierLifetime() time.Duration { return f.runBudget + f.cleanup }

// argv supplies locators only. Each handle is natively inspected before use and
// the launch ID is correlation that the parent-created profile stream must match.
type helperLaunchLocators struct {
	operatorRead, reportWrite, profileRead uintptr
	launchID                               string
}
type helperLaunchService struct{ pid, state uint32 }
type helperLaunchProcess struct {
	pid       uint32
	creation  uint64
	imagePath string
	alive     bool
}
type helperLaunchACE struct {
	kind, flags uint8
	mask        uint32
	sid         string
}

// owner/acl describe the token object itself, not TokenDefaultDacl.
type helperLaunchToken struct {
	tokenID, modifiedID uint64
	primary             tokenProjection
	serviceGroup        helperServiceGroup
	owner               string
	acl                 []helperLaunchACE
}

// server is the pipe's recorded server PID. NPFS keeps no creator generation;
// generation comes from the parent process reference held while it is alive.
type helperLaunchPipe struct {
	original, retained                uintptr
	access, flags, mode, info, server uint32
}

// Observations are read only through references retained by the witness in
// this child, never from argv or profile payload. The parent process query is
// accepted only with the same held parent's token identity and owner/DACL.
type helperLaunchObservation struct {
	parentService, supplierService        helperLaunchService
	inheritedFrom                         uint32
	parent, supplier, self                helperLaunchProcess
	parentToken, supplierToken, selfToken helperLaunchToken
	pipes                                 [3]helperLaunchPipe
}

// acquire performs native I/O and retains every returned reference before
// testing errors. inspect uses those same references, including pipe duplicates
// retained after the entry closes its inherited originals.
type helperLaunchWitness interface {
	acquire(context.Context, helperLaunchFixed, helperLaunchLocators) error
	inspect(context.Context) (helperLaunchObservation, error)
	close(context.Context) error
}
type helperLaunchSource struct {
	mu      sync.Mutex
	self    *helperLaunchSource
	fixed   helperLaunchFixed
	args    []string
	witness helperLaunchWitness
	used    bool
}
type helperLaunchLease struct {
	fixed                    helperLaunchFixed
	loc                      helperLaunchLocators
	witness                  helperLaunchWitness
	request                  context.Context
	gate                     chan struct{}
	pinned                   helperLaunchObservation
	child                    helperBinding
	observed, failed, closed bool
	revoked                  atomic.Bool
	uncertain                bool
}
type helperLaunchReceiver struct{ lease *helperLaunchLease }

// SYNCHRONIZE|READ_CONTROL plus read data/EA/attributes, or write data/attributes.
var helperLaunchPipeAccess = [3]uint32{0x120089, 0x120102, 0x120089}

// runHelperProcess passes the native witness; non-Windows has none and refuses.
func newHelperLaunchSource(fixed helperLaunchFixed, args []string, witness helperLaunchWitness) (*helperLaunchSource, error) {
	if !fixed.valid() || witness == nil {
		return nil, ErrUnconfigured
	}
	s := &helperLaunchSource{fixed: fixed, args: append([]string(nil), args...), witness: witness}
	s.self = s
	return s, nil
}
func (f helperLaunchFixed) valid() bool {
	name := func(s string) bool { return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "/\\\x00") }
	return name(f.parentService) && name(f.supplierService) && !strings.EqualFold(f.parentService, f.supplierService) &&
		localHelperImage(f.parentImage) && localHelperImage(f.helperImage) && !strings.EqualFold(f.parentImage, f.helperImage) &&
		(nativeLaunchSpec{helperUserSID: "S-1-5-19", helperServiceSID: f.helperServiceSID}).helperIdentityValid() &&
		(nativeLaunchSpec{helperUserSID: "S-1-5-19", helperServiceSID: f.parentServiceSID}).helperIdentityValid() && f.parentServiceSID != f.helperServiceSID &&
		f.runBudget > 0 && f.runBudget <= time.Minute && f.cleanup > 0 && f.cleanup <= time.Minute
}

// Only canonical decimal handle numbers are accepted, matching helperEntryConfig.
func parseHelperLaunchArgs(args []string) (helperLaunchLocators, bool) {
	if len(args) != 5 || args[0] != "--operator-auth-internal" || !profileLaunchID(args[4]) {
		return helperLaunchLocators{}, false
	}
	var h [3]uintptr
	for i, arg := range args[1:4] {
		n, err := strconv.ParseUint(arg, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != arg || uint64(uintptr(n)) != n || !reportHandle(uintptr(n)) {
			return helperLaunchLocators{}, false
		}
		h[i] = uintptr(n)
	}
	if h[0] == h[1] || h[0] == h[2] || h[1] == h[2] {
		return helperLaunchLocators{}, false
	}
	return helperLaunchLocators{operatorRead: h[0], reportWrite: h[1], profileRead: h[2], launchID: args[4]}, true
}

// Unparseable argv names no handle to own; process exit closes the inheritance.
func (s *helperLaunchSource) prepare(ctx context.Context) (out helperEntryLease, result error) {
	if s == nil || s.self != s {
		return nil, ErrUnconfigured
	}
	s.mu.Lock()
	if s.used {
		s.mu.Unlock()
		return nil, ErrDenied
	}
	s.used = true
	s.mu.Unlock()
	if !reportContextValid(ctx) {
		return nil, ErrDenied
	}
	loc, ok := parseHelperLaunchArgs(s.args)
	if !ok {
		return nil, ErrDenied
	}
	l := &helperLaunchLease{fixed: s.fixed, loc: loc, witness: s.witness, request: ctx, gate: make(chan struct{}, 1)}
	out = l
	defer func() {
		if recover() != nil {
			l.failed, l.uncertain = true, true
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
		if errors.Is(result, ErrFatal) {
			l.uncertain = true
		}
	}()
	if err := l.witness.acquire(ctx, l.fixed, l.loc); err != nil {
		l.failed = true
		return l, errors.Join(ErrDenied, err)
	}
	if err := l.verify(ctx, nil); err != nil {
		return l, err
	}
	return l, nil
}

// child stays zero until the first observation is pinned, so an unverified
// config cannot pass helperEntryConfig.valid. Locators remain for raw cleanup.
func (l *helperLaunchLease) config() helperEntryConfig {
	return helperEntryConfig{
		binding:      profileReceiverBinding{launchID: l.loc.launchID, profileRead: l.loc.profileRead, child: l.child},
		operatorRead: l.loc.operatorRead, reportWrite: l.loc.reportWrite, runBudget: l.fixed.runBudget, cleanup: l.fixed.cleanup,
	}
}
func (l *helperLaunchLease) receiverSource() profileReceiverSource { return helperLaunchReceiver{l} }
func (l *helperLaunchLease) enter(ctx context.Context) bool {
	if !reportContextValid(ctx) {
		l.revoked.Store(true)
		return false
	}
	select {
	case l.gate <- struct{}{}:
		if reportContextValid(ctx) {
			return true
		}
		<-l.gate
	case <-ctx.Done():
	}
	l.revoked.Store(true)
	return false
}
func (l *helperLaunchLease) check(ctx context.Context, config helperEntryConfig) error {
	if l == nil {
		return ErrDenied
	}
	return l.verify(ctx, &config)
}

// The first verify, only from prepare, pins the observation and derives the
// child binding. Later checks must present that config and observe no drift.
func (l *helperLaunchLease) verify(ctx context.Context, want *helperEntryConfig) (result error) {
	if !l.enter(ctx) {
		return ErrDenied
	}
	defer func() {
		if recover() != nil {
			l.uncertain = true
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
		if errors.Is(result, ErrFatal) {
			l.uncertain = true
		}
		if result != nil {
			l.failed = true
		}
		<-l.gate
	}()
	if l.uncertain {
		return errors.Join(ErrFatal, ErrUnconfirmed)
	}
	if l.closed || l.failed || l.revoked.Load() || !reportContextValid(l.request) || (want == nil) == l.observed ||
		(want != nil && *want != l.config()) {
		return ErrDenied
	}
	o, err := l.witness.inspect(ctx)
	if err != nil {
		return errors.Join(ErrDenied, err)
	}
	if !reportContextValid(ctx) || !reportContextValid(l.request) || l.revoked.Load() || !l.matches(o) {
		return ErrDenied
	}
	if l.observed {
		if !reflect.DeepEqual(o, l.pinned) {
			return ErrDenied
		}
		return nil
	}
	o.parentToken.acl, o.supplierToken.acl, o.selfToken.acl = slices.Clone(o.parentToken.acl), slices.Clone(o.supplierToken.acl), slices.Clone(o.selfToken.acl)
	st, ct := o.supplierToken, o.selfToken
	l.pinned, l.observed = o, true
	l.child = helperBinding{pid: o.self.pid, creation: o.self.creation, tokenID: ct.tokenID, childModifiedID: ct.modifiedID,
		sourceTokenID: st.tokenID, sourceModifiedID: st.modifiedID, user: ct.primary.user, imagePath: o.self.imagePath,
		primary: ct.primary, sourceServiceGroup: st.serviceGroup, childServiceGroup: ct.serviceGroup}
	return nil
}

// The parent is this process's kernel creator, the fixed supervisor Service's
// running PID, alive and older than this child. Every pipe server is that same
// parent. The supplier is the fixed supplier Service sharing the helper image.
func (l *helperLaunchLease) matches(o helperLaunchObservation) bool {
	f, p, s, c := l.fixed, o.parent, o.supplier, o.self
	if o.parentService != (helperLaunchService{p.pid, 4}) || o.supplierService != (helperLaunchService{s.pid, 4}) ||
		o.inheritedFrom != p.pid || !p.olderThan(f.parentImage, c.creation) || !s.olderThan(f.helperImage, c.creation) ||
		c.pid == 0 || c.creation == 0 || !c.alive || c.imagePath != f.helperImage || p.pid == s.pid || p.pid == c.pid || s.pid == c.pid {
		return false
	}
	// The SYSTEM supervisor's own held token pairs the process-query evidence:
	// fixed user/service SID, and no right beyond query for helper or guest.
	pt := o.parentToken
	if pt.tokenID == 0 || pt.modifiedID == 0 || pt.primary.user != "S-1-5-18" || pt.primary.kind != 1 || pt.primary.restricted || pt.primary.appContainer ||
		!pt.serviceGroup.matches(f.parentServiceSID) || !helperLaunchTokenProtected(pt, f.parentServiceSID) {
		return false
	}
	st, ct := o.supplierToken, o.selfToken
	if !st.helper(f.helperServiceSID) || !helperLaunchTokenProtected(st, f.helperServiceSID) || !ct.helper(f.helperServiceSID) ||
		ct.primary != st.primary || ct.serviceGroup != st.serviceGroup {
		return false
	}
	originals := [3]uintptr{l.loc.operatorRead, l.loc.reportWrite, l.loc.profileRead}
	modes := [3]uint32{1, 1, 2} // OVERLAPPED, OVERLAPPED, synchronous
	// Named server end/message, unqueried write-only client, anonymous read end.
	infos := [3]uint32{5, 0, 1}
	seen := map[uintptr]bool{originals[0]: true, originals[1]: true, originals[2]: true}
	for i, pipe := range o.pipes {
		if pipe.original != originals[i] || !reportHandle(pipe.retained) || seen[pipe.retained] || pipe.access != helperLaunchPipeAccess[i] ||
			pipe.flags != 0 || pipe.mode != modes[i] || pipe.info != infos[i] || pipe.server != p.pid {
			return false
		}
		seen[pipe.retained] = true
	}
	return true
}
func (p helperLaunchProcess) olderThan(image string, child uint64) bool {
	return p.pid != 0 && p.alive && p.creation != 0 && p.creation < child && p.imagePath == image
}
func (t helperLaunchToken) helper(serviceSID string) bool {
	return t.tokenID != 0 && t.modifiedID != 0 && t.primary.user == "S-1-5-19" && t.primary.kind == 1 &&
		t.primary.authentication != 0 && t.primary.integrity != 0 && !t.primary.restricted && !t.primary.appContainer &&
		t.serviceGroup.matches(serviceSID)
}

// Opening the supplier with process query is paired with its held token's
// owner/DACL: only SYSTEM, Administrators and the dedicated service SID may
// own it or hold more than READ_CONTROL|TOKEN_QUERY. Shared LocalService, the
// operator and the guest may only query. This is the single token-object
// predicate: the supervisor's Record check and native supplier snapshot call
// it through supplierTokenProtected, so one Record is judged alike on both sides.
func helperLaunchTokenProtected(t helperLaunchToken, serviceSID string) bool {
	trusted := func(sid string) bool { return sid == "S-1-5-18" || sid == "S-1-5-32-544" || sid == serviceSID }
	if !trusted(t.owner) || t.acl == nil || len(t.acl) > 32 {
		return false
	}
	for _, ace := range t.acl {
		if ace.kind > 1 || ace.flags != 0 || ace.mask == 0 || ace.mask&^uint32(0x010f01ff) != 0 || !reportSIDValid(ace.sid) {
			return false
		}
		if ace.kind == 0 && !trusted(ace.sid) && ace.mask&^uint32(0x20008) != 0 {
			return false
		}
	}
	return true
}
func (s helperLaunchReceiver) check(ctx context.Context, binding profileReceiverBinding) error {
	if s.lease == nil {
		return ErrUnconfigured
	}
	c := s.lease.config()
	c.binding = binding
	return s.lease.check(ctx, c)
}
func (l *helperLaunchLease) close(ctx context.Context) (result error) {
	if l == nil || !l.enter(ctx) {
		return ErrUnconfirmed
	}
	defer func() {
		if recover() != nil {
			l.uncertain = true
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
		if errors.Is(result, ErrFatal) {
			l.uncertain = true
		}
		<-l.gate
	}()
	l.failed = true
	if l.uncertain {
		return errors.Join(ErrFatal, ErrUnconfirmed)
	}
	if l.closed {
		return nil
	}
	if err := l.witness.close(ctx); err != nil {
		return errors.Join(ErrUnconfirmed, err)
	}
	l.closed = true
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
