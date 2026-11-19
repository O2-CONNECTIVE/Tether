package agentgateway

import (
	"encoding/json"
	"net"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// MaxConfigBytes bounds the operator configuration document.
const MaxConfigBytes = 64 << 10

// validRescueIdentity checks the configured display name and organization;
// see docs/rescue-connection-start.md, 名前と組織の出どころ.
func validRescueIdentity(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 64 {
		return false
	}
	bidi := unicode.Properties["Bidi_Control"]
	for _, r := range value {
		if unicode.IsControl(r) || (bidi != nil && unicode.Is(bidi, r)) {
			return false
		}
	}
	return true
}

// AuthMode is the one authentication mode of a configuration document. There
// is no default and no fallback between modes.
type AuthMode string

const (
	// ModeOAuthBearer verifies an existing provider's JWT access token sent as
	// "Authorization: Bearer" (Verifier).
	ModeOAuthBearer AuthMode = "oauth_bearer_jwt"
	// ModeCloudflareAccess verifies the Cloudflare Access application token
	// sent by the Access edge as HeaderAccessAssertion (AccessVerifier).
	ModeCloudflareAccess AuthMode = "cloudflare_access"
)

// DeploymentProfile selects the rule for the listen address. It is read only
// from the protected document, never inferred from the environment, and it
// changes no authentication: both profiles serve the same verifier and the
// same registration proof.
type DeploymentProfile string

const (
	// ProfileLoopback is the default when "deployment_profile" is omitted: a
	// numeric loopback address that a tunnel connector on the same host
	// forwards to.
	ProfileLoopback DeploymentProfile = "loopback"
	// ProfileContainerPrivate is for a dedicated Linux container whose port
	// the host publishes on 127.0.0.1 only. It admits the IPv4 wildcard
	// 0.0.0.0 or one RFC 1918 IPv4 address with a port of 1024 or above.
	// Everything attached to the same bridge can still connect; network
	// separation is not authorization.
	ProfileContainerPrivate DeploymentProfile = "container_private"
)

// FileConfig is the operator's public trust configuration. It holds no
// secret: no signer, client secret, token or private key member is accepted.
type FileConfig struct {
	Listen          string            // numeric host:port admitted by Profile; see listenAllowed
	Profile         DeploymentProfile // ProfileLoopback unless the document names another
	Mode            AuthMode
	Resource        string // the public MCP resource URL in both modes
	Organization    string
	Verifier        VerifierConfig       // ModeOAuthBearer only
	Access          AccessVerifierConfig // ModeCloudflareAccess only
	AccessKeySource string               // empty: static access_keys; cloudflare_certs: bounded HTTPS refresh
	Principals      []Principal          // Signer is never read from the file
	RoleAssurance   map[Role]Assurance   // optional "role_assurance"
	AllowedOrigins  []string
}

// PairingVerifier supplies the pairing-specific verifier configuration.
// Pair-token lifetime and audience cardinality are enforced by the pairing verifier.
func (c FileConfig) PairingVerifier(audience string, types []string) (*Verifier, error) {
	if c.Mode != ModeOAuthBearer {
		return nil, ErrConfig
	}
	base := c.Verifier
	v, err := NewVerifier(VerifierConfig{Issuer: base.Issuer, Audience: audience, Scope: "", Types: types, JWKS: base.JWKS, JWKSURL: base.JWKSURL, Leeway: base.Leeway, Clock: base.Clock})
	if err != nil {
		return nil, err
	}
	v.maxLifetime = PairTokenMaxLifetime
	v.singleAudience = true
	return v, nil
}

// Members of each mode. The sets share only the mode-neutral members, so a
// document can never mix the two modes' trust inputs.
var (
	oauthMembers  = []string{"listen", "deployment_profile", "auth_mode", "resource", "audience", "issuer", "scope", "token_types", "leeway_seconds", "jwks", "jwks_url", "principals", "role_assurance", "allowed_origins", "organization"}
	accessMembers = []string{"listen", "deployment_profile", "auth_mode", "resource", "access_issuer", "access_aud", "access_keys", "access_key_source", "leeway_seconds", "principals", "role_assurance", "allowed_origins", "organization"}
)

// ParseConfig reads the strict JSON configuration. "auth_mode" is required and
// selects the member set; every member of that set is required except
// deployment_profile, role_assurance and allowed_origins. Every principal names
// its roles. Access requires either access_keys
// or access_key_source="cloudflare_certs", never both; unknown (including the other
// mode's), duplicate or null members are refused.
func ParseConfig(raw []byte) (FileConfig, error) {
	if len(raw) == 0 || len(raw) > MaxConfigBytes || agentwire.CheckJSON(raw, false) != nil {
		return FileConfig{}, ErrConfig
	}
	all, err := agentwire.Fields(raw)
	if err != nil {
		return FileConfig{}, ErrConfig
	}
	var c FileConfig
	var allowed []string
	if !decodeInto(all["auth_mode"], &c.Mode) {
		return FileConfig{}, ErrConfig
	}
	switch c.Mode {
	case ModeOAuthBearer:
		allowed = oauthMembers
	case ModeCloudflareAccess:
		allowed = accessMembers
	default:
		return FileConfig{}, ErrConfig
	}
	m, err := agentwire.Fields(raw, allowed...)
	if err != nil {
		return FileConfig{}, ErrConfig
	}
	var leeway int
	ok := decodeInto(m["listen"], &c.Listen) && decodeInto(m["resource"], &c.Resource) && decodeInto(m["leeway_seconds"], &leeway)
	if raw, has := m["organization"]; has {
		ok = ok && firstByte(raw) == '"' && decodeInto(raw, &c.Organization) && validRescueIdentity(c.Organization) && c.Organization != ""
	}
	if c.Mode == ModeOAuthBearer {
		ok = ok && decodeInto(m["issuer"], &c.Verifier.Issuer) && decodeInto(m["scope"], &c.Verifier.Scope) &&
			decodeInto(m["token_types"], &c.Verifier.Types)
		c.Verifier.Audience = c.Resource
		if raw, has := m["audience"]; has {
			ok = ok && firstByte(raw) == '"' && decodeInto(raw, &c.Verifier.Audience)
		}
		if raw, has := m["jwks"]; has {
			ok = ok && firstByte(raw) == '{'
			c.Verifier.JWKS = append([]byte(nil), raw...)
		}
		if raw, has := m["jwks_url"]; has {
			ok = ok && firstByte(raw) == '"' && decodeInto(raw, &c.Verifier.JWKSURL)
		}
		ok = ok && (len(c.Verifier.JWKS) == 0) != (c.Verifier.JWKSURL == "")
	} else {
		ok = ok && decodeInto(m["access_issuer"], &c.Access.Issuer) && decodeInto(m["access_aud"], &c.Access.AUD)
		if source, has := m["access_key_source"]; has {
			ok = ok && decodeInto(source, &c.AccessKeySource) && c.AccessKeySource == "cloudflare_certs" && m["access_keys"] == nil
		} else {
			ok = ok && firstByte(m["access_keys"]) == '{'
			c.Access.Keys = append([]byte(nil), m["access_keys"]...)
		}
	}
	if raw, has := m["allowed_origins"]; has {
		ok = ok && decodeInto(raw, &c.AllowedOrigins)
	}
	if raw, has := m["role_assurance"]; has {
		f, err := agentwire.Fields(raw, string(RoleRescueOperator), string(RoleResidentOwner))
		ok = ok && err == nil && len(f) != 0
		c.RoleAssurance = map[Role]Assurance{}
		for role, v := range f {
			var a Assurance
			ok = ok && firstByte(v) == '"' && decodeInto(v, &a)
			c.RoleAssurance[Role(role)] = a
		}
	}
	c.Profile = ProfileLoopback
	if raw, has := m["deployment_profile"]; has {
		ok = ok && decodeInto(raw, &c.Profile)
	}
	var people []json.RawMessage
	if !ok || !decodeInto(m["principals"], &people) || len(people) == 0 || len(people) > MaxPrincipals ||
		leeway < 0 || leeway > int(MaxLeeway/time.Second) {
		return FileConfig{}, ErrConfig
	}
	for i, item := range people {
		pm, err := agentwire.Fields(item, "label", "subject", "roles", "display_name")
		var p Principal
		if err != nil || !decodeInto(pm["label"], &p.Label) || !decodeInto(pm["subject"], &p.Subject) {
			return FileConfig{}, ErrConfig
		}
		// New checks the values; only the shape is read here.
		if firstByte(pm["roles"]) != '[' || !decodeInto(pm["roles"], &p.Roles) {
			return FileConfig{}, rolesError(i)
		}
		if raw, has := pm["display_name"]; has {
			if firstByte(raw) != '"' || !decodeInto(raw, &p.DisplayName) || !validRescueIdentity(p.DisplayName) || p.DisplayName == "" {
				return FileConfig{}, ErrConfig
			}
		}
		c.Principals = append(c.Principals, p)
	}
	if c.Mode == ModeOAuthBearer {
		c.Verifier.Leeway = time.Duration(leeway) * time.Second
	} else {
		c.Access.Leeway = time.Duration(leeway) * time.Second
	}
	if !listenAllowed(c.Profile, c.Listen) {
		return FileConfig{}, ErrConfig
	}
	// Validate everything now so that a bad document is refused before any
	// signer or registration producer is consulted.
	cfg, err := c.GatewayConfig()
	if err != nil {
		return FileConfig{}, err
	}
	if _, err := New(cfg); err != nil {
		return FileConfig{}, err
	}
	return c, nil
}

// GatewayConfig builds the verifier of the document's one mode and returns a
// Config naming it, the principals (without signers) and the origins. Trusted
// composition adds signers and budgets before New.
func (c FileConfig) GatewayConfig() (Config, error) {
	cfg := Config{Resource: c.Resource, Organization: c.Organization, Principals: append([]Principal(nil), c.Principals...), RoleAssurance: c.RoleAssurance, AllowedOrigins: c.AllowedOrigins}
	var err error
	switch c.Mode {
	case ModeOAuthBearer:
		if c.Verifier.Audience == "" || c.AccessKeySource != "" {
			return Config{}, ErrConfig
		}
		cfg.Verifier, err = NewVerifier(c.Verifier)
	case ModeCloudflareAccess:
		switch c.AccessKeySource {
		case "":
			cfg.Access, err = NewAccessVerifier(c.Access)
		case "cloudflare_certs":
			cfg.Access, err = NewRotatingAccessVerifier(c.Access, ConfiguredAccessKeyRefreshPolicy())
		default:
			err = ErrConfig
		}
	default:
		err = ErrConfig
	}
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func decodeInto(raw json.RawMessage, v any) bool {
	return raw != nil && json.Unmarshal(raw, v) == nil
}

// ListenNetwork is the net.Listen network for Listen. The container profile
// is pinned to "tcp4": with "tcp", Go opens a dual-stack IPv6 socket for
// 0.0.0.0, which would also accept IPv6. The loopback profile keeps "tcp".
func (c FileConfig) ListenNetwork() string {
	switch c.Profile {
	case ProfileLoopback:
		return "tcp"
	case ProfileContainerPrivate:
		return "tcp4"
	}
	return ""
}

// listenAllowed requires a numeric address (no name is resolved) and a
// canonical decimal port from 1 to 65535. The loopback profile admits only a
// loopback address. The container profile admits only a dotted IPv4 literal
// that is 0.0.0.0 or in 10/8, 172.16/12 or 192.168/16, and a port of 1024 or
// above; loopback, IPv6 (including "::" and IPv4-mapped forms), public,
// link-local and shared addresses are refused.
func listenAllowed(profile DeploymentProfile, addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if ip == nil || err != nil || strconv.Itoa(n) != port || n < 1 || n > 65535 {
		return false
	}
	switch profile {
	case ProfileLoopback:
		return ip.IsLoopback()
	case ProfileContainerPrivate:
		v4 := ip.To4()
		return v4 != nil && v4.String() == host && n >= 1024 && (v4.Equal(net.IPv4zero) || v4.IsPrivate())
	}
	return false
}
