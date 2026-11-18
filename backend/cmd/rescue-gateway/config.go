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
	gateway      agentgateway.FileConfig
	endpoint     string
	keys         []agentkeys.Config // keys[i] belongs to gateway.Principals[i]
	hosts        []agenttransport.EnrolledHost
	resident     []agentgateway.ResidentEnrollment // explicit fixed-scope opt-in only
	residentOnly bool                              // rejects legacy registration and generic MCP binding
	pairing      *pairingConfig
}

// pairingConfig is the optional host-pairing composition input.
type pairingConfig struct {
	RegistryPath string
	Audience     string
	ClientID     string
	TokenTypes   []string
}

// parseLocalConfig validates the whole document before any key is opened.
// Only resident and resident_only are optional; unknown, duplicate and null members are refused at
// every depth. Omission never promotes legacy registrations to resident access.
// configPath is the document's own path, which no key may share.
func parseLocalConfig(raw []byte, configPath string) (localConfig, error) {
	if len(raw) == 0 || len(raw) > agentkeys.MaxTrustConfigBytes || agentwire.CheckJSON(raw, false) != nil {
		return localConfig{}, errConfig
	}
	m, fieldErr := agentwire.Fields(raw, "gateway", "registration_endpoint", "principal_keys", "hosts", "resident", "resident_only", "pairing")
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
	if raw, present := m["resident_only"]; present {
		if !bytes.Equal(raw, []byte("true")) || len(c.resident) == 0 {
			return localConfig{}, errConfig
		}
		c.residentOnly = true
	}
	if raw, present := m["pairing"]; present {
		f, fieldErr := agentwire.Fields(raw, "registry_path", "audience", "client_id", "token_types")
		if fieldErr != nil || len(f) != 4 {
			return localConfig{}, errConfig
		}
		var p pairingConfig
		var valid bool
		p.RegistryPath, valid = text(f["registry_path"], maxPathBytes)
		if !valid || !absolutePath(p.RegistryPath) {
			return localConfig{}, errConfig
		}
		p.Audience, valid = text(f["audience"], 256)
		if !valid || !printable(p.Audience) {
			return localConfig{}, errConfig
		}
		p.ClientID, valid = text(f["client_id"], 256)
		if !valid || !printable(p.ClientID) {
			return localConfig{}, errConfig
		}
		vals, valid := array(f["token_types"], 16)
		if !valid || len(vals) == 0 {
			return localConfig{}, errConfig
		}
		for _, v := range vals {
			s, yes := text(v, 256)
			if !yes || !printable(s) {
				return localConfig{}, errConfig
			}
			p.TokenTypes = append(p.TokenTypes, s)
		}
		if c.gateway.Mode != agentgateway.ModeOAuthBearer || p.Audience == c.gateway.Verifier.Audience ||
			(p.Audience == p.ClientID && containsNormalType(p.TokenTypes, "jwt")) ||
			pairingPathConflict(c.gateway.Resource, c.endpoint) || samePath(p.RegistryPath, configPath) {
			return localConfig{}, errConfig
		}
		for _, k := range c.keys {
			if samePath(p.RegistryPath, k.Path) {
				return localConfig{}, errConfig
			}
		}
		c.pairing = &p
	}
	paired := false
	for i := range c.hosts {
		if c.hosts[i].Paired {
			paired = true
			if len(c.hosts[i].Principals) != 1 {
				return localConfig{}, errConfig
			}
			found := false
			for _, r := range c.resident {
				if r.HostLabel == c.hosts[i].Label && r.PrincipalLabel == c.hosts[i].Principals[0] {
					found = true
				}
			}
			if !found {
				return localConfig{}, errConfig
			}
		}
	}
	if paired && c.pairing == nil {
		return localConfig{}, errConfig
	}
	return c, nil
}

func containsNormalType(types []string, target string) bool {
	for _, t := range types {
		if strings.ToLower(t) == target {
			return true
		}
	}
	return false
}

// pairingPathConflict reports whether the fixed pair route would shadow the MCP
// resource or the host registration route. Metadata paths start with
// MetadataPrefix and cannot equal the fixed PairPath.
func pairingPathConflict(resource, endpoint string) bool {
	r, err := url.Parse(resource)
	e, ok := wssEndpoint(endpoint)
	return err != nil || !ok || r.Path == agentgateway.PairPath || e.Path == agentgateway.PairPath
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
		entries = append(entries, agentgateway.ResidentEnrollment{HostLabel: host, PrincipalLabel: principal})
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
		f, fieldErr := agentwire.Fields(item, "label", "public_key", "principals", "paired")
		if fieldErr != nil || len(f) < 3 {
			return nil, false
		}
		label, ok1 := text(f["label"], maxLabelBytes)
		encoded, hasPublicKey := text(f["public_key"], 128)
		served, ok3 := array(f["principals"], agentgateway.MaxPrincipals)
		paired := bytes.Equal(f["paired"], []byte("true"))
		key, ok4 := parsePublicKey(encoded)
		if paired && f["public_key"] == nil {
			ok4 = true
		}
		_, isPrincipal := labels[label]
		if !ok1 || (!paired && !hasPublicKey) || (paired && hasPublicKey) || !ok3 || !ok4 || len(served) == 0 || (paired && len(served) != 1) || !validLabel(label) || isPrincipal || hostLabels[label] ||
			(encoded != "" && hostKeys[encoded]) || (key != nil && principalPrints[ssh.FingerprintSHA256(key)]) {
			wipeMembers(f)
			return nil, false
		}
		h := agenttransport.EnrolledHost{Label: label, PublicKey: key, Paired: paired}
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
		hostLabels[label] = true
		if encoded != "" {
			hostKeys[encoded] = true
		}
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
