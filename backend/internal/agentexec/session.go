package agentexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// FixedStartConstraint is trusted local admission narrowing, not wire authority.
// Command is a string so a value copy has no caller-mutable byte backing store.
// A non-nil constraint admits at most one exact request for this grant. It does
// not sandbox that command or change its OS identity, binding or hard deadline.
// Exactly one of Command (the exact text) and CommandSHA256 (lowercase hex of
// the text that arrives later) is set. Dir is required with the hash and
// refused with Command; it needs a runtime that implements DirectoryRuntime.
type FixedStartConstraint struct {
	RequestID     string
	Command       string
	TimeoutMillis uint32
	CommandSHA256 string
	Dir           string
}

// Validate checks the default request bounds before local consent. New also
// checks any stricter owner Limits. It neither issues nor consumes authority.
func (c FixedStartConstraint) Validate() error {
	return c.validate(Limits{}.normalized())
}
func (c FixedStartConstraint) validate(l Limits) error {
	if !validRequestID(c.RequestID) || c.TimeoutMillis == 0 || c.TimeoutMillis > l.MaxTimeout {
		return ErrUnconfigured
	}
	if c.Command != "" {
		if c.CommandSHA256 != "" || c.Dir != "" || len(c.Command) > l.MaxCommandBytes {
			return ErrUnconfigured
		}
		return nil
	}
	decoded, err := hex.DecodeString(c.CommandSHA256)
	if err != nil || len(c.CommandSHA256) != sha256.Size*2 || hex.EncodeToString(decoded) != c.CommandSHA256 || c.Dir == "" {
		return ErrUnconfigured
	}
	return nil
}

// Config is supplied only by the protected owner that received Authority from
// the same Issuer.Launch. Request fields never select or replace these values.
// The approved mode is not configurable: it is derived from the grant policy.
type Config struct {
	// ElapsedDeadline is an optional, trusted-owner monotonic deadline fixed
	// before consent. It can only shorten the Session budget, never extend it.
	// maclaunch passes Prepare's time.Now().Add(initialBudget), retaining its
	// monotonic component. It is not accepted from wire requests.
	ElapsedDeadline time.Time
	FixedStart      *FixedStartConstraint
	Issuer          *ephemeral.Issuer
	Authority       *supervisor.Authority
	Runtime         GuestRuntime
	Clock           func() time.Time
	Limits          Limits
	CleanupTimeout  time.Duration // bounds each Terminate/Release attempt after the grant ends
}

// Session serves one grant and at most one non-terminal command at a time. It
// is never reset or resumed: after revoke, expiry or transport loss a new
// approval must create a new Issuer authority and Session.
type Session struct {
	issuer         *ephemeral.Issuer
	authority      *supervisor.Authority
	binding        supervisor.Binding
	keySet         [32]byte // the grant's approved key set digest, immutable
	profile        Profile
	mode           Mode
	deadline       time.Time
	runtime        GuestRuntime
	clock          func() time.Time
	limits         Limits
	fixedStart     *FixedStartConstraint
	cleanupTimeout time.Duration
	random         io.Reader
	afterFunc      func(time.Duration, func()) func() bool
	beforeStart    func() // test seam between admission and the pre-start recheck
	// The hard deadline has one owner: checkDeadline on one timer at a time.
	// budget is the wall time left at New, fixed as an elapsed-time cap that a
	// later wall clock change can shorten but never extend; elapsed reads the
	// monotonic time since New; recheck bounds every wait so a wall-clock
	// jump (or a sleep the monotonic clock did not count) is seen within it.
	budget  time.Duration
	elapsed func() time.Duration
	recheck time.Duration

	mu           sync.Mutex
	base         context.Context
	cancelBase   context.CancelFunc
	revoked      bool
	reason       StopReason
	byRequest    map[string]*command // per grant; never carried into another Session
	byID         map[string]*command
	order        []*command
	deadlineStop func() bool
	// adapterClaimed and transportClaimed are set once by Claim and
	// ClaimTransport and never cleared.
	adapterClaimed   bool
	transportClaimed bool
	adapterPrefix    string // fixed by the one trusted wire adapter's claim
}

// runtimeCommandContext exposes the existing finite command/Session budget.
// It deliberately owns no timer: Session's existing deadline owner still
// revokes canonical authority before canceling a grant-capped command.
type runtimeCommandContext struct {
	context.Context
	deadline time.Time
}

func (c *runtimeCommandContext) Deadline() (time.Time, bool) { return c.deadline, true }

type command struct {
	id, requestID string
	digest        [32]byte
	stdin         []byte // cleared once the runtime call returns
	ctx           context.Context
	cancel        context.CancelFunc
	timerStop     func() bool
	done          chan struct{}
	out           *outputBuffer

	phase      Phase
	exit       ExitStatus
	exited     bool // natural exit observed before any stop
	stop       StopReason
	cleanup    Cleanup
	terminal   bool
	proc       GuestProcess // retained until Release is confirmed
	terminated bool         // Terminate returned nil; a retry resumes at Release
	releasing  bool         // the worker owns Terminate/Release from here
	killSent   bool         // one early Terminate per command from a stop request
	cleaning   bool         // a Cleanup retry is in flight
	terms      sync.WaitGroup
}

// New claims the issuer's current Authority for this single Session. It fails
// with ErrPolicy unless the grant carries an approved agent policy digest,
// ErrProfile unless that policy, the runtime profile, the grant boundary and
// the issuer kind all name the same profile (checked before any claim), ErrSessionClaimed
// if any Session already claimed it (Close never releases the claim), and
// ErrUnconfigured for any Authority that is not the issuer's current object.
func New(cfg Config) (*Session, error) {
	return newSession(cfg, func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop })
}

func newSession(cfg Config, after func(time.Duration, func()) func() bool) (*Session, error) {
	if cfg.Issuer == nil || cfg.Authority == nil || cfg.Runtime == nil || cfg.Clock == nil || cfg.CleanupTimeout <= 0 || after == nil {
		return nil, ErrUnconfigured
	}
	if !cfg.ElapsedDeadline.IsZero() && !time.Now().Before(cfg.ElapsedDeadline) {
		return nil, ErrDenied
	}
	limits := cfg.Limits.normalized()
	var fixed *FixedStartConstraint
	if cfg.FixedStart != nil {
		copied := *cfg.FixedStart
		if err := copied.validate(limits); err != nil {
			return nil, err
		}
		if copied.Dir != "" {
			if _, ok := cfg.Runtime.(DirectoryRuntime); !ok {
				return nil, ErrUnconfigured
			}
		}
		fixed = &copied
		limits.MaxCommands = 1
	}
	if limits.MaxRetained <= recordOverhead || limits.MaxTotalOutput == 0 {
		return nil, ErrUnconfigured
	}
	clock := func() time.Time { return readClock(cfg.Clock) }
	// Grant does not observe time; the only clock observation is Lookup, which
	// the issuer serializes, so concurrent New calls cannot report a regression.
	grant := cfg.Authority.Grant()
	binding := grant.Binding
	profile, mode := approvedPolicy(binding.Identity.PolicyDigest)
	if mode == 0 {
		return nil, ErrPolicy
	}
	// The trusted local runtime must serve exactly the approved profile: a Mac
	// current-user runtime never serves a Windows-guest grant, or vice versa.
	if runtimeProfile(cfg.Runtime) != profile {
		return nil, ErrProfile
	}
	// The approved boundary and the issuer kind must match the same profile, so
	// a Mac policy on a guest grant, or a Windows policy on a current-user grant,
	// is refused before the claim. The issuer pointer check follows below.
	if b := profileBoundary(profile); b == 0 || grant.GuestBoundary != b || cfg.Issuer.Boundary() != b {
		return nil, ErrProfile
	}
	// Pointer identity with the issuer's current entry is the authority check;
	// an equal-looking Authority or Binding from elsewhere is rejected.
	if current, err := cfg.Issuer.Lookup(binding, clock); err != nil || current != cfg.Authority {
		select {
		case <-cfg.Authority.Done():
			return nil, ErrDenied
		default:
			return nil, ErrUnconfigured
		}
	}
	if err := cfg.Authority.ClaimConsumer(binding); err != nil {
		if errors.Is(err, supervisor.ErrClaimed) {
			return nil, ErrSessionClaimed
		}
		return nil, ErrDenied
	}
	s := &Session{
		issuer: cfg.Issuer, authority: cfg.Authority, binding: binding, keySet: grant.KeySetDigest, profile: profile, mode: mode, deadline: grant.HardDeadline,
		runtime: cfg.Runtime, clock: clock, limits: limits, fixedStart: fixed, cleanupTimeout: cfg.CleanupTimeout, random: rand.Reader, afterFunc: after,
		byRequest: map[string]*command{}, byID: map[string]*command{},
	}
	s.base, s.cancelBase = context.WithCancel(context.Background())
	started := time.Now()
	s.elapsed = func() time.Duration { return time.Since(started) }
	s.budget, s.recheck = s.deadline.Sub(clock()), DeadlineRecheck
	if !cfg.ElapsedDeadline.IsZero() {
		s.budget = min(s.budget, cfg.ElapsedDeadline.Sub(started))
	}
	done := cfg.Authority.Done()
	s.mu.Lock()
	s.deadlineStop = func() bool { return false }
	s.armDeadlineLocked()
	s.mu.Unlock()
	go s.watch(done)
	return s, nil
}

// DeadlineRecheck bounds every wait of the deadline owner. A monotonic timer
// alone can fire late after a sleep or a forward wall-clock jump; re-observing
// the canonical issuer clock at this cadence ends the grant within it without
// any request.
const DeadlineRecheck = time.Second

// armDeadlineLocked replaces the single deadline timer. The wait is the least
// of the recheck cadence, the wall time left and the elapsed budget left; it
// never schedules anything after revocation.
func (s *Session) armDeadlineLocked() {
	if s.revoked {
		return
	}
	s.deadlineStop()
	wait := s.recheck
	if left := s.deadline.Sub(s.clock()); left < wait {
		wait = left
	}
	if left := s.budget - s.elapsed(); left < wait {
		wait = left
	}
	s.deadlineStop = s.afterFunc(max(wait, 0), s.checkDeadline)
}

// ObserveDeadline re-observes the canonical issuer clock now, as the deadline
// owner would at its next recheck. It revokes with StopHardDeadline only if
// the deadline (or the elapsed budget) has passed and never extends anything.
// A transport that ends at the hard deadline calls it before TransportLost so
// the first recorded stop is the deadline, not the transport.
func (s *Session) ObserveDeadline() {
	if s == nil || s.issuer == nil {
		return
	}
	s.checkDeadline()
}

// checkDeadline decides through the canonical issuer: Lookup reads the clock
// under the issuer lock and revokes on an expired, regressed or unavailable
// clock. The elapsed budget can only end the grant earlier. A callback that
// runs before both have passed re-arms instead of revoking, so an early or
// spurious timer never ends a live grant. Revocation precedes any cancel.
func (s *Session) checkDeadline() {
	s.mu.Lock()
	if s.revoked {
		s.mu.Unlock()
		return
	}
	live, reason := s.liveLocked()
	if live && s.elapsed() >= s.budget {
		live, reason = false, StopHardDeadline
	}
	if live {
		s.armDeadlineLocked()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.revoke(reason)
}

// watch follows the held Authority itself, so issuer Revoke/Close and Owner
// stop reach running work without waiting for a request or for output. It
// does not borrow the Launch callback context, whose lifetime is the caller's.
func (s *Session) watch(done <-chan struct{}) {
	select {
	case <-done:
		reason := StopRevoked
		if _, r := s.authority.Status(); r == supervisor.HardDeadlineReached {
			reason = StopHardDeadline
		}
		s.revoke(reason)
	case <-s.base.Done():
	}
}

func readClock(clock func() time.Time) (now time.Time) {
	defer func() {
		if recover() != nil {
			now = time.Time{}
		}
	}()
	return clock()
}

// Binding is the grant this session serves. It is an identifier for request
// routing, not a credential.
func (s *Session) Binding() supervisor.Binding {
	if s == nil {
		return supervisor.Binding{}
	}
	return s.binding
}

// Profile is the execution environment approved by the grant and served by the
// runtime. Adapters can show it; it is not an authorization input.
func (s *Session) Profile() Profile {
	if s == nil {
		return 0
	}
	return s.profile
}

// Mode is the mode derived from the grant policy. Adapters pass it back in
// StartRequest; it is never selected by a request.
func (s *Session) Mode() Mode {
	if s == nil {
		return 0
	}
	return s.mode
}

// KeySetDigest is the immutable approved key set digest of the grant, for the
// trusted channel composition to check its authenticated peer key against.
// It is a read-only copy and neither authenticates nor authorizes anything.
func (s *Session) KeySetDigest() [32]byte {
	if s == nil {
		return [32]byte{}
	}
	return s.keySet
}

// Claim reserves this Session for exactly one adapter, such as one wire
// server. It is single-use and never released, so a second adapter cannot
// serve, replay into or adopt the first adapter's commands.
func (s *Session) Claim() error { return s.claim(&s.adapterClaimed) }

// ClaimWithRequestPrefix takes the same single adapter claim as Claim and
// records its locally generated request namespace. Wire still passes the full
// prefixed ID for idempotency, while FixedStart checks the approved client ID
// in that exact namespace. Direct owner starts keep their separate raw IDs.
// The prefix is trusted composition input, never a wire or MCP request field.
func (s *Session) ClaimWithRequestPrefix(prefix string) error {
	if s == nil || s.issuer == nil || !validRequestID(prefix) {
		return ErrUnconfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adapterClaimed {
		return ErrSessionClaimed
	}
	if s.fixedStart != nil && !validRequestID(prefix+s.fixedStart.RequestID) {
		return ErrUnconfigured
	}
	s.adapterClaimed, s.adapterPrefix = true, prefix
	return nil
}

// ClaimTransport reserves this Session for exactly one authenticated channel
// server. Like Claim it is single-use and never released: there is no second
// channel, reconnect or reset for the same grant.
func (s *Session) ClaimTransport() error { return s.claim(&s.transportClaimed) }

func (s *Session) claim(flag *bool) error {
	if s == nil || s.issuer == nil {
		return ErrUnconfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if *flag {
		return ErrSessionClaimed
	}
	*flag = true
	return nil
}

// Grant is a copy of the immutable grant of the canonical Authority this
// Session claimed, read without observing time. It is not authorization: a
// transport uses it to bind to exactly this Session's grant, key set digest,
// boundary and hard deadline instead of trusting a separately supplied value.
func (s *Session) Grant() supervisor.ProvisionedGrant {
	if s == nil || s.authority == nil {
		return supervisor.ProvisionedGrant{}
	}
	return s.authority.Grant()
}

// Done is closed once the canonical Authority of this Session is revoked, for
// any reason, including this Session's own revoke, deadline or transport loss.
func (s *Session) Done() <-chan struct{} {
	if s == nil || s.authority == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.authority.Done()
}

func (s *Session) header(version uint16, binding supervisor.Binding) error {
	if s == nil || s.issuer == nil {
		return ErrUnconfigured
	}
	if version != Version {
		return ErrVersion
	}
	if binding != s.binding {
		return ErrBinding
	}
	return nil
}

// liveLocked re-observes the canonical issuer entry. Lookup itself revokes an
// expired entry; it neither renews nor extends the grant.
func (s *Session) liveLocked() (bool, StopReason) {
	if s.revoked {
		return false, s.reason
	}
	if s.elapsed != nil && s.elapsed() >= s.budget {
		return false, StopHardDeadline
	}
	if current, err := s.issuer.Lookup(s.binding, s.clock); err == nil && current == s.authority {
		return true, StopNone
	}
	// Read the reason Lookup recorded without a second clock observation, so a
	// clock value read earlier on another goroutine cannot look like a regression.
	if _, r := s.authority.Status(); r == supervisor.HardDeadlineReached {
		return false, StopHardDeadline
	}
	return false, StopRevoked
}

// observe applies an external revocation before any operation reports state.
// revoke is idempotent; repeating it ensures the stop reason is recorded even
// while the Authority watcher is still between its own steps.
func (s *Session) observe() {
	s.mu.Lock()
	live, reason := s.liveLocked()
	s.mu.Unlock()
	if !live {
		s.revoke(reason)
	}
}

// Start admits one command after checking the live grant. A replay of the same
// request ID and canonical payload returns the existing execution of this grant
// without any new side effect, including after an uncertain start or revoke.
func (s *Session) Start(r StartRequest) (Status, error) {
	if err := s.header(r.Version, r.Binding); err != nil {
		return Status{}, err
	}
	if !validRequestID(r.RequestID) || len(r.Command) == 0 || len(r.Command) > s.limits.MaxCommandBytes ||
		r.TimeoutMillis == 0 || r.TimeoutMillis > s.limits.MaxTimeout {
		return Status{}, ErrInvalid
	}
	switch r.Mode {
	case ModeFull:
	case ModeRestricted:
		return Status{}, ErrRestrictedUndefined
	default:
		return Status{}, ErrInvalid
	}
	if r.Mode != s.mode {
		return Status{}, ErrPolicy
	}
	digest := r.digest()
	s.observe()
	s.mu.Lock()
	if c := s.byRequest[r.RequestID]; c != nil {
		defer s.mu.Unlock()
		if c.digest != digest {
			return Status{}, ErrRequestMismatch
		}
		return c.statusLocked(), nil
	}
	if live, reason := s.liveLocked(); !live {
		s.mu.Unlock()
		s.revoke(reason)
		return Status{}, ErrDenied
	}
	defer s.mu.Unlock()
	if f := s.fixedStart; f != nil {
		matchingID := r.RequestID == f.RequestID || (s.adapterPrefix != "" && r.RequestID == s.adapterPrefix+f.RequestID)
		commandMatches := string(r.Command) == f.Command
		if f.CommandSHA256 != "" {
			sum := sha256.Sum256(r.Command)
			commandMatches = hex.EncodeToString(sum[:]) == f.CommandSHA256
		}
		if !matchingID || !commandMatches || r.TimeoutMillis != f.TimeoutMillis {
			return Status{}, ErrPolicy
		}
	}
	if n := len(s.order); n > 0 {
		last := s.order[n-1]
		switch {
		case !last.terminal || last.cleaning:
			return Status{}, ErrBusy
		case last.cleanup == CleanupUnknown:
			return Status{}, ErrCleanupUnknown
		}
	}
	if len(s.order) >= s.limits.MaxCommands {
		return Status{}, ErrExhausted
	}
	var raw [16]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return Status{}, ErrIdentifier
	}
	id := hex.EncodeToString(raw[:])
	if s.byID[id] != nil {
		return Status{}, ErrIdentifier
	}
	for _, previous := range s.order {
		previous.out.releaseLocked()
	}
	now := s.clock()
	until := now.Add(time.Duration(r.TimeoutMillis) * time.Millisecond)
	// A command deadline at or past the grant deadline is the grant deadline:
	// its timer revokes the grant first instead of racing the Session timer
	// and mislabelling the stop as a command timeout.
	capped := !until.Before(s.deadline)
	if capped {
		until = s.deadline
	}
	c := &command{
		id: id, requestID: r.RequestID, digest: digest,
		stdin: append([]byte(nil), r.Command...), done: make(chan struct{}), out: newOutputBuffer(),
		phase: PhaseStarting, cleanup: CleanupPending,
	}
	owned, cancel := context.WithCancel(s.base)
	// Keep the existing timer and cancellation owner. Metadata can only shorten
	// admission: requested timeout, canonical hard deadline, and the immutable
	// Session elapsed cap all bound what StartShell may observe.
	deadline := until
	elapsedDeadline := time.Now().Add(max(s.budget-s.elapsed(), 0))
	if elapsedDeadline.Before(deadline) {
		deadline = elapsedDeadline
	}
	c.ctx, c.cancel = &runtimeCommandContext{Context: owned, deadline: deadline}, cancel
	s.byRequest[c.requestID], s.byID[c.id] = c, c
	s.order = append(s.order, c)
	c.timerStop = s.afterFunc(until.Sub(now), func() {
		if capped {
			// The grant deadline is decided only by the deadline owner, which
			// revokes the grant before stopping this command.
			s.checkDeadline()
			return
		}
		s.stopCommand(c, StopCommandTimeout)
	})
	go s.run(c)
	return c.statusLocked(), nil
}

func (s *Session) find(version uint16, binding supervisor.Binding, id string) (*command, error) {
	if err := s.header(version, binding); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.byID[id]
	if c == nil {
		return nil, ErrNotFound
	}
	return c, nil
}

// Status reports phase, exit status and cleanup separately. It observes an
// external revocation first but does not require a live grant.
func (s *Session) Status(r StatusRequest) (Status, error) {
	c, err := s.find(r.Version, r.Binding, r.CommandID)
	if err != nil {
		return Status{}, err
	}
	s.observe()
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.statusLocked(), nil
}

// Output returns bounded data from Cursor and acknowledges earlier bytes. Guest
// output is delivered only while the grant is live.
func (s *Session) Output(r OutputRequest) (Output, error) {
	c, err := s.find(r.Version, r.Binding, r.CommandID)
	if err != nil {
		return Output{}, err
	}
	if r.MaxBytes <= 0 {
		return Output{}, ErrInvalid
	}
	s.mu.Lock()
	if live, reason := s.liveLocked(); !live {
		s.mu.Unlock()
		s.revoke(reason)
		return Output{}, ErrDenied
	}
	defer s.mu.Unlock()
	return c.out.readLocked(r.Cursor, min(r.MaxBytes, s.limits.MaxReply))
}

// Cancel closes this command irreversibly and requests guest termination. It
// is idempotent and does not revoke the grant; cleanup continues in the worker.
func (s *Session) Cancel(r CancelRequest) (Status, error) {
	c, err := s.find(r.Version, r.Binding, r.CommandID)
	if err != nil {
		return Status{}, err
	}
	s.observe()
	s.stopCommand(c, StopCanceled)
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.statusLocked(), nil
}

// Cleanup retries the unconfirmed phase with the same retained process. It
// never starts or adopts another process and needs no live grant.
func (s *Session) Cleanup(ctx context.Context, r CleanupRequest) (Status, error) {
	c, err := s.find(r.Version, r.Binding, r.CommandID)
	if err != nil {
		return Status{}, err
	}
	if ctx == nil || ctx.Err() != nil {
		return Status{}, ErrInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return Status{}, ErrInvalid
	}
	s.observe()
	s.mu.Lock()
	switch {
	case !c.terminal || c.cleaning:
		defer s.mu.Unlock()
		return c.statusLocked(), ErrBusy
	case c.cleanup == CleanupConfirmed || c.cleanup == CleanupNotRequired:
		defer s.mu.Unlock()
		return c.statusLocked(), nil
	case c.proc == nil:
		defer s.mu.Unlock()
		return c.statusLocked(), ErrCleanupUnknown
	}
	c.cleaning = true
	proc, terminated := c.proc, c.terminated
	s.mu.Unlock()
	if !terminated {
		err = guard(func() error { return proc.Terminate(ctx) })
	}
	if err == nil {
		terminated = true
		err = guard(func() error { return proc.Release(ctx) })
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.cleaning, c.terminated = false, terminated
	if err != nil {
		return c.statusLocked(), ErrCleanupUnknown
	}
	c.cleanup, c.proc = CleanupConfirmed, nil
	return c.statusLocked(), nil
}

// Revoke irreversibly revokes the grant before cancelling guest workload.
func (s *Session) Revoke() { s.revoke(StopRevoked) }

// TransportLost is not a reconnectable pause: the grant is revoked first and a
// fresh approval is required for any later command.
func (s *Session) TransportLost() { s.revoke(StopTransportLost) }

func (s *Session) revoke(reason StopReason) {
	if s == nil || s.issuer == nil {
		return
	}
	s.mu.Lock()
	if !s.revoked {
		s.revoked, s.reason = true, reason
	}
	reason = s.reason
	s.mu.Unlock()
	// Grant first. The issuer may already have moved past this entry, so the
	// held Authority is revoked directly as well; both are irreversible.
	_ = s.issuer.Revoke(s.binding)
	_ = s.authority.Revoke(s.binding)
	s.mu.Lock()
	s.deadlineStop()
	var victims []*command
	for _, c := range s.order {
		if !c.terminal {
			if c.stop == StopNone && !c.exited {
				c.stop = reason
			}
			victims = append(victims, c)
		}
	}
	s.mu.Unlock()
	s.cancelBase()
	for _, c := range victims {
		s.stopCommand(c, reason)
	}
}

// stopCommand records the first stop reason, cancels the worker context and
// asks the retained process to terminate so a blocked read cannot outlive it.
// After a natural exit the worker already owns whole-workload termination.
func (s *Session) stopCommand(c *command, reason StopReason) {
	s.mu.Lock()
	if c.terminal || c.exited {
		s.mu.Unlock()
		return
	}
	if c.stop == StopNone {
		c.stop = reason
	}
	c.cancel()
	proc := c.proc
	if proc == nil || c.releasing || c.killSent {
		s.mu.Unlock()
		return
	}
	c.killSent = true
	c.terms.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), s.cleanupTimeout)
	s.mu.Unlock()
	// The stop caller owns admission/cancellation, not this potentially slower
	// native operation. Keep the one early attempt owned by the command: run
	// joins terms before its final Terminate/Release and before closing done.
	// A caller's shorter Close budget can return unknown without detaching it.
	go func() {
		defer c.terms.Done()
		defer cancel()
		_ = guard(func() error { return proc.Terminate(ctx) })
	}()
}

// Close revokes the grant and waits for every command worker within ctx. It
// reports ErrCleanupUnknown unless every retained workload was released. The
// Authority claim is never returned, so the same grant cannot get a new Session.
func (s *Session) Close(ctx context.Context) error {
	if s == nil || s.issuer == nil {
		return ErrUnconfigured
	}
	s.revoke(StopRevoked)
	s.mu.Lock()
	order := append([]*command(nil), s.order...)
	s.mu.Unlock()
	for _, c := range order {
		if ctx == nil {
			return ErrCleanupUnknown
		}
		select {
		case <-c.done:
		case <-ctx.Done():
			return ErrCleanupUnknown
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range order {
		if c.cleanup != CleanupConfirmed && c.cleanup != CleanupNotRequired {
			return ErrCleanupUnknown
		}
	}
	return nil
}

func (c *command) statusLocked() Status {
	return Status{
		CommandID: c.id, Phase: c.phase, Exit: c.exit, Stop: c.stop, Cleanup: c.cleanup, Terminal: c.terminal,
		OutputEnd: c.out.end, Truncated: c.out.truncated, Dropped: c.out.dropped,
	}
}
