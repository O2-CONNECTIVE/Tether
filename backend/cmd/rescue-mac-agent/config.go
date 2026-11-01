package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"golang.org/x/crypto/ssh"
)

var errConfig = errors.New("rescue-mac-agent: invalid local composition document")

const (
	maxPathBytes     = 1024
	maxEndpointBytes = 256
	maxLabelBytes    = 32

	minSession = time.Minute
	// maxSession leaves a margin below the channel's absolute lifetime cap so
	// that the approved deadline is still accepted after the prompt.
	maxSession = agentchannel.MaxClientLifetime - time.Minute
	// minCleanup exceeds macagent's default TERM, KILL and I/O waits (2s each)
	// so that a first cleanup attempt can be confirmed.
	minCleanup = 7 * time.Second
	maxCleanup = time.Minute
)

// localConfig is the recipient's own composition document. It names the
// gateway endpoint and enrolled labels, pins the one gateway principal key
// this Mac will accept, references the local host key by path and an
// independently recorded fingerprint, binds the fixed current-user policy and
// bounds the local runtime. It holds no private byte and no approval.
type localConfig struct {
	endpoint  string
	host      string
	principal string
	allowed   ssh.PublicKey
	hostKey   agentkeys.Config
	workDir   string
	home      string
	session   time.Duration
	cleanup   time.Duration
}

var configMembers = []string{"registration_endpoint", "host_label", "principal_label", "principal_public_key",
	"host_key_path", "host_key_fingerprint", "policy_digest", "work_dir", "home", "session_seconds", "cleanup_seconds"}

// parseLocalConfig validates the whole document before the host key is
// opened. Every member is required; unknown, duplicate and null members are
// refused. configPath is the document's own path, which the key may not share.
func parseLocalConfig(raw []byte, configPath string) (localConfig, error) {
	if len(raw) == 0 || len(raw) > agentkeys.MaxTrustConfigBytes || agentwire.CheckJSON(raw, false) != nil {
		return localConfig{}, errConfig
	}
	m, err := agentwire.Fields(raw, configMembers...)
	defer func() {
		for _, v := range m {
			wipe(v)
		}
	}()
	if err != nil || len(m) != len(configMembers) {
		return localConfig{}, errConfig
	}
	var c localConfig
	var encoded, policy string
	ok := true
	for _, f := range []struct {
		name  string
		limit int
		into  *string
	}{
		{"registration_endpoint", maxEndpointBytes, &c.endpoint}, {"host_label", maxLabelBytes, &c.host},
		{"principal_label", maxLabelBytes, &c.principal}, {"principal_public_key", 128, &encoded},
		{"host_key_path", maxPathBytes, &c.hostKey.Path}, {"host_key_fingerprint", 64, &c.hostKey.Fingerprint},
		{"policy_digest", 64, &policy}, {"work_dir", maxPathBytes, &c.workDir}, {"home", maxPathBytes, &c.home},
	} {
		var good bool
		*f.into, good = text(m[f.name], f.limit)
		ok = ok && good
	}
	sessionSeconds, ok1 := integer(m["session_seconds"])
	cleanupSeconds, ok2 := integer(m["cleanup_seconds"])
	c.session, c.cleanup = time.Duration(sessionSeconds)*time.Second, time.Duration(cleanupSeconds)*time.Second
	var ok3 bool
	c.allowed, ok3 = parsePublicKey(encoded)
	if !ok || !ok1 || !ok2 || !ok3 {
		return localConfig{}, errConfig
	}
	if _, ok := wssEndpoint(c.endpoint); !ok || !validLabel(c.host) || !validLabel(c.principal) || c.host == c.principal ||
		policy != agentexec.MacCurrentUserPolicyDigest() ||
		!keyPath(c.hostKey.Path) || strings.EqualFold(c.hostKey.Path, configPath) || !validFingerprint(c.hostKey.Fingerprint) ||
		ssh.FingerprintSHA256(c.allowed) == c.hostKey.Fingerprint ||
		!localDir(c.workDir) || !localDir(c.home) ||
		c.session < minSession || c.session > maxSession || c.cleanup < minCleanup || c.cleanup > maxCleanup {
		return localConfig{}, errConfig
	}
	return c, nil
}

// wssEndpoint applies agenttransport's endpoint rules before the key is read;
// NewClient checks them again.
func wssEndpoint(s string) (*url.URL, bool) {
	u, err := url.Parse(s)
	if err != nil || !printable(s) || u.Scheme != "wss" || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		u.Path == "" || u.Path == "/" || strings.ToLower(u.Host) != u.Host || u.String() != s {
		return nil, false
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return nil, false
		}
	}
	return u, true
}

// parsePublicKey accepts only the canonical one-line "ssh-ed25519 <base64>"
// form: no options, comment, certificate or other key type.
func parsePublicKey(s string) (ssh.PublicKey, bool) {
	encoded, ok := strings.CutPrefix(s, ssh.KeyAlgoED25519+" ")
	if !ok {
		return nil, false
	}
	wire, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(wire) != encoded {
		return nil, false
	}
	k, err := ssh.ParsePublicKey(wire)
	if err != nil || k.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(k.Marshal(), wire) {
		return nil, false
	}
	return k, true
}

// keyPath is a syntax pre-check only; agentkeys validates the path again and
// refuses before opening anything. Nothing is discovered.
func keyPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > maxPathBytes || !utf8.ValidString(p) || path.Clean(p) != p {
		return false
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// localDir matches maclaunch's cwd/HOME rule: a clean absolute path, not root.
func localDir(p string) bool {
	return p != "/" && path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}

func validFingerprint(s string) bool {
	encoded, ok := strings.CutPrefix(s, "SHA256:")
	b, err := base64.RawStdEncoding.DecodeString(encoded)
	return ok && err == nil && len(b) == 32 && base64.RawStdEncoding.EncodeToString(b) == encoded
}

func validLabel(s string) bool {
	if len(s) == 0 || len(s) > maxLabelBytes {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func printable(s string) bool {
	for _, c := range []byte(s) {
		if c <= 0x20 || c >= 0x7f {
			return false
		}
	}
	return s != ""
}

func text(raw json.RawMessage, limit int) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil || s == "" || len(s) > limit {
		return "", false
	}
	return s, true
}

// integer accepts only a canonical non-negative decimal of at most 6 digits.
func integer(raw json.RawMessage) (int, bool) {
	s := string(raw)
	n, err := strconv.Atoi(s)
	if err != nil || len(s) > 6 || n < 0 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// wipe is best effort: it clears this slice's backing array, not copies made
// by the runtime.
func wipe(b []byte) {
	clear(b[:cap(b)])
}
