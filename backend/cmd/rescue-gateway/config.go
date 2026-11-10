package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"golang.org/x/crypto/ssh"
)

var errConfig = errors.New("rescue-gateway: invalid local composition document")

const (
	maxPathBytes     = 1024
	maxEndpointBytes = 256
	maxLabelBytes    = 32
)

// localConfig is the gateway host's composition document. It envelopes the
// existing public gateway document and adds only references: each principal's
// enrolled key path plus an independently recorded fingerprint, the exact wss
// registration endpoint and the enrolled host keys. It holds no private byte.
type localConfig struct {
	gateway  agentgateway.FileConfig
	endpoint string
	keys     []agentkeys.Config // keys[i] belongs to gateway.Principals[i]
	hosts    []agenttransport.EnrolledHost
	resident []agentgateway.ResidentEnrollment // explicit fixed-scope opt-in only
}

// parseLocalConfig validates the whole document before any key is opened.
// Only resident is optional; unknown, duplicate and null members are refused at
// every depth. Omission never promotes legacy registrations to resident access.
// configPath is the document's own path, which no key may share.
func parseLocalConfig(raw []byte, configPath string) (localConfig, error) {
	if len(raw) == 0 || len(raw) > agentkeys.MaxTrustConfigBytes || agentwire.CheckJSON(raw, false) != nil {
		return localConfig{}, errConfig
	}
	m, fieldErr := agentwire.Fields(raw, "gateway", "registration_endpoint", "principal_keys", "hosts", "resident")
	defer wipeMembers(m)
	if fieldErr != nil || m["gateway"] == nil || m["registration_endpoint"] == nil || m["principal_keys"] == nil || m["hosts"] == nil {
		return localConfig{}, errConfig
	}
	var c localConfig
	var err error
	var ok bool
	if c.gateway, err = agentgateway.ParseConfig(m["gateway"]); err != nil {
		return localConfig{}, errConfig
	}
	if c.endpoint, ok = text(m["registration_endpoint"], maxEndpointBytes); !ok || !distinctEndpoints(c.gateway.Resource, c.endpoint) {
		return localConfig{}, errConfig
	}
	labels := map[string]int{}
	for i, p := range c.gateway.Principals {
		labels[p.Label] = i
	}
	if c.keys, ok = principalKeys(m["principal_keys"], labels, configPath); !ok {
		return localConfig{}, errConfig
	}
	if c.hosts, ok = enrolledHosts(m["hosts"], labels, c.keys); !ok {
		return localConfig{}, errConfig
	}
	if raw, present := m["resident"]; present {
		if c.resident, ok = residentEnrollments(raw, c.hosts); !ok {
			return localConfig{}, errConfig
		}
	}
	return c, nil
}

// residentEnrollments selects an existing, unambiguous principal/host pair.
// The fixed operation is explicit: no command, timeout, grant or policy input
// is accepted here. All validation finishes before any native key read.
func residentEnrollments(raw json.RawMessage, hosts []agenttransport.EnrolledHost) ([]agentgateway.ResidentEnrollment, bool) {
	items, ok := array(raw, agentgateway.MaxPrincipals)
	if !ok || len(items) == 0 {
		return nil, false
	}
	entries := make([]agentgateway.ResidentEnrollment, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		m, ok := members(item, "host_label", "principal_label", "operation")
		if !ok {
			return nil, false
		}
		host, hostOK := text(m["host_label"], maxLabelBytes)
		principal, principalOK := text(m["principal_label"], maxLabelBytes)
		operation, operationOK := text(m["operation"], 64)
		wipeMembers(m)
		if !hostOK || !principalOK || !operationOK || operation != agentgateway.ResidentFixedID || seen[principal] {
			return nil, false
		}
		var selected *agenttransport.EnrolledHost
		count := 0
		for i := range hosts {
			for _, p := range hosts[i].Principals {
				if p == principal {
					count++
					if hosts[i].Label == host {
						selected = &hosts[i]
					}
				}
			}
		}
		if count != 1 || selected == nil {
			return nil, false
		}
		seen[principal] = true
		entries = append(entries, agentgateway.ResidentEnrollment{HostLabel: host, PrincipalLabel: principal, HostKey: selected.PublicKey.Marshal()})
	}
	return entries, true
}

// principalKeys requires exactly one key reference per configured principal.
// Paths and fingerprints are distinct per member and from the document.
func principalKeys(raw json.RawMessage, labels map[string]int, configPath string) ([]agentkeys.Config, bool) {
	items, ok := array(raw, len(labels))
	if !ok || len(items) != len(labels) {
		return nil, false
	}
	keys := make([]agentkeys.Config, len(labels))
	seen := make([]bool, len(labels))
	var paths []string
	fingerprints := map[string]bool{}
	for _, item := range items {
		f, ok := members(item, "label", "path", "fingerprint")
		if !ok {
			return nil, false
		}
		label, ok1 := text(f["label"], maxLabelBytes)
		path, ok2 := text(f["path"], maxPathBytes)
		fingerprint, ok3 := text(f["fingerprint"], 64)
		wipeMembers(f)
		i, known := labels[label]
		if !ok1 || !ok2 || !ok3 || !known || seen[i] || !absolutePath(path) || !validFingerprint(fingerprint) ||
			fingerprints[fingerprint] || samePath(path, configPath) || samePath(path, paths...) {
			return nil, false
		}
		seen[i], fingerprints[fingerprint] = true, true
		paths = append(paths, path)
		keys[i] = agentkeys.Config{Path: path, Fingerprint: fingerprint}
	}
	return keys, true
}

// enrolledHosts reads at most four operator-enrolled Mac host keys. Host
// labels never equal a principal label, every mapped principal exists, and a
// host key is never one of the principal keys.
func enrolledHosts(raw json.RawMessage, labels map[string]int, keys []agentkeys.Config) ([]agenttransport.EnrolledHost, bool) {
	items, ok := array(raw, agenttransport.MaxHosts)
	if !ok || len(items) == 0 {
		return nil, false
	}
	principalPrints := map[string]bool{}
	for _, k := range keys {
		principalPrints[k.Fingerprint] = true
	}
	hosts := make([]agenttransport.EnrolledHost, 0, len(items))
	hostLabels, hostKeys := map[string]bool{}, map[string]bool{}
	for _, item := range items {
		f, ok := members(item, "label", "public_key", "principals")
		if !ok {
			return nil, false
		}
		label, ok1 := text(f["label"], maxLabelBytes)
		encoded, ok2 := text(f["public_key"], 128)
		served, ok3 := array(f["principals"], agentgateway.MaxPrincipals)
		key, ok4 := parsePublicKey(encoded)
		_, isPrincipal := labels[label]
		if !ok1 || !ok2 || !ok3 || !ok4 || len(served) == 0 || !validLabel(label) || isPrincipal || hostLabels[label] ||
			hostKeys[encoded] || principalPrints[ssh.FingerprintSHA256(key)] {
			wipeMembers(f)
			return nil, false
		}
		h := agenttransport.EnrolledHost{Label: label, PublicKey: key}
		mapped := map[string]bool{}
		for _, s := range served {
			p, ok := text(s, maxLabelBytes)
			if _, known := labels[p]; !ok || !known || mapped[p] {
				wipeMembers(f)
				return nil, false
			}
			mapped[p] = true
			h.Principals = append(h.Principals, p)
		}
		wipeMembers(f)
		hostLabels[label], hostKeys[encoded] = true, true
		hosts = append(hosts, h)
	}
	return hosts, true
}

// distinctEndpoints requires the canonical wss registration endpoint on the
// same public origin as the https MCP resource, at an exact path that is not
// the resource, its protected resource metadata or any well-known location.
func distinctEndpoints(resource, endpoint string) bool {
	r, err := url.Parse(resource)
	e, ok := wssEndpoint(endpoint)
	if err != nil || !ok || r.Scheme != "https" || r.RawPath != "" || r.Host != e.Host {
		return false
	}
	switch e.Path {
	case r.Path, agentgateway.MetadataPrefix, agentgateway.MetadataPrefix + r.Path:
		return false
	}
	return !strings.HasPrefix(e.Path, "/.well-known/")
}

// wssEndpoint applies agenttransport's endpoint rules before any key is read;
// NewHandler and NewClient check them again.
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

// absolutePath is a syntax pre-check only; agentkeys validates the platform
// path again and refuses before opening anything. Nothing is discovered.
func absolutePath(p string) bool {
	if p == "" || len(p) > maxPathBytes || !utf8.ValidString(p) {
		return false
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return strings.HasPrefix(p, "/") || len(p) > 3 && p[0] >= 'A' && p[0] <= 'Z' && p[1:3] == `:\`
}

// samePath compares case-insensitively: both target volumes may fold case,
// so a case variant is treated as the same file.
func samePath(p string, others ...string) bool {
	for _, o := range others {
		if strings.EqualFold(p, o) {
			return true
		}
	}
	return false
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

// members reads a strict object with exactly the named members present.
func members(raw json.RawMessage, want ...string) (map[string]json.RawMessage, bool) {
	m, err := agentwire.Fields(raw, want...)
	if err != nil || len(m) != len(want) {
		if m != nil {
			wipeMembers(m)
		}
		return nil, false
	}
	return m, true
}

func array(raw json.RawMessage, limit int) ([]json.RawMessage, bool) {
	var items []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil || len(items) > limit {
		return nil, false
	}
	return items, true
}

func text(raw json.RawMessage, limit int) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil || s == "" || len(s) > limit {
		return "", false
	}
	return s, true
}

// wipeMembers clears the decoded copies of the document; best effort, like
// wipe, it does not reach copies made by the runtime.
func wipeMembers(m map[string]json.RawMessage) {
	for _, v := range m {
		wipe(v)
	}
}

func wipe(b []byte) {
	clear(b[:cap(b)])
}
