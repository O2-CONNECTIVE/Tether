package agentgateway

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentwork"
	"golang.org/x/crypto/ssh"
)

const (
	PairPath             = "/tether-agent/v1/pair"
	PairTokenMaxLifetime = 600 * time.Second
	MaxPairBody          = 4 << 10
)

type HostEntry struct {
	Label      string
	Key        []byte
	Principals []string
	Paired     bool
}
type PairRecord struct {
	HostLabel       string
	HostKey         []byte
	Issuer, Subject string
	RegisteredAt    time.Time
}
type PairingConfig struct {
	Verifier                       *Verifier
	Endpoint, RegistrationEndpoint string
	Records                        []PairRecord
	Save                           func([]PairRecord) error
	Disconnect                     func(string)
}

func PairTranscript(endpoint string, hostKey []byte, tokenHash [32]byte) []byte {
	var out bytes.Buffer
	for _, part := range [][]byte{[]byte("rescue-host.agentgateway.pair\x00v1\x00"), []byte(endpoint), hostKey, tokenHash[:]} {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(part)))
		_, _ = out.Write(part)
	}
	return out.Bytes()
}

func (g *Gateway) ConfigureHosts(hosts []HostEntry) error {
	if g == nil || len(hosts) == 0 || len(hosts) > MaxPrincipals {
		return ErrConfig
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hosts != nil || g.pairing != nil || g.resident != nil || g.closed || len(g.sessions) != 0 {
		return ErrRefused
	}
	configured := make(map[string]*hostSlot, len(hosts))
	keys := map[string]bool{}
	for _, p := range g.byLabel {
		if p.signer != nil {
			keys[string(p.signer.PublicKey().Marshal())] = true
		}
	}
	for _, h := range hosts {
		if !validLabel(h.Label) || configured[h.Label] != nil || len(h.Principals) == 0 || len(h.Principals) > MaxPrincipals || (h.Paired && (h.Key != nil || len(h.Principals) != 1)) || (!h.Paired && len(h.Key) == 0) {
			return ErrConfig
		}
		slot := &hostSlot{principals: map[string]bool{}}
		for _, label := range h.Principals {
			p := g.byLabel[label]
			// A pair slot replaces its host key on its owner's IdP login
			// alone, so only a resident owner may hold one.
			if p == nil || slot.principals[label] || (h.Paired && (p.signer == nil || !p.resident)) {
				return ErrConfig
			}
			slot.principals[label] = true
			if h.Paired {
				slot.paired = p
			}
		}
		if !h.Paired {
			key, err := ssh.ParsePublicKey(h.Key)
			if err != nil || key.Type() != ssh.KeyAlgoED25519 {
				return ErrConfig
			}
			slot.key = append([]byte(nil), key.Marshal()...)
			if keys[string(slot.key)] {
				return ErrConfig
			}
			keys[string(slot.key)] = true
		}
		configured[h.Label] = slot
	}
	g.hosts = configured
	return nil
}

func (g *Gateway) HostKey(label string) []byte {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	slot := g.hosts[label]
	if slot == nil {
		return nil
	}
	return append([]byte(nil), slot.key...)
}

func (g *Gateway) ConfigurePairing(cfg PairingConfig) error {
	if g == nil {
		return ErrConfig
	}
	g.pairMu.Lock()
	defer g.pairMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.verifier == nil || g.hosts == nil || g.pairing != nil || g.closed || len(g.sessions) != 0 || g.resident != nil || !validPairEndpoint(cfg.Endpoint) || !validRegistrationURL(cfg.RegistrationEndpoint) {
		return ErrConfig
	}
	// The pair route takes only its own verifier: an MCP token must never register a host.
	if cfg.Verifier == nil || cfg.Verifier == g.verifier || cfg.Verifier.audience == g.verifier.audience || cfg.Verifier.issuer == "" {
		return ErrConfig
	}
	used := map[string]bool{}
	for _, p := range g.byLabel {
		if p.signer != nil {
			used[string(p.signer.PublicKey().Marshal())] = true
		}
	}
	for _, slot := range g.hosts {
		if len(slot.key) != 0 {
			used[string(slot.key)] = true
		}
	}
	seen := map[string]bool{}
	for i := range cfg.Records {
		r := &cfg.Records[i]
		slot := g.hosts[r.HostLabel]
		key, err := ssh.ParsePublicKey(r.HostKey)
		if slot == nil || slot.paired == nil || slot.paired.signer == nil || r.Issuer != cfg.Verifier.issuer || r.Subject != slot.paired.subject || err != nil || key.Type() != ssh.KeyAlgoED25519 || used[string(key.Marshal())] || seen[r.HostLabel] {
			return ErrConfig
		}
		seen[r.HostLabel] = true
		used[string(key.Marshal())] = true
		r.HostKey = append([]byte(nil), key.Marshal()...)
	}
	cfg.Records = clonePairRecords(cfg.Records)
	for _, r := range cfg.Records {
		slot := g.hosts[r.HostLabel]
		slot.key = append([]byte(nil), r.HostKey...)
		record := r
		slot.record = &record
	}
	g.pairing = &cfg
	return nil
}

func validPairEndpoint(raw string) bool {
	if !httpsURL(raw, true) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Path == PairPath
}

func validRegistrationURL(raw string) bool {
	return residentwork.ValidEndpoint(raw)
}

func clonePairRecords(in []PairRecord) []PairRecord {
	out := make([]PairRecord, len(in))
	for i, r := range in {
		out[i] = r
		out[i].HostKey = append([]byte(nil), r.HostKey...)
	}
	return out
}

func (g *Gateway) ServePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxPairBody+1))
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	if len(body) > MaxPairBody {
		http.Error(w, "", http.StatusRequestEntityTooLarge)
		return
	}
	g.mu.Lock()
	cfg := g.pairing
	g.mu.Unlock()
	if cfg == nil {
		http.Error(w, "", http.StatusNotFound)
		return
	}
	header := r.Header.Values("Authorization")
	token := ""
	if len(header) == 1 {
		parts := strings.Fields(header[0])
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			token = parts[1]
		}
	}
	identity, err := cfg.Verifier.Verify(token)
	if err != nil {
		challenge := "Bearer error=\"invalid_token\""
		if len(header) == 0 {
			challenge = "Bearer"
		}
		w.Header().Set("WWW-Authenticate", challenge)
		http.Error(w, "", http.StatusUnauthorized)
		return
	}
	g.mu.Lock()
	p := g.bySubject[identity.Subject]
	var label string
	var target *hostSlot
	for name, slot := range g.hosts {
		if p != nil && slot.paired == p {
			label, target = name, slot
			break
		}
	}
	if target == nil {
		g.mu.Unlock()
		http.Error(w, "", http.StatusForbidden)
		return
	}
	g.mu.Unlock()
	if agentwire.CheckJSON(body, false) != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	m, err := agentwire.Fields(body, "host_public_key", "proof")
	var keyText, proofText string
	keyOK := json.Unmarshal(m["host_public_key"], &keyText) == nil
	proofOK := json.Unmarshal(m["proof"], &proofText) == nil
	if err != nil || len(m) != 2 || !keyOK || !proofOK || keyText == "" || proofText == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(keyText))
	if err != nil || comment != "" || len(options) != 0 || len(rest) != 0 || key.Type() != ssh.KeyAlgoED25519 || strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(key)), "\n") != keyText {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	sigWire, err := base64.RawURLEncoding.Strict().DecodeString(proofText)
	var signature struct {
		Format string
		Blob   []byte
		Rest   []byte `ssh:"rest"`
	}
	parseErr := ssh.Unmarshal(sigWire, &signature)
	tokenHash := sha256.Sum256([]byte(token))
	sig := &ssh.Signature{Format: signature.Format, Blob: signature.Blob}
	if err != nil || parseErr != nil || len(signature.Rest) != 0 || signature.Format != ssh.KeyAlgoED25519 || len(signature.Blob) != ed25519.SignatureSize || key.Verify(PairTranscript(cfg.Endpoint, key.Marshal(), tokenHash), sig) != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	g.pairMu.Lock()
	g.mu.Lock()
	if g.hostKeyInUseLocked(key.Marshal(), target) {
		g.mu.Unlock()
		g.pairMu.Unlock()
		http.Error(w, "", http.StatusConflict)
		return
	}
	entry := g.residentEntryForPrincipalLocked(p)
	if target.replacing || p.route != nil || p.connecting || (entry != nil && entry.current != nil) {
		g.mu.Unlock()
		g.pairMu.Unlock()
		http.Error(w, "", http.StatusConflict)
		return
	}
	target.replacing = true
	record := PairRecord{HostLabel: label, HostKey: key.Marshal(), Issuer: identity.Issuer, Subject: identity.Subject, RegisteredAt: g.clock().UTC()}
	records := clonePairRecords(cfg.Records)
	found := false
	for i := range records {
		if records[i].HostLabel == label {
			records[i] = record
			found = true
			break
		}
	}
	if !found {
		records = append(records, record)
	}
	g.mu.Unlock()
	if cfg.Save != nil {
		if err := cfg.Save(clonePairRecords(records)); err != nil {
			g.mu.Lock()
			target.replacing = false
			g.mu.Unlock()
			g.pairMu.Unlock()
			http.Error(w, "", http.StatusServiceUnavailable)
			return
		}
	}
	g.mu.Lock()
	target.key = append([]byte(nil), key.Marshal()...)
	target.record = &record
	cfg.Records = clonePairRecords(records)
	g.mu.Unlock()
	g.pairMu.Unlock()
	// The slot stays reserved until the old host's links are cut, so the old
	// session cannot start work that would reach the replaced host.
	if cfg.Disconnect != nil {
		cfg.Disconnect(label)
	}
	g.mu.Lock()
	target.replacing = false
	g.mu.Unlock()
	principalKey := ssh.MarshalAuthorizedKey(p.signer.PublicKey())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"registration_endpoint": cfg.RegistrationEndpoint, "host_label": label, "principal_label": p.label, "principal_public_key": strings.TrimSuffix(string(principalKey), "\n")})
}

func (g *Gateway) hostKeyInUseLocked(key []byte, except *hostSlot) bool {
	for _, slot := range g.hosts {
		if slot != except && bytes.Equal(slot.key, key) {
			return true
		}
	}
	for _, p := range g.byLabel {
		if p.signer != nil && bytes.Equal(p.signer.PublicKey().Marshal(), key) {
			return true
		}
	}
	return false
}

func (g *Gateway) residentEntryForPrincipalLocked(p *principal) *residentEntry {
	if g.resident == nil {
		return nil
	}
	return g.resident.entries[p]
}
