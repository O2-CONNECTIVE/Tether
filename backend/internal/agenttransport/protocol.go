// Package agenttransport carries one recipient-approved agent channel over an
// outbound WebSocket (TLS) from the temporary Mac to the shared gateway. The
// gateway side is an http.Handler with a fixed, operator-configured set of at
// most four enrolled host keys; the Mac side is a single-use client of one
// maclaunch-approved owner. Before the opaque binary stream starts, the host
// proves its enrolled key over a fresh per-connection challenge; the agent
// channel's SSH host-key proof on the same stream then proves it again.
//
// It issues no grant, token, key or certificate, forwards no OAuth token,
// reads no proxy setting, cookie or self-asserted header, and never retries,
// reconnects or resumes. Endpoints are configuration, never command input.
package agenttransport

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"golang.org/x/crypto/ssh"
)

const (
	// Subprotocol is the only WebSocket subprotocol offered and accepted. The
	// version is part of the name; there is no negotiation.
	Subprotocol = "rescue-agent-register.v1"

	MaxHosts   = 4 // enrolled host keys per gateway
	MaxPending = 4 // concurrent unauthenticated registrations
	// MaxTracked bounds the transports one Handler owns at once: pending
	// registrations, live streams and closes not yet confirmed. Beyond it the
	// Handler refuses new upgrades until owners are confirmed closed.
	MaxTracked = 2 * MaxPending

	// MaxRegistrationMessage bounds one registration message, including every
	// fragment of it. MaxStreamMessage bounds one binary stream message; the
	// sender splits writes so that no message exceeds it.
	MaxRegistrationMessage = 2 << 10
	MaxStreamMessage       = 32 << 10

	NonceSize = 32

	MaxRegistration = 30 * time.Second
	MaxApproval     = 60 * time.Second
	MaxCleanup      = time.Minute

	// minRemaining is the shortest dispatch lifetime either end registers.
	minRemaining = time.Second

	transcriptDomain = "rescue-host.agenttransport.registration\x00v1\x00"
	profileMac       = "mac_current_user"

	typeChallenge = "challenge"
	typeRegister  = "register"
	typeAccepted  = "accepted"
)

var (
	ErrConfig  = errors.New("agenttransport: untrusted or incomplete configuration")
	ErrRefused = errors.New("agenttransport: registration refused")
	ErrUsed    = errors.New("agenttransport: this client already ran")
	ErrClosed  = errors.New("agenttransport: closed")
	// ErrCleanupUnknown is the same sentinel as agentchannel's, so every layer
	// reports an unconfirmed close the same way.
	ErrCleanupUnknown = agentchannel.ErrCleanupUnknown
)

// Budgets bound one registration attempt. Every value is required.
type Budgets struct {
	Registration time.Duration // from the upgrade to the verified proof
	Approval     time.Duration // whole attempt: gateway until Connect returns, Mac until the proof is accepted
	Cleanup      time.Duration // one wait for the transport (and on the Mac the owner) to close
}

// DefaultBudgets are the product budgets for a wide-area tunnel.
func DefaultBudgets() Budgets {
	return Budgets{Registration: 10 * time.Second, Approval: 30 * time.Second, Cleanup: 5 * time.Second}
}

func (b Budgets) valid() bool {
	return b.Registration > 0 && b.Registration <= MaxRegistration &&
		b.Approval > b.Registration && b.Approval <= MaxApproval &&
		b.Cleanup > 0 && b.Cleanup <= MaxCleanup
}

// parseEndpoint accepts only an exact, canonical wss URL with a path. There is
// no ws fallback, userinfo, query, fragment or case variant of the host.
func parseEndpoint(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || !printable(s) || u.Scheme != "wss" || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		u.Path == "" || u.Path == "/" || strings.ToLower(u.Host) != u.Host || u.String() != s {
		return nil, ErrConfig
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return nil, ErrConfig
		}
	}
	return u, nil
}

func printable(s string) bool {
	for _, c := range []byte(s) {
		if c <= 0x20 || c >= 0x7f {
			return false
		}
	}
	return s != ""
}

// validLabel matches the gateway's principal label syntax; host labels use it too.
func validLabel(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// rawEd25519 parses a private copy of SSH wire bytes and accepts only the
// canonical encoding of a raw Ed25519 key (no certificate or other type).
func rawEd25519(wire []byte) (ssh.PublicKey, error) {
	wire = append([]byte(nil), wire...)
	k, err := ssh.ParsePublicKey(wire)
	if err != nil || k.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(k.Marshal(), wire) {
		return nil, ErrConfig
	}
	return k, nil
}

// checkDispatch accepts only an unexpired Mac current-user dispatch with a
// complete binding. It is a shape and freshness check, not a credential.
func checkDispatch(d agentgateway.Dispatch, now time.Time) error {
	b := d.Binding
	if b.Profile != agentexec.ProfileMacCurrentUser || b.Grant.Generation == 0 || !b.Grant.Identity.Valid() ||
		b.Grant.Identity.PolicyDigest != agentexec.MacCurrentUserPolicyDigest() || b.KeySetDigest == ([32]byte{}) {
		return ErrRefused
	}
	if !now.Add(minRemaining).Before(d.Deadline) || d.Deadline.After(now.Add(agentchannel.MaxClientLifetime)) ||
		!time.Unix(0, d.Deadline.UnixNano()).Equal(d.Deadline) {
		return ErrRefused
	}
	if _, err := rawEd25519(d.HostKey); err != nil {
		return ErrRefused
	}
	return nil
}

// transcript is the exact byte string the host signs: domain, the gateway
// resource identity (the configured endpoint), host and principal labels, the
// complete dispatch and this connection's nonce, each length-prefixed.
func transcript(resource, host, principal string, d agentgateway.Dispatch, nonce []byte) []byte {
	var out []byte
	field := func(v []byte) {
		out = binary.BigEndian.AppendUint32(out, uint32(len(v)))
		out = append(out, v...)
	}
	b := d.Binding
	field([]byte(transcriptDomain))
	field([]byte(resource))
	field([]byte(host))
	field([]byte(principal))
	field(binary.BigEndian.AppendUint64(nil, b.Grant.Generation))
	field([]byte(b.Grant.Identity.LaunchID))
	field([]byte(b.Grant.Identity.Epoch))
	field([]byte(b.Grant.Identity.PolicyDigest))
	field(b.KeySetDigest[:])
	field([]byte{byte(b.Profile)})
	field(binary.BigEndian.AppendUint64(nil, uint64(d.Deadline.UnixNano())))
	field(d.HostKey)
	field(nonce)
	return out
}

// sign produces a raw ssh-ed25519 signature and checks it against key, so a
// signer whose key changed cannot produce a proof for another key.
func sign(signer ssh.Signer, key ssh.PublicKey, data []byte) ([]byte, error) {
	var sig *ssh.Signature
	var err error
	if as, ok := signer.(ssh.AlgorithmSigner); ok {
		sig, err = as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoED25519)
	} else {
		sig, err = signer.Sign(rand.Reader, data)
	}
	if err != nil || sig == nil || sig.Format != ssh.KeyAlgoED25519 || key.Verify(data, sig) != nil {
		return nil, ErrConfig
	}
	return ssh.Marshal(sig), nil
}

func verify(key ssh.PublicKey, data, wire []byte) error {
	var sig ssh.Signature
	if ssh.Unmarshal(wire, &sig) != nil || sig.Format != ssh.KeyAlgoED25519 || len(sig.Blob) != 64 || len(sig.Rest) != 0 ||
		key.Verify(data, &sig) != nil {
		return ErrRefused
	}
	return nil
}

var b64 = base64.RawURLEncoding

func decodeB64(s string) ([]byte, bool) {
	v, err := b64.DecodeString(s)
	return v, err == nil && b64.EncodeToString(v) == s
}

// stringFields reads a strict JSON object whose members are exactly want, each a
// JSON string. Duplicates, unknown or missing members and null are refused.
func stringFields(msg []byte, want ...string) (map[string]string, bool) {
	if agentwire.CheckJSON(msg, false) != nil {
		return nil, false
	}
	m, err := agentwire.Fields(msg, want...)
	if err != nil || len(m) != len(want) {
		return nil, false
	}
	out := make(map[string]string, len(want))
	for _, k := range want {
		raw, ok := m[k]
		var s string
		if !ok || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
			return nil, false
		}
		out[k] = s
	}
	return out, true
}

func encodeChallenge(nonce []byte) []byte {
	b, _ := json.Marshal(map[string]string{"type": typeChallenge, "nonce": b64.EncodeToString(nonce)})
	return b
}

func decodeChallenge(msg []byte) ([]byte, error) {
	m, ok := stringFields(msg, "type", "nonce")
	if !ok || m["type"] != typeChallenge {
		return nil, ErrRefused
	}
	nonce, ok := decodeB64(m["nonce"])
	if !ok || len(nonce) != NonceSize {
		return nil, ErrRefused
	}
	return nonce, nil
}

func encodeAccepted() []byte { return []byte(`{"type":"accepted"}`) }

func isAccepted(msg []byte) bool {
	m, ok := stringFields(msg, "type")
	return ok && m["type"] == typeAccepted
}

var registerFields = []string{"type", "host", "principal", "generation", "launch_id", "epoch", "policy_digest",
	"key_set_digest", "profile", "deadline_unix_nano", "host_key", "signature"}

type registration struct {
	host, principal string
	dispatch        agentgateway.Dispatch
	signature       []byte
}

func encodeRegister(host, principal string, d agentgateway.Dispatch, sig []byte) []byte {
	b := d.Binding
	m := map[string]string{
		"type": typeRegister, "host": host, "principal": principal,
		"generation": strconv.FormatUint(b.Grant.Generation, 10), "launch_id": b.Grant.Identity.LaunchID,
		"epoch": b.Grant.Identity.Epoch, "policy_digest": b.Grant.Identity.PolicyDigest,
		"key_set_digest": hex.EncodeToString(b.KeySetDigest[:]), "profile": profileMac,
		"deadline_unix_nano": strconv.FormatInt(d.Deadline.UnixNano(), 10),
		"host_key":           b64.EncodeToString(d.HostKey), "signature": b64.EncodeToString(sig),
	}
	out, _ := json.Marshal(m)
	return out
}

// decodeRegister parses canonical encodings only; it derives no trust. The
// caller checks the labels against configuration and the proof against the
// enrolled key, never against the key the message carries.
func decodeRegister(msg []byte) (registration, error) {
	m, ok := stringFields(msg, registerFields...)
	if !ok || m["type"] != typeRegister || m["profile"] != profileMac || !validLabel(m["host"]) || !validLabel(m["principal"]) {
		return registration{}, ErrRefused
	}
	var r registration
	r.host, r.principal = m["host"], m["principal"]
	gen, err := strconv.ParseUint(m["generation"], 10, 64)
	if err != nil || strconv.FormatUint(gen, 10) != m["generation"] {
		return registration{}, ErrRefused
	}
	deadline, err := strconv.ParseInt(m["deadline_unix_nano"], 10, 64)
	if err != nil || deadline <= 0 || strconv.FormatInt(deadline, 10) != m["deadline_unix_nano"] {
		return registration{}, ErrRefused
	}
	digest, err := hex.DecodeString(m["key_set_digest"])
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != m["key_set_digest"] {
		return registration{}, ErrRefused
	}
	hostKey, ok1 := decodeB64(m["host_key"])
	sig, ok2 := decodeB64(m["signature"])
	if !ok1 || !ok2 {
		return registration{}, ErrRefused
	}
	b := &r.dispatch.Binding
	b.Grant.Generation = gen
	b.Grant.Identity.LaunchID, b.Grant.Identity.Epoch, b.Grant.Identity.PolicyDigest = m["launch_id"], m["epoch"], m["policy_digest"]
	copy(b.KeySetDigest[:], digest)
	b.Profile = agentexec.ProfileMacCurrentUser
	r.dispatch.Deadline = time.Unix(0, deadline)
	r.dispatch.HostKey = hostKey
	r.signature = sig
	return r, nil
}

func newNonce() ([]byte, error) {
	n := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, n); err != nil {
		return nil, err
	}
	return n, nil
}
