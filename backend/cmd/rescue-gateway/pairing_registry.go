package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"golang.org/x/crypto/ssh"
)

var errRegistry = errors.New("rescue-gateway: invalid pairing registry")

// registryOps is the private filesystem seam used by native-store tests.
type registryOps struct {
	readTrust func(context.Context, string) ([]byte, error)
	rename    func(string, string) error
	syncDir   func(string) error
	// diagnostic receives fixed operator text; it never carries paths or identities.
	diagnostic io.Writer
}

type storedPairRecord struct {
	HostLabel    string `json:"label"`
	PublicKey    string `json:"public_key"`
	Fingerprint  string `json:"fingerprint"`
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
	RegisteredAt string `json:"registered_at"`
}

type storedPairRegistry struct {
	Hosts []storedPairRecord `json:"hosts"`
}

// validatePairingRegistry is the validation boundary for persisted pairings.
// Stub: the validation rules are covered by A14 and will be implemented by
// the green change.
func validatePairingRegistry(raw []byte, cfg localConfig) ([]agentgateway.PairRecord, error) {
	if len(raw) == 0 {
		return nil, nil // no registry file yet: every slot starts empty
	}
	if agentwire.CheckJSON(raw, false) != nil {
		return nil, errRegistry
	}
	m, e := agentwire.Fields(raw, "hosts")
	if e != nil || len(m) != 1 {
		return nil, errRegistry
	}
	items, ok := array(m["hosts"], agenttransport.MaxHosts)
	if !ok {
		return nil, errRegistry
	}
	defer wipeMembers(m)
	var records []agentgateway.PairRecord
	labels, keys, principals := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, h := range cfg.hosts {
		if h.Paired {
			labels[h.Label] = true
		} else if h.PublicKey != nil {
			keys[string(h.PublicKey.Marshal())] = true
		}
	}
	// Signers are not loaded yet; the enrolled fingerprints identify principal keys.
	for _, k := range cfg.keys {
		principals[k.Fingerprint] = true
	}
	for _, item := range items {
		f, err := agentwire.Fields(item, "label", "public_key", "fingerprint", "issuer", "subject", "registered_at")
		if err != nil || len(f) != 6 {
			return nil, errRegistry
		}
		var x storedPairRecord
		valid := decodeString(f["label"], &x.HostLabel) && decodeString(f["public_key"], &x.PublicKey) && decodeString(f["fingerprint"], &x.Fingerprint) && decodeString(f["issuer"], &x.Issuer) && decodeString(f["subject"], &x.Subject) && decodeString(f["registered_at"], &x.RegisteredAt)
		wipeMembers(f)
		if !valid || !labels[x.HostLabel] {
			return nil, errRegistry
		}
		labels[x.HostLabel] = false // one record per slot
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(x.PublicKey))
		if err != nil || len(rest) != 0 || key.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(ssh.MarshalAuthorizedKey(key), []byte(x.PublicKey+"\n")) {
			return nil, errRegistry
		}
		fp := ssh.FingerprintSHA256(key)
		if fp != x.Fingerprint || keys[string(key.Marshal())] || principals[fp] {
			return nil, errRegistry
		}
		host := hostByLabel(cfg.hosts, x.HostLabel)
		p := principalByLabel(cfg.gateway.Principals, host.Principals[0])
		tm, err := time.Parse(time.RFC3339, x.RegisteredAt)
		if err != nil || tm.Location() != time.UTC || x.Issuer != cfg.gateway.Verifier.Issuer || x.Subject != p.Subject || keys[string(key.Marshal())] {
			return nil, errRegistry
		}
		keys[string(key.Marshal())] = true
		records = append(records, agentgateway.PairRecord{HostLabel: x.HostLabel, HostKey: key.Marshal(), Issuer: x.Issuer, Subject: x.Subject, RegisteredAt: tm})
	}
	return records, nil
}

func decodeString(raw []byte, out *string) bool {
	return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, out) == nil && *out != ""
}
func hostByLabel(h []agenttransport.EnrolledHost, label string) agenttransport.EnrolledHost {
	for _, x := range h {
		if x.Label == label {
			return x
		}
	}
	return agenttransport.EnrolledHost{}
}
func principalByLabel(p []agentgateway.Principal, label string) agentgateway.Principal {
	for _, x := range p {
		if x.Label == label {
			return x
		}
	}
	return agentgateway.Principal{}
}

// encodePairingRegistry writes records in label order, in the form validatePairingRegistry reads.
func encodePairingRegistry(records []agentgateway.PairRecord) ([]byte, error) {
	d := storedPairRegistry{Hosts: []storedPairRecord{}}
	records = slices.Clone(records)
	slices.SortFunc(records, func(a, b agentgateway.PairRecord) int { return strings.Compare(a.HostLabel, b.HostLabel) })
	for _, r := range records {
		k, err := ssh.ParsePublicKey(r.HostKey)
		if err != nil {
			return nil, errRegistry
		}
		d.Hosts = append(d.Hosts, storedPairRecord{r.HostLabel, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), ssh.FingerprintSHA256(k), r.Issuer, r.Subject, r.RegisteredAt.UTC().Format(time.RFC3339)})
	}
	return json.Marshal(d)
}
