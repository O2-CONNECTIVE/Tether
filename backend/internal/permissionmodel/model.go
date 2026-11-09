// Package permissionmodel compares explicit permission bounds without producing
// authority. It has no runtime caller, authentication, persistence, clock,
// transport, key, grant or policy loader. WithinBounds is not a capability.
//
// A future trusted caller must authenticate the user AND client, prove host
// identity, obtain current authoritative policy/revocation state, supply a
// trusted time and serialize the decision with admission. These inputs do not
// prove any of those facts. Revisions only compare supplied values; stale pairs
// of matching snapshots are not detected. Offline revocation and refresh policy
// remain unresolved. No decision is cached or changes existing Tether gates.
package permissionmodel

// IDs are distinct opaque internal identifiers, not email addresses, account
// names, keys, OAuth subjects, session IDs, or caller-selected host paths.
type HostID string
type UserID string
type ClientID string

type Binding struct {
	Host   HostID
	User   UserID
	Client ClientID
}

type Mode uint8

const (
	Temporary Mode = iota + 1
	Persistent
)

// Operation names are model categories, not newly published MCP operations.
// DiscoverHost and ManageSharing in particular have no product API here.
// A request names exactly one category; combinations and unknown bits refuse.
type Operation uint16

const (
	DiscoverHost Operation = 1 << iota
	ReadStatus
	ReadResult
	Execute
	Cancel
	ManageSharing
)

type Actions uint16

const allActions = Actions(DiscoverHost | ReadStatus | ReadResult | Execute | Cancel | ManageSharing)

// Limits describe one already-selected exact binding and a finite decision
// window [ValidFromMillis, ValidUntilMillis). This is not a host connection
// lifetime, enrollment expiry, token, cryptographic grant or grant issuer.
// Renewal, local approval, ownership and SSO mapping are outside this model.
// Zero/unknown fields refuse. Empty Actions never imply a default permission.
type Limits struct {
	Binding                           Binding
	Mode                              Mode
	Revision                          uint64
	Actions                           Actions
	ValidFromMillis, ValidUntilMillis int64
	Revoked                           bool
}

// Distinct wrappers prevent passing account bounds as host bounds implicitly.
type HostPolicy struct{ Limits }
type AccountPolicy struct{ Limits }

// Expected revisions must come from the trusted admission context, not tool
// arguments. Temporary mode requires no account policy, but still requires
// authenticated User and Client identities and exact host permission bounds.
type Request struct {
	Binding                       Binding
	Mode                          Mode
	Operation                     Operation
	HostRevision, AccountRevision uint64
}

type Outcome uint8

const (
	Denied Outcome = iota
	WithinBounds
)

// Reasons are internal comparison outcomes, not an external error schema.
// They must not expose another user's host/policy existence through an API.
type Reason uint8

const (
	InvalidInput Reason = iota
	BoundsSatisfied
	BindingMismatch
	ModeMismatch
	RevisionMismatch
	Revoked
	OutsideWindow
	OperationExcluded
)

type Decision struct {
	Outcome Outcome
	Reason  Reason
}

// Assess is deterministic and side-effect free. It does not check possession,
// signatures, issuer claims, live revocation, replay or physical stop/cleanup.
// Persistent requests require both independent bounds. Either side can refuse.
// Temporary requests cannot fall back from persistent policy or manage sharing.
func Assess(nowMillis int64, r Request, h HostPolicy, a AccountPolicy) Decision {
	if nowMillis <= 0 || !validBinding(r.Binding) || !validMode(r.Mode) ||
		!validOperation(r.Operation) || r.HostRevision == 0 || !validLimits(h.Limits) {
		return deny(InvalidInput)
	}
	if r.Mode == Temporary {
		if r.AccountRevision != 0 || a != (AccountPolicy{}) {
			return deny(InvalidInput)
		}
	} else if r.AccountRevision == 0 || !validLimits(a.Limits) {
		return deny(InvalidInput)
	}
	if reason := compare(nowMillis, r, h.Limits, r.HostRevision); reason != BoundsSatisfied {
		return deny(reason)
	}
	if r.Mode == Temporary && r.Operation == ManageSharing {
		return deny(OperationExcluded)
	}
	if r.Mode == Persistent {
		if reason := compare(nowMillis, r, a.Limits, r.AccountRevision); reason != BoundsSatisfied {
			return deny(reason)
		}
	}
	return Decision{WithinBounds, BoundsSatisfied}
}

func deny(reason Reason) Decision { return Decision{Denied, reason} }
func compare(now int64, r Request, p Limits, revision uint64) Reason {
	if p.Binding != r.Binding {
		return BindingMismatch
	}
	if p.Mode != r.Mode {
		return ModeMismatch
	}
	if p.Revision != revision {
		return RevisionMismatch
	}
	if p.Revoked {
		return Revoked
	}
	if now < p.ValidFromMillis || now >= p.ValidUntilMillis {
		return OutsideWindow
	}
	if p.Actions&Actions(r.Operation) == 0 {
		return OperationExcluded
	}
	return BoundsSatisfied
}
func validMode(m Mode) bool           { return m == Temporary || m == Persistent }
func validOperation(o Operation) bool { return o != 0 && o&(o-1) == 0 && Actions(o) & ^allActions == 0 }
func validLimits(p Limits) bool {
	return validBinding(p.Binding) && validMode(p.Mode) && p.Revision != 0 &&
		p.Actions != 0 && p.Actions & ^allActions == 0 &&
		p.ValidFromMillis > 0 && p.ValidUntilMillis > p.ValidFromMillis
}
func validBinding(b Binding) bool {
	return validID(string(b.Host)) && validID(string(b.User)) && validID(string(b.Client))
}
func validID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
