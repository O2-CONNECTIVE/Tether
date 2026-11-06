// Package agentexec is the memory-only agent command core for one approved
// grant. It holds the canonical issuer Authority supplied by the protected owner
// and never derives authority from request fields, SIDs, owner IDs or booleans.
// The mode comes only from the grant's fixed agent policy digest, the Session
// single-use claims that Authority, and its revocation notification stops work.
// Command bytes reach only the fixed guest shell stdin of GuestRuntime; there is
// no argv, privileged shell, native launcher, transport, MCP or reconnect here.
package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// Version is the only accepted request contract version.
const Version uint16 = 1

// fullPolicyDocument is the fixed agent command policy, separate from the
// SSH/Python policy digest. A grant serves agent commands only when the
// protected approval put exactly this document's digest into the issuer policy.
const fullPolicyDocument = "rescue-host.agent-command-policy\x00v1\x00" +
	"mode=full\x00principal=temporary-nonadmin-guest\x00input=fixed-guest-shell-stdin-only\x00" +
	"argv=fixed-by-runtime\x00target-path=none\x00privileged-shell=none\x00" +
	"restricted-helper=undefined-refused\x00commands=one-active-per-grant\x00"

// FullPolicyDigest is derived from the fixed document above. It is not a
// credential and cannot be configured; producing a grant with it is the
// protected approval owner's decision.
func FullPolicyDigest() string {
	sum := sha256.Sum256([]byte(fullPolicyDocument))
	return hex.EncodeToString(sum[:])
}

// macCurrentUserPolicyDocument is a separate, versioned policy for the macOS
// runtime that runs as the already logged-in user. It promises no guest
// isolation: commands reach that user's resources, and cleanup covers only the
// owned shell, its process group and its I/O, not every detached descendant.
const macCurrentUserPolicyDocument = "rescue-host.agent-command-policy.mac-current-user\x00v1\x00" +
	"mode=full\x00platform=darwin\x00principal=logged-in-current-user\x00elevation=none\x00" +
	"isolation=none-current-user-resources-reachable\x00input=fixed-shell-stdin-only\x00" +
	"argv=fixed-by-runtime\x00environment=fixed-minimal\x00cwd=fixed-by-local-owner-not-a-sandbox\x00" +
	"cleanup=owned-process-group-and-io-only\x00restricted-helper=undefined-refused\x00" +
	"commands=one-active-per-grant\x00"

// MacCurrentUserPolicyDigest is derived from the fixed Mac document. Like
// FullPolicyDigest it cannot be configured or supplied by a request.
func MacCurrentUserPolicyDigest() string {
	sum := sha256.Sum256([]byte(macCurrentUserPolicyDocument))
	return hex.EncodeToString(sum[:])
}

// Profile names the execution environment a policy approves. It is decided by
// the grant digest and by the trusted local runtime, never by a request.
type Profile uint8

const (
	ProfileWindowsGuest   Profile = 1 // FullPolicyDigest: temporary non-admin guest
	ProfileMacCurrentUser Profile = 2 // MacCurrentUserPolicyDigest: logged-in macOS user
)

// ProfiledRuntime is implemented by a runtime that declares its profile. A
// runtime without it keeps the original GuestRuntime meaning, the Windows
// temporary guest, and can never serve another profile's grant.
type ProfiledRuntime interface {
	GuestRuntime
	AgentProfile() Profile
}

func runtimeProfile(r GuestRuntime) Profile {
	if p, ok := r.(ProfiledRuntime); ok {
		return p.AgentProfile()
	}
	return ProfileWindowsGuest
}

// approvedPolicy maps only the fixed agent policies; every other digest,
// including the evaluation exit0 or SSH policies, selects nothing.
func approvedPolicy(policyDigest string) (Profile, Mode) {
	switch policyDigest {
	case FullPolicyDigest():
		return ProfileWindowsGuest, ModeFull
	case MacCurrentUserPolicyDigest():
		return ProfileMacCurrentUser, ModeFull
	}
	return 0, 0
}

// profileBoundary is the only grant boundary each profile accepts. The
// current-user boundary comes only from ephemeral.NewCurrentUser through
// supervisor.NewCurrentUser; the default issuer and guest consumers refuse it.
func profileBoundary(p Profile) supervisor.GuestBoundary {
	switch p {
	case ProfileWindowsGuest:
		return supervisor.SeparatedNonAdminGuest
	case ProfileMacCurrentUser:
		return supervisor.CurrentUserNoIsolation
	}
	return 0
}

var (
	ErrUnconfigured        = errors.New("agentexec: session is unconfigured")
	ErrVersion             = errors.New("agentexec: unsupported request version")
	ErrInvalid             = errors.New("agentexec: malformed request")
	ErrBinding             = errors.New("agentexec: request is bound to another grant")
	ErrDenied              = errors.New("agentexec: grant is not live")
	ErrRestrictedUndefined = errors.New("agentexec: restricted helper bodies are undefined")
	ErrRequestMismatch     = errors.New("agentexec: request ID was used with another payload")
	ErrBusy                = errors.New("agentexec: another command or its cleanup is in progress")
	ErrCleanupUnknown      = errors.New("agentexec: command cleanup is unconfirmed")
	ErrNotFound            = errors.New("agentexec: unknown command")
	ErrCursor              = errors.New("agentexec: output cursor is beyond the produced output")
	ErrExhausted           = errors.New("agentexec: per-session command limit reached")
	ErrPolicy              = errors.New("agentexec: grant policy does not approve this mode")
	ErrSessionClaimed      = errors.New("agentexec: grant already has a session")
	ErrIdentifier          = errors.New("agentexec: command ID could not be allocated")
	ErrProfile             = errors.New("agentexec: runtime profile does not match the grant policy")
	// ErrNotStarted is returned by GuestRuntime only when no guest side effect
	// can have happened. Any other nil-process error is an uncertain start.
	ErrNotStarted = errors.New("agentexec: guest shell was not started")
)

// Mode selects the request body semantics. It is not authority.
type Mode uint8

const (
	ModeFull       Mode = 1 // bytes go to the fixed guest shell stdin only
	ModeRestricted Mode = 2 // helper bodies are undefined and always rejected
)

type Stream uint8

const (
	Stdout Stream = 1
	Stderr Stream = 2
)

type Phase uint8

const (
	PhaseStarting Phase = iota + 1
	PhaseRunning
	PhaseExited       // guest shell exit observed without a stop request
	PhaseCanceled     // stopped by cancel, timeout, revoke, deadline or transport loss
	PhaseFailed       // runtime or partial-start failure
	PhaseStartUnknown // start returned no process and no ErrNotStarted proof
)

// Cleanup is reported separately from the exit status.
type Cleanup uint8

const (
	CleanupPending Cleanup = iota + 1
	CleanupConfirmed
	CleanupUnknown
	CleanupNotRequired // runtime proved ErrNotStarted
)

type StopReason uint8

const (
	StopNone StopReason = iota
	StopCanceled
	StopCommandTimeout
	StopRevoked
	StopHardDeadline
	StopTransportLost
	StopRuntimeFault
)

// Limits bound memory and request sizes. Zero values select the defaults.
type Limits struct {
	MaxCommandBytes int    // request body copied once to the guest stdin
	MaxRetained     int    // unacknowledged output, including record overhead
	MaxTotalOutput  uint64 // bytes assigned cursor offsets; later output is dropped
	MaxReply        int    // data bytes in one Output reply
	MaxCommands     int    // command records kept for idempotent replay
	MaxTimeout      uint32 // milliseconds; the grant deadline still applies
}

func (l Limits) normalized() Limits {
	if l.MaxCommandBytes <= 0 {
		l.MaxCommandBytes = 64 << 10
	}
	if l.MaxRetained <= 0 {
		l.MaxRetained = 1 << 20
	}
	if l.MaxTotalOutput == 0 {
		l.MaxTotalOutput = 16 << 20
	}
	if l.MaxReply <= 0 {
		l.MaxReply = 64 << 10
	}
	if l.MaxCommands <= 0 {
		l.MaxCommands = 32
	}
	if l.MaxTimeout == 0 {
		l.MaxTimeout = 30 * 60 * 1000
	}
	return l
}

type StartRequest struct {
	Version       uint16
	Binding       supervisor.Binding
	RequestID     string
	Mode          Mode
	Command       []byte
	TimeoutMillis uint32
}

type StatusRequest struct {
	Version   uint16
	Binding   supervisor.Binding
	CommandID string
}

type CancelRequest = StatusRequest
type CleanupRequest = StatusRequest

type OutputRequest struct {
	Version   uint16
	Binding   supervisor.Binding
	CommandID string
	// Cursor acknowledges every byte before it; the reply starts at Cursor.
	Cursor   uint64
	MaxBytes int
}

// ExitStatus is the command's natural outcome only: Known is set when the guest
// shell exited before any stop request. A forced termination reports Stop and
// leaves Exit unknown rather than presenting the kill code as the result.
type ExitStatus struct {
	Known bool
	Code  int32
}

type Status struct {
	CommandID string
	Phase     Phase
	Exit      ExitStatus
	Stop      StopReason
	Cleanup   Cleanup
	Terminal  bool   // the command worker returned; exit and cleanup are final unless Cleanup is retried
	OutputEnd uint64 // next cursor that output would receive
	Truncated bool
	Dropped   uint64
}

type Chunk struct {
	Stream Stream
	Offset uint64
	Data   []byte
}

type Output struct {
	Chunks []Chunk
	Next   uint64
	// Gap counts bytes between Cursor and the first retained byte that the
	// client acknowledged earlier or that were released by a later Start.
	Gap       uint64
	Truncated bool
	Dropped   uint64
	Complete  bool // no further output will be assigned to this command
}

// GuestRuntime is the protected owner's fixed guest shell launcher. It takes no
// argv, path, user, SID or token from the request. The only request-derived
// input is stdin, which it writes to the fixed shell of its profile: the
// non-administrator guest shell unless it implements ProfiledRuntime.
// StartShell receives a finite Deadline bounded by the command timeout, grant
// hard deadline and original Session elapsed cap. This metadata creates no new
// timer: Session still owns cancellation and canonical revoke-before-cancel.
// A non-nil process returned with an error remains owned by the caller.
type GuestRuntime interface {
	StartShell(ctx context.Context, grant supervisor.Binding, stdin []byte) (GuestProcess, error)
}

// GuestProcess methods must honor ctx; Terminate must be idempotent and safe to
// call concurrently with Next so that a revoked grant unblocks a pending read.
// Release confirms process/Job exit and owned handle closure; it is retried on
// error with the same process and never replaced by another owner.
type GuestProcess interface {
	Next(ctx context.Context) (Stream, []byte, error) // io.EOF after both streams end
	Wait(ctx context.Context) (ExitStatus, error)
	Terminate(ctx context.Context) error
	Release(ctx context.Context) error
}

func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// digest is the canonical payload identity used for request ID idempotency.
func (r StartRequest) digest() [32]byte {
	h := sha256.New()
	field := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	var fixed [15]byte
	binary.BigEndian.PutUint16(fixed[0:], r.Version)
	binary.BigEndian.PutUint64(fixed[2:], r.Binding.Generation)
	fixed[10] = byte(r.Mode)
	binary.BigEndian.PutUint32(fixed[11:], r.TimeoutMillis)
	h.Write(fixed[:])
	field([]byte(r.Binding.Identity.LaunchID))
	field([]byte(r.Binding.Identity.Epoch))
	field([]byte(r.Binding.Identity.PolicyDigest))
	field([]byte(r.RequestID))
	field(r.Command)
	var d [32]byte
	h.Sum(d[:0])
	return d
}
