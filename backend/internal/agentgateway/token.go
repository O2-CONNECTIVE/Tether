package agentgateway

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
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

// VerifierConfig names the trusted provider inputs. JWKSURL enables bounded
// HTTPS refresh on an unknown kid. Exactly one of JWKS and JWKSURL is set, so a
// key the provider removed from its JWKS is not kept trusted by static config.
type VerifierConfig struct {
	Issuer          string        // exact "iss"; an https URL
	Audience        string        // exact expected "aud" value
	Scope           string        // the one required scope; empty disables the check
	Types           []string      // accepted JOSE "typ" values, for example "at+jwt"
	JWKS            []byte        // optional initial public JWK Set
	JWKSURL         string        // optional HTTPS JWK Set endpoint
	Leeway          time.Duration // clock skew tolerance, 0..MaxLeeway
	Clock           func() time.Time
	RefusedAudience string // tokens naming this audience are refused (the pair route's)
}

// Identity is an authenticated token holder. It carries no authority by
// itself; authorization maps Subject to a configured principal.
type Identity struct {
	Issuer  string
	Subject string
	Expiry  time.Time
}

// Verifier is a resource-server bearer token verifier for compact JWS access
// tokens. It never issues or forwards tokens; it fetches JWKS only when an
// unknown kid arrives and a JWKS URL is configured.
type Verifier struct {
	issuer, audience, scope string
	refusedAudience         string
	types                   map[string]bool
	keysMu                  sync.RWMutex
	keys                    map[string]verificationKey
	refresh                 *jwksRefresh
	leeway                  time.Duration
	clock                   func() time.Time
	maxLifetime             time.Duration
	singleAudience          bool
}

type verificationKey struct {
	alg string
	rsa *rsa.PublicKey
	ec  *ecdsa.PublicKey
	ed  ed25519.PublicKey
}

// NewVerifier validates every input and refuses a JWK Set that contains
// private key members, symmetric keys or unsupported signing algorithms.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if !httpsURL(cfg.Issuer, false) || !printable(cfg.Audience) || (cfg.Scope != "" && !token68Text(cfg.Scope)) ||
		(cfg.JWKSURL != "" && !httpsURL(cfg.JWKSURL, false)) || len(cfg.Types) == 0 || cfg.Leeway < 0 || cfg.Leeway > MaxLeeway ||
		(len(cfg.JWKS) == 0) == (cfg.JWKSURL == "") {
		return nil, ErrConfig
	}
	types := map[string]bool{}
	for _, t := range cfg.Types {
		if !printable(t) {
			return nil, ErrConfig
		}
		types[normalType(t)] = true
	}
	keys := map[string]verificationKey{}
	if len(cfg.JWKS) != 0 {
		var err error
		keys, err = parseJWKS(cfg.JWKS, false)
		if err != nil {
			return nil, err
		}
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	v := &Verifier{issuer: cfg.Issuer, audience: cfg.Audience, scope: cfg.Scope, refusedAudience: cfg.RefusedAudience, types: types, keys: keys, leeway: cfg.Leeway, clock: clock}
	if cfg.JWKSURL != "" {
		v.refresh = newJWKSRefresh(cfg.JWKSURL)
	}
	return v, nil
}

// Audience is the exact value tokens must name in "aud".
func (v *Verifier) Audience() string { return v.audience }

// Issuer is the one trusted authorization server.
func (v *Verifier) Issuer() string { return v.issuer }

// Scope is the required scope; empty means no scope check.
func (v *Verifier) Scope() string { return v.scope }

var b64 = base64.RawURLEncoding.Strict()

// Verify authenticates one compact JWS. The signature is checked with the key
// selected by "kid" from the configured or explicitly refreshed set, and "alg"
// must equal that key's configured algorithm; header-supplied keys and URLs are refused.
func (v *Verifier) Verify(token string) (Identity, error) {
	if v == nil {
		return Identity{}, ErrToken
	}
	keys := v.keySnapshot()
	if kid, ok := jwsKeyID(token); ok && v.refresh != nil {
		if _, known := keys[kid]; !known {
			v.refreshUnknownKey(kid)
			keys = v.keySnapshot()
		}
	}
	typ, payload, ok := verifyJWS(token, keys, true, "alg", "kid", "typ", "x5t", "x5t#S256")
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
	case "ES256":
		digest := sha256.Sum256(signed)
		if len(sig) != 64 || key.ec == nil || !ecdsa.Verify(key.ec, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
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
	if !ok1 || !ok2 || !ok3 || !ok4 || iss != v.issuer || !printable(sub) || !audienceListed(c["aud"], v.audience, true) ||
		(v.refusedAudience != "" && audienceListed(c["aud"], v.refusedAudience, true)) ||
		(v.singleAudience && !singleAudience(c["aud"], v.audience)) {
		return Identity{}, ErrToken
	}
	now := v.clock()
	maxLifetime := v.maxLifetime
	if maxLifetime == 0 {
		maxLifetime = MaxTokenLifetime
	}
	if !exp.After(iat) || exp.Sub(iat) > maxLifetime || !now.Before(exp.Add(v.leeway)) || iat.After(now.Add(v.leeway)) {
		return Identity{}, ErrToken
	}
	if raw, ok := c["nbf"]; ok {
		nbf, ok := numericDate(raw)
		if !ok || nbf.After(now.Add(v.leeway)) {
			return Identity{}, ErrToken
		}
	}
	if v.scope != "" {
		scopes, ok := scopeSet(c)
		if !ok {
			return Identity{}, ErrToken
		}
		if !scopes[v.scope] {
			return Identity{}, ErrScope
		}
	}
	return Identity{Issuer: iss, Subject: sub, Expiry: exp}, nil
}

func singleAudience(raw json.RawMessage, want string) bool {
	if s, ok := str(raw); ok {
		return s == want
	}
	var list []json.RawMessage
	if firstByte(raw) != '[' || json.Unmarshal(raw, &list) != nil || len(list) != 1 {
		return false
	}
	s, ok := str(list[0])
	return ok && s == want
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

// AccessVerifierConfig names the Access application's trust inputs. Unlike
// OAuth bearer mode, nothing is discovered or fetched: the team issuer, the
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
		m, err := agentwire.Fields(item, "kty", "kid", "alg", "use", "n", "e", "crv", "x", "y", "x5t", "x5t#S256", "x5c", "ext", "key_ops")
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
		// Supabase Auth publishes WebCrypto members: ext (boolean) and key_ops,
		// which for a published verification key is exactly ["verify"]. The
		// Access key contract keeps refusing both.
		_, hasExt := m["ext"]
		_, hasOps := m["key_ops"]
		if rsaOnly && (hasExt || hasOps) {
			return nil, ErrConfig
		}
		if ext, ok := m["ext"]; ok {
			var b bool
			if json.Unmarshal(ext, &b) != nil {
				return nil, ErrConfig
			}
		}
		if ops, ok := m["key_ops"]; ok {
			var list []string
			if json.Unmarshal(ops, &list) != nil || len(list) != 1 || list[0] != "verify" {
				return nil, ErrConfig
			}
		}
		var key verificationKey
		switch {
		case kty == "RSA" && alg == "RS256":
			key.rsa, err = rsaKey(m["n"], m["e"])
		case kty == "EC" && alg == "ES256" && !rsaOnly:
			key.ec, err = p256Key(m["crv"], m["x"], m["y"])
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

func p256Key(rawCrv, rawX, rawY json.RawMessage) (*ecdsa.PublicKey, error) {
	crv, ok1 := str(rawCrv)
	xs, ok2 := str(rawX)
	ys, ok3 := str(rawY)
	if !ok1 || !ok2 || !ok3 || crv != "P-256" {
		return nil, ErrConfig
	}
	x, err1 := b64.DecodeString(xs)
	y, err2 := b64.DecodeString(ys)
	curve := elliptic.P256()
	if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
		return nil, ErrConfig
	}
	public := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	if !curve.IsOnCurve(public.X, public.Y) {
		return nil, ErrConfig
	}
	return public, nil
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
