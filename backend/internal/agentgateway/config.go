package agentgateway

import (
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// MaxConfigBytes bounds the operator configuration document.
const MaxConfigBytes = 64 << 10

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
	Listen         string            // numeric host:port admitted by Profile; see listenAllowed
	Profile        DeploymentProfile // ProfileLoopback unless the document names another
	Mode           AuthMode
	Resource       string               // the public MCP resource URL in both modes
	Verifier       VerifierConfig       // ModeOAuthBearer only
	Access         AccessVerifierConfig // ModeCloudflareAccess only
	Principals     []Principal          // Signer is never read from the file
	AllowedOrigins []string
}

// Members of each mode. The sets share only the mode-neutral members, so a
// document can never mix the two modes' trust inputs.
var (
	oauthMembers  = []string{"listen", "deployment_profile", "auth_mode", "resource", "issuer", "scope", "token_types", "leeway_seconds", "jwks", "principals", "allowed_origins"}
	accessMembers = []string{"listen", "deployment_profile", "auth_mode", "resource", "access_issuer", "access_aud", "access_keys", "leeway_seconds", "principals", "allowed_origins"}
)

// ParseConfig reads the strict JSON configuration. "auth_mode" is required and
// selects the member set; every member of that set is required except
// deployment_profile and allowed_origins; unknown (including the other
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
	if c.Mode == ModeOAuthBearer {
		ok = ok && decodeInto(m["issuer"], &c.Verifier.Issuer) && decodeInto(m["scope"], &c.Verifier.Scope) &&
			decodeInto(m["token_types"], &c.Verifier.Types) && firstByte(m["jwks"]) == '{'
		c.Verifier.Audience = c.Resource
		c.Verifier.JWKS = append([]byte(nil), m["jwks"]...)
	} else {
		ok = ok && decodeInto(m["access_issuer"], &c.Access.Issuer) && decodeInto(m["access_aud"], &c.Access.AUD) &&
			firstByte(m["access_keys"]) == '{'
		c.Access.Keys = append([]byte(nil), m["access_keys"]...)
	}
	if raw, has := m["allowed_origins"]; has {
		ok = ok && decodeInto(raw, &c.AllowedOrigins)
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
	for _, item := range people {
		pm, err := agentwire.Fields(item, "label", "subject")
		var p Principal
		if err != nil || !decodeInto(pm["label"], &p.Label) || !decodeInto(pm["subject"], &p.Subject) {
			return FileConfig{}, ErrConfig
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
	cfg := Config{Principals: append([]Principal(nil), c.Principals...), AllowedOrigins: c.AllowedOrigins}
	var err error
	switch c.Mode {
	case ModeOAuthBearer:
		if c.Verifier.Audience != c.Resource {
			return Config{}, ErrConfig
		}
		cfg.Verifier, err = NewVerifier(c.Verifier)
	case ModeCloudflareAccess:
		cfg.Access, err = NewAccessVerifier(c.Access)
		cfg.Resource = c.Resource
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
