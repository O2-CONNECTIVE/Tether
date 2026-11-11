package agentgateway

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

const (
	MaxTokenBytes    = 8 << 10
	MaxLeeway        = time.Minute
	MaxTokenLifetime = 24 * time.Hour
	maxKeys          = 16
	maxAudiences     = 16
	maxText          = 256
	minRSABits       = 2048
	maxRSABits       = 8192
)

var (
	ErrConfig = errors.New("agentgateway: untrusted or incomplete configuration")
	// ErrToken means the bearer token or Access assertion was not
	// authenticated: malformed, unknown key, bad signature, wrong issuer,
	// audience or type, or outside its validity window. A bearer token maps to
	// 401 invalid_token, an Access assertion to 403 without a challenge.
	ErrToken = errors.New("agentgateway: bearer token is not valid")
	// ErrScope means an authenticated token lacks the required scope. It maps
	// to 403 insufficient_scope.
	ErrScope = errors.New("agentgateway: bearer token lacks the required scope")
)

// VerifierConfig names the existing provider's trust inputs. Nothing here is
// discovered, fetched or defaulted: the issuer, this resource's audience, the
// scope, the accepted token types and the public verification keys must all
// come from the provider registration that the operator already holds.
type VerifierConfig struct {
	Issuer   string        // exact "iss"; an https URL
	Audience string        // this resource's canonical URI; must appear in "aud"
	Scope    string        // the one scope this resource requires
	Types    []string      // accepted JOSE "typ" values, for example "at+jwt"
	JWKS     []byte        // JWK Set with public RS256 or EdDSA (Ed25519) keys only
	Leeway   time.Duration // clock skew tolerance, 0..MaxLeeway
	Clock    func() time.Time
}

// Identity is an authenticated token holder. It carries no authority by
// itself; authorization maps Subject to a configured principal.
type Identity struct {
	Issuer  string
	Subject string
	Expiry  time.Time
}

// Verifier is a resource-server bearer token verifier for compact JWS access
// tokens. It never issues tokens, contacts the provider or forwards a token.
type Verifier struct {
	issuer, audience, scope string
	types                   map[string]bool
	keys                    map[string]verificationKey
	leeway                  time.Duration
	clock                   func() time.Time
}

type verificationKey struct {
	alg string
	rsa *rsa.PublicKey
	ed  ed25519.PublicKey
}

// NewVerifier validates every input and refuses a JWK Set that contains
// private key members, symmetric keys or algorithms other than RS256/EdDSA.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if !httpsURL(cfg.Issuer, false) || !httpsURL(cfg.Audience, true) || !token68Text(cfg.Scope) ||
		len(cfg.Types) == 0 || cfg.Leeway < 0 || cfg.Leeway > MaxLeeway {
		return nil, ErrConfig
	}
	types := map[string]bool{}
	for _, t := range cfg.Types {
		if !printable(t) {
			return nil, ErrConfig
		}
		types[normalType(t)] = true
	}
	keys, err := parseJWKS(cfg.JWKS, false)
	if err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Verifier{issuer: cfg.Issuer, audience: cfg.Audience, scope: cfg.Scope, types: types, keys: keys, leeway: cfg.Leeway, clock: clock}, nil
}

// Audience is the resource URI that tokens must name.
func (v *Verifier) Audience() string { return v.audience }

// Issuer is the one trusted authorization server.
func (v *Verifier) Issuer() string { return v.issuer }

// Scope is the scope this resource requires.
func (v *Verifier) Scope() string { return v.scope }

var b64 = base64.RawURLEncoding.Strict()

// Verify authenticates one compact JWS. The signature is checked with the key
// selected by "kid" from the configured set only, and "alg" must equal that
// key's configured algorithm; header-supplied keys and URLs are refused.
func (v *Verifier) Verify(token string) (Identity, error) {
	if v == nil {
		return Identity{}, ErrToken
	}
	typ, payload, ok := verifyJWS(token, v.keys, true, "alg", "kid", "typ", "x5t", "x5t#S256")
	if !ok || !v.types[normalType(typ)] {
		return Identity{}, ErrToken
	}
	return v.claims(payload)
}

// verifyJWS checks one compact JWS against the configured keys only and
// returns its "typ" and the signed payload. Access alone permits an absent typ
// (interpreted as JWT); OAuth requires it. Present malformed values are refused.
// The original signed bytes are never changed. The header may hold only
// the named members, so "jwk", "jku", "x5u", "crit" and the like are refused.
func verifyJWS(token string, keys map[string]verificationKey, typeRequired bool, header ...string) (string, []byte, bool) {
	if len(token) == 0 || len(token) > MaxTokenBytes {
		return "", nil, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", nil, false
	}
	raw, err1 := b64.DecodeString(parts[0])
	payload, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || agentwire.CheckJSON(raw, false) != nil {
		return "", nil, false
	}
	h, err := agentwire.Fields(raw, header...)
	if err != nil {
		return "", nil, false
	}
	alg, ok1 := str(h["alg"])
	kid, ok2 := str(h["kid"])
	typ, ok3 := str(h["typ"])
	if _, has := h["typ"]; !typeRequired && !has {
		typ, ok3 = "JWT", true
	}
	key, known := keys[kid]
	if !ok1 || !ok2 || !ok3 || !known || key.alg != alg {
		return "", nil, false
	}
	signed := []byte(parts[0] + "." + parts[1])
	switch alg {
	case "RS256":
		digest := sha256.Sum256(signed)
		if len(sig) != key.rsa.Size() || rsa.VerifyPKCS1v15(key.rsa, crypto.SHA256, digest[:], sig) != nil {
			return "", nil, false
		}
	case "EdDSA":
		if len(sig) != ed25519.SignatureSize || !ed25519.Verify(key.ed, signed, sig) {
			return "", nil, false
		}
	default:
		return "", nil, false
	}
	return typ, payload, true
}

func (v *Verifier) claims(payload []byte) (Identity, error) {
	if agentwire.CheckJSON(payload, true) != nil {
		return Identity{}, ErrToken
	}
	c, err := agentwire.Fields(payload)
	if err != nil {
		return Identity{}, ErrToken
	}
	iss, ok1 := str(c["iss"])
	sub, ok2 := str(c["sub"])
	exp, ok3 := numericDate(c["exp"])
	iat, ok4 := numericDate(c["iat"])
	if !ok1 || !ok2 || !ok3 || !ok4 || iss != v.issuer || !printable(sub) || !audienceListed(c["aud"], v.audience, true) {
		return Identity{}, ErrToken
	}
	now := v.clock()
	if !exp.After(iat) || exp.Sub(iat) > MaxTokenLifetime || !now.Before(exp.Add(v.leeway)) || iat.After(now.Add(v.leeway)) {
		return Identity{}, ErrToken
	}
	if raw, ok := c["nbf"]; ok {
		nbf, ok := numericDate(raw)
		if !ok || nbf.After(now.Add(v.leeway)) {
			return Identity{}, ErrToken
		}
	}
	scopes, ok := scopeSet(c)
	if !ok {
		return Identity{}, ErrToken
	}
	if !scopes[v.scope] {
		return Identity{}, ErrScope
	}
	return Identity{Issuer: iss, Subject: sub, Expiry: exp}, nil
}

// audienceListed requires want in "aud": an array of 1..maxAudiences strings,
// or, only when allowString, a single string.
func audienceListed(raw json.RawMessage, want string, allowString bool) bool {
	if s, ok := str(raw); ok {
		return allowString && s == want
	}
	var list []json.RawMessage
	if firstByte(raw) != '[' || json.Unmarshal(raw, &list) != nil || len(list) == 0 || len(list) > maxAudiences {
		return false
	}
	found := false
	for _, item := range list {
		s, ok := str(item)
		if !ok {
			return false
		}
		found = found || s == want
	}
	return found
}

// scopeSet reads "scope" (RFC 9068, space-delimited) or "scp" (a string or an
// array of strings). Both members at once are ambiguous and refused.
func scopeSet(c map[string]json.RawMessage) (map[string]bool, bool) {
	scope, hasScope := c["scope"]
	scp, hasScp := c["scp"]
	set := map[string]bool{}
	switch {
	case hasScope && hasScp:
		return nil, false
	case hasScope:
		s, ok := str(scope)
		if !ok {
			return nil, false
		}
		for _, f := range strings.Fields(s) {
			set[f] = true
		}
	case hasScp:
		if s, ok := str(scp); ok {
			for _, f := range strings.Fields(s) {
				set[f] = true
			}
			break
		}
		var list []json.RawMessage
		if firstByte(scp) != '[' || json.Unmarshal(scp, &list) != nil {
			return nil, false
		}
		for _, item := range list {
			s, ok := str(item)
			if !ok {
				return nil, false
			}
			set[s] = true
		}
	}
	return set, true
}

// HeaderAccessAssertion carries the Cloudflare Access application token that
// the Access edge adds after it has resolved the caller's opaque Managed OAuth
// token. The header alone proves nothing; only its signature does.
const HeaderAccessAssertion = "Cf-Access-Jwt-Assertion"

// AccessVerifierConfig names the Access application's trust inputs. Like
// VerifierConfig nothing is discovered or fetched: the team issuer, the
// application AUD tag and the team's public keys come from the operator's
// Access application, and key rotation is a protected configuration update.
type AccessVerifierConfig struct {
	Issuer string        // exact "iss": the team-domain URL, "https://<team-domain>" without a path
	AUD    string        // the Access application AUD tag; never the MCP resource URL
	Keys   []byte        // JWK Set holding only the team's RSA RS256 public keys ("keys" of its certs document)
	Leeway time.Duration // clock skew tolerance, 0..MaxLeeway
	Clock  func() time.Time
}

// AccessVerifier authenticates the Access application token of one Access
// application. It is a separate mode from Verifier: it accepts no OAuth
// access token, reads no scope and never falls back to the bearer verifier.
type AccessVerifier struct {
	rotation    *accessKeyRotation // nil for the existing static verifier
	issuer, aud string
	keys        map[string]verificationKey
	leeway      time.Duration
	clock       func() time.Time
}

// NewAccessVerifier validates every input and refuses keys other than public
// RSA keys for RS256.
func NewAccessVerifier(cfg AccessVerifierConfig) (*AccessVerifier, error) {
	if !accessIssuer(cfg.Issuer) || !accessAUD(cfg.AUD) || cfg.Leeway < 0 || cfg.Leeway > MaxLeeway {
		return nil, ErrConfig
	}
	keys, err := parseJWKS(cfg.Keys, true)
	if err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &AccessVerifier{issuer: cfg.Issuer, aud: cfg.AUD, keys: keys, leeway: cfg.Leeway, clock: clock}, nil
}

// Verify authenticates one assertion: RS256 with a configured kid, header
// members "alg", "kid" and optional "typ" (JWT when present) only, payload "type" app, the exact
// issuer, the AUD tag listed in an "aud" array, and integer "exp", "iat" and
// "nbf" within bounds. Only an identity-based user token passes: a non-empty
// "sub" and an "email" are required and a service token ("common_name") is
// refused. Email is checked for presence only and never identifies anyone.
func (v *AccessVerifier) Verify(assertion string) (Identity, error) {
	if v == nil {
		return Identity{}, ErrToken
	}
	if v.rotation != nil {
		return v.rotation.verify(v, assertion)
	}
	return v.verifyWithKeys(assertion, v.keys)
}

func (v *AccessVerifier) verifyWithKeys(assertion string, keys map[string]verificationKey) (Identity, error) {
	typ, payload, ok := verifyJWS(assertion, keys, false, "alg", "kid", "typ")
	if !ok || normalType(typ) != "jwt" || agentwire.CheckJSON(payload, true) != nil {
		return Identity{}, ErrToken
	}
	c, err := agentwire.Fields(payload)
	if err != nil {
		return Identity{}, ErrToken
	}
	iss, ok1 := str(c["iss"])
	sub, ok2 := str(c["sub"])
	kind, ok3 := str(c["type"])
	email, ok4 := str(c["email"])
	exp, ok5 := numericDate(c["exp"])
	iat, ok6 := numericDate(c["iat"])
	nbf, ok7 := numericDate(c["nbf"])
	_, service := c["common_name"]
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 || service || iss != v.issuer || kind != "app" ||
		!printable(sub) || len(email) > maxText || strings.IndexByte(email, '@') < 1 || !audienceListed(c["aud"], v.aud, false) {
		return Identity{}, ErrToken
	}
	now := v.clock()
	if !exp.After(iat) || exp.Sub(iat) > MaxTokenLifetime || !now.Before(exp.Add(v.leeway)) ||
		iat.After(now.Add(v.leeway)) || nbf.After(now.Add(v.leeway)) {
		return Identity{}, ErrToken
	}
	return Identity{Issuer: iss, Subject: sub, Expiry: exp}, nil
}

// accessIssuer is a lower-case https origin without port, path or slash, the
// form of the Access team-domain "iss".
func accessIssuer(s string) bool {
	u, err := url.Parse(s)
	return err == nil && httpsURL(s, false) && u.Path == "" && u.RawPath == "" && u.Port() == "" &&
		strings.ToLower(u.Host) == u.Host && u.String() == s
}

// accessAUD is an opaque tag of letters, digits, '-' and '_' (Cloudflare shows
// 64 hex digits), so a URL such as the resource can never be configured.
func accessAUD(s string) bool {
	if len(s) == 0 || len(s) > maxText {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// parseJWKS accepts only public signature keys with an explicit algorithm.
// rsaOnly is the Access key contract: every key is RSA for RS256, and "alg",
// which the mode already fixes, may be absent but never names another one.
func parseJWKS(raw []byte, rsaOnly bool) (map[string]verificationKey, error) {
	if agentwire.CheckJSON(raw, false) != nil {
		return nil, ErrConfig
	}
	top, err := agentwire.Fields(raw, "keys")
	if err != nil {
		return nil, ErrConfig
	}
	var list []json.RawMessage
	if firstByte(top["keys"]) != '[' || json.Unmarshal(top["keys"], &list) != nil || len(list) == 0 || len(list) > maxKeys {
		return nil, ErrConfig
	}
	keys := map[string]verificationKey{}
	for _, item := range list {
		all, err := agentwire.Fields(item)
		if err != nil {
			return nil, ErrConfig
		}
		for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
			if _, ok := all[private]; ok {
				return nil, errors.Join(ErrConfig, errors.New("agentgateway: the JWK Set contains private or symmetric key material"))
			}
		}
		m, err := agentwire.Fields(item, "kty", "kid", "alg", "use", "n", "e", "crv", "x", "x5t", "x5t#S256", "x5c")
		if err != nil {
			return nil, ErrConfig
		}
		kty, ok1 := str(m["kty"])
		kid, ok2 := str(m["kid"])
		alg, ok3 := str(m["alg"])
		if _, has := m["alg"]; rsaOnly && !has {
			alg, ok3 = "RS256", true
		}
		if _, dup := keys[kid]; !ok1 || !ok2 || !ok3 || !printable(kid) || dup {
			return nil, ErrConfig
		}
		if use, ok := m["use"]; ok {
			if s, ok := str(use); !ok || s != "sig" {
				return nil, ErrConfig
			}
		}
		var key verificationKey
		switch {
		case kty == "RSA" && alg == "RS256":
			key.rsa, err = rsaKey(m["n"], m["e"])
		case kty == "OKP" && alg == "EdDSA" && !rsaOnly:
			key.ed, err = edKey(m["crv"], m["x"])
		default:
			err = ErrConfig
		}
		if err != nil {
			return nil, ErrConfig
		}
		key.alg = alg
		keys[kid] = key
	}
	return keys, nil
}

func rsaKey(rawN, rawE json.RawMessage) (*rsa.PublicKey, error) {
	ns, ok1 := str(rawN)
	es, ok2 := str(rawE)
	if !ok1 || !ok2 {
		return nil, ErrConfig
	}
	n, err1 := b64.DecodeString(ns)
	e, err2 := b64.DecodeString(es)
	if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 || e[0] == 0 || len(n) == 0 || n[0] == 0 {
		return nil, ErrConfig
	}
	exp := 0
	for _, b := range e {
		exp = exp<<8 | int(b)
	}
	modulus := new(big.Int).SetBytes(n)
	if exp < 3 || exp%2 == 0 || modulus.BitLen() < minRSABits || modulus.BitLen() > maxRSABits {
		return nil, ErrConfig
	}
	return &rsa.PublicKey{N: modulus, E: exp}, nil
}

func edKey(rawCrv, rawX json.RawMessage) (ed25519.PublicKey, error) {
	crv, ok1 := str(rawCrv)
	xs, ok2 := str(rawX)
	if !ok1 || !ok2 || crv != "Ed25519" {
		return nil, ErrConfig
	}
	x, err := b64.DecodeString(xs)
	if err != nil || len(x) != ed25519.PublicKeySize {
		return nil, ErrConfig
	}
	return ed25519.PublicKey(x), nil
}

// numericDate accepts an integer NumericDate only. Fractions, exponents,
// negative values and values beyond 2^53 are refused.
func numericDate(raw json.RawMessage) (time.Time, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || len(s) > 16 || strings.Trim(s, "0123456789") != "" || (len(s) > 1 && s[0] == '0') {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > 1<<53 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

func str(raw json.RawMessage) (string, bool) {
	var s string
	if firstByte(raw) != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func firstByte(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
		default:
			return c
		}
	}
	return 0
}

// printable is 1..maxText bytes of visible ASCII without spaces.
func printable(s string) bool {
	if len(s) == 0 || len(s) > maxText {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// token68Text is printable without the characters that would break a quoted
// WWW-Authenticate parameter.
func token68Text(s string) bool { return printable(s) && !strings.ContainsAny(s, `"\,`) }

// normalType compares JOSE "typ" case-insensitively with the optional
// "application/" prefix removed (RFC 7515 section 4.1.9).
func normalType(t string) string {
	t = strings.ToLower(t)
	return strings.TrimPrefix(t, "application/")
}

// httpsURL accepts an absolute https URL without credentials, query or
// fragment; requirePath also demands a non-root path (the MCP endpoint).
func httpsURL(s string, requirePath bool) bool {
	u, err := url.Parse(s)
	if err != nil || !printable(s) || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	return !requirePath || (u.Path != "" && u.Path != "/" && !strings.HasSuffix(u.Path, "/"))
}
