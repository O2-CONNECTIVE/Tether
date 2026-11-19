package agenttransport

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

func (s *ResidentSourceLease) ReportStandby(r agentgateway.ResidentStandby) error {
	if s == nil || s.self != s || s.receiver == nil || s.receiver.cfg.HostKey == nil || !validResidentStandby(r) {
		return ErrRefused
	}
	s.mu.Lock()
	if s.finishing || s.finished {
		s.mu.Unlock()
		return ErrRefused
	}
	s.mu.Unlock()
	b := s.receiver.binding
	values := residentStandbyValues(b.generation, r)
	sig, err := sign(s.receiver.cfg.HostKey, s.receiver.cfg.HostKey.PublicKey(), residentTranscript(b, "host-standby", values...))
	if err != nil {
		return err
	}
	msg := residentJSON(map[string]string{"type": "resident_standby", "generation": values[0], "revision": values[1], "standby": values[2], "until_unix_ms": values[3], "signature": b64.EncodeToString(sig)})
	// Bounded like every other resident write: a report must not hold the
	// write lock past a collection proof's wait, or the proof fails while the
	// control lives on and is not resent (#412 review).
	ctx, cancel := context.WithTimeout(s.receiver.Context(), 5*time.Second)
	defer cancel()
	return residentWrite(ctx, s.receiver.ws, msg)
}

func validResidentStandby(r agentgateway.ResidentStandby) bool {
	if r.Revision == 0 || (r.Standby != "on" && r.Standby != "stopping") || (r.Standby == "stopping" && !r.Until.IsZero()) {
		return false
	}
	if !r.Until.IsZero() && (r.Until.UnixMilli() < 1_000_000_000_000 || r.Until.UnixMilli() >= 10_000_000_000_000) {
		return false
	}
	return true
}

func residentStandbyValues(generation string, r agentgateway.ResidentStandby) []string {
	until := "0"
	if !r.Until.IsZero() {
		until = strconv.FormatInt(r.Until.UnixMilli(), 10)
	}
	return []string{generation, strconv.FormatUint(r.Revision, 10), r.Standby, until}
}

func decodeResidentStandby(b residentBinding, msg []byte, key ssh.PublicKey) (agentgateway.ResidentStandby, bool) {
	m, ok := residentFields(msg, "resident_standby", "generation", "revision", "standby", "until_unix_ms", "signature")
	if !ok || m["generation"] != b.generation {
		return agentgateway.ResidentStandby{}, false
	}
	revision, okRevision := residentDecimal(m["revision"])
	if !okRevision {
		return agentgateway.ResidentStandby{}, false
	}
	until := time.Time{}
	if m["until_unix_ms"] != "0" {
		ms, ok := residentDecimal(m["until_unix_ms"])
		if !ok || len(m["until_unix_ms"]) != 13 {
			return agentgateway.ResidentStandby{}, false
		}
		until = time.UnixMilli(int64(ms))
	}
	r := agentgateway.ResidentStandby{Revision: revision, Standby: m["standby"], Until: until}
	sig, ok := residentSignature(m)
	if !validResidentStandby(r) || !ok || verify(key, residentTranscript(b, "host-standby", residentStandbyValues(b.generation, r)...), sig) != nil {
		return agentgateway.ResidentStandby{}, false
	}
	return r, true
}

func residentMessageType(msg []byte) string {
	var fields map[string]json.RawMessage
	if len(msg) > MaxRegistrationMessage || json.Unmarshal(msg, &fields) != nil {
		return ""
	}
	var kind string
	_ = json.Unmarshal(fields["type"], &kind)
	return kind
}

func (h *Handler) ResidentStandby(host, principal, generation string) (agentgateway.ResidentStandby, bool) {
	if h == nil {
		return agentgateway.ResidentStandby{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.residents[residentMapKey(host, principal)]
	valid := !h.closed && p != nil && p.ready && p.ctx.Err() == nil && p.binding.generation == generation
	if !valid {
		return agentgateway.ResidentStandby{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.standby == nil {
		return agentgateway.ResidentStandby{}, true
	}
	return *p.standby, true
}
