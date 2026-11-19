package agentgateway

import "fmt"

// Role is what a configured principal may do. Every principal has one or
// both; a document without roles, or with an unknown one, is refused.
type Role string

const (
	// RoleRescueOperator may ask a host that accepts rescue for a connection:
	// today the recipient-approved temporary dispatch (Connect). It never
	// reaches a resident host, even when the same principal also owns one.
	RoleRescueOperator Role = "rescue_operator"
	// RoleResidentOwner may pair its own host and send resident work to it.
	RoleResidentOwner Role = "resident_owner"
)

// Assurance is the IdP authentication strength a role requires.
type Assurance string

const (
	// AssuranceAny requires nothing beyond a valid token, as before roles.
	AssuranceAny Assurance = "any"
	// AssuranceMultiFactor is recognized but refused at start until the
	// gateway can check the token's factors (#398). Accepting it unchecked
	// would let a single-factor token through a role that asked for more.
	AssuranceMultiFactor Assurance = "multi_factor"
)

// RoleError is a refusal by a role rule. Reason is a fixed message that names
// the refused entry by its position in the document, never by label, subject
// or key, so the entry may print it as a diagnostic. It matches ErrConfig.
type RoleError struct{ Reason string }

func (e *RoleError) Error() string { return e.Reason }
func (e *RoleError) Unwrap() error { return ErrConfig }

func rolesError(i int) error {
	return &RoleError{Reason: fmt.Sprintf("gateway の principals の %d 番目の roles がないか不正です。rescue_operator と resident_owner から 1 つ以上を重複なく書いてください", i+1)}
}

// principalRoles reports the roles of one principal, or false when there is
// none, an unknown one or a repeated one.
func principalRoles(roles []Role) (rescue, resident, ok bool) {
	for _, r := range roles {
		switch {
		case r == RoleRescueOperator && !rescue:
			rescue = true
		case r == RoleResidentOwner && !resident:
			resident = true
		default:
			return false, false, false
		}
	}
	return rescue, resident, rescue || resident
}

// checkAssurance refuses an unknown role or strength, and multi_factor until
// the gateway can check it.
func checkAssurance(m map[Role]Assurance) error {
	for role := range m {
		if role != RoleRescueOperator && role != RoleResidentOwner {
			return ErrConfig
		}
	}
	for _, role := range []Role{RoleRescueOperator, RoleResidentOwner} {
		a, has := m[role]
		if !has {
			continue
		}
		switch a {
		case AssuranceAny:
		case AssuranceMultiFactor:
			return &RoleError{Reason: fmt.Sprintf("gateway の role_assurance で %s に multi_factor を求めていますが、多要素の検査がまだ無いため起動しません（#398）", role)}
		default:
			return &RoleError{Reason: fmt.Sprintf("gateway の role_assurance の %s に知らない値があります。any か multi_factor を書いてください", role)}
		}
	}
	return nil
}
