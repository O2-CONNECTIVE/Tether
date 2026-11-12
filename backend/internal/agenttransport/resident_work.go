package agenttransport

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func (i ResidentFixedIntent) workBinding() agentgateway.ResidentWorkBinding {
	return agentgateway.ResidentWorkBinding{Host: i.host, Principal: i.principal, Generation: i.generation, RequestID: i.requestID, Sequence: i.sequence, DeadlineMillis: i.deadline.UnixMilli()}
}
func workTranscript(endpoint, host, principal string, d agentgateway.Dispatch, nonce []byte, b agentgateway.ResidentWorkBinding) []byte {
	out := transcript(endpoint, host, principal, d, nonce)
	for _, v := range []string{"resident-fixed-work-v1", b.Generation, strconv.FormatUint(b.Sequence, 10), b.RequestID, strconv.FormatInt(b.DeadlineMillis, 10)} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(v)))
		out = append(out, v...)
	}
	return out
}
func encodeWorkRegister(host, principal string, d agentgateway.Dispatch, sig []byte, b agentgateway.ResidentWorkBinding) []byte {
	var m map[string]string
	_ = json.Unmarshal(encodeRegister(host, principal, d, sig), &m)
	m["type"] = "resident_register"
	m["resident_generation"] = b.Generation
	m["resident_sequence"] = strconv.FormatUint(b.Sequence, 10)
	m["resident_request_id"] = b.RequestID
	m["resident_deadline_ms"] = strconv.FormatInt(b.DeadlineMillis, 10)
	return residentJSON(m)
}
func decodeWorkRegister(msg []byte) (registration, error) {
	keys := append(append([]string{}, registerFields...), "resident_generation", "resident_sequence", "resident_request_id", "resident_deadline_ms")
	m, ok := stringFields(msg, keys...)
	if !ok || m["type"] != "resident_register" || !residentHex(m["resident_generation"], 32) || !residentHex(m["resident_request_id"], 16) {
		return registration{}, ErrRefused
	}
	seq, okSeq := residentDecimal(m["resident_sequence"])
	ms, okMS := residentDecimal(m["resident_deadline_ms"])
	if !okSeq || seq == 0 || seq > 32 || !okMS || len(m["resident_deadline_ms"]) != 13 {
		return registration{}, ErrRefused
	}
	b := agentgateway.ResidentWorkBinding{Host: m["host"], Principal: m["principal"], Generation: m["resident_generation"], RequestID: m["resident_request_id"], Sequence: seq, DeadlineMillis: int64(ms)}
	for _, k := range []string{"resident_generation", "resident_sequence", "resident_request_id", "resident_deadline_ms"} {
		delete(m, k)
	}
	m["type"] = typeRegister
	r, e := decodeRegister(residentJSON(m))
	if e != nil {
		return r, e
	}
	r.resident = &b
	return r, nil
}
func (h *Handler) ResidentGeneration(host, principal string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.residents[residentMapKey(host, principal)]
	if h.closed || p == nil || !p.ready || p.ctx.Err() != nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sequence >= residentMaxIntents {
		return ""
	}
	return p.binding.generation
}
func (h *Handler) BeginResidentFixed(ctx context.Context, host, principal, generation, id string, deadline time.Time) error {
	e := h.proposeResident(ctx, host, principal, generation, id, deadline, residentRunScope{})
	if e == ErrWorkPending {
		return agentgateway.ErrResidentPending
	}
	if e == ErrWorkUnconfigured {
		return agentgateway.ErrResidentWorkUnconfigured
	}
	return e
}
func (h *Handler) workIsCurrent(b agentgateway.ResidentWorkBinding) bool {
	h.mu.Lock()
	p := h.residents[residentMapKey(b.Host, b.Principal)]
	valid := !h.closed && p != nil && p.ready && p.ctx.Err() == nil && p.binding.generation == b.Generation
	h.mu.Unlock()
	if !valid {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	i, ok := p.intents[b.RequestID]
	return ok && i.sequence == b.Sequence && i.deadline.UnixMilli() == b.DeadlineMillis
}
func collectionProof(b residentBinding, i ResidentFixedIntent, cleanup string) agentgateway.ResidentCollectionProof {
	return agentgateway.ResidentCollectionProof{Binding: i.workBinding(), Endpoint: b.endpoint, ClientNonce: b.clientNonce, ServerNonce: b.serverNonce, HostKey: b.hostKey, PrincipalKey: b.principalKey, Cleanup: cleanup}
}

// Collected signs only the original work binding after the consumer observes
// SAME common owner's original cleanup and Run join. It confers no execution
// authority; cleanup can be reported after the finite operation has expired.
func (s *ResidentSourceLease) Collected(i ResidentFixedIntent, originalResult error) error {
	if s == nil || s.self != s || i.Receiver() != s.receiver {
		return ErrRefused
	}
	s.mu.Lock()
	if s.finishing || !s.replies[i.RequestID()] || s.collections[i.RequestID()] {
		s.mu.Unlock()
		return ErrRefused
	}
	s.collections[i.RequestID()] = true
	s.mu.Unlock()
	cleanup := "confirmed"
	if originalResult != nil {
		cleanup = "unknown"
	}
	proof := collectionProof(s.receiver.binding, i, cleanup)
	sig, e := sign(s.receiver.cfg.HostKey, s.receiver.cfg.HostKey.PublicKey(), agentgateway.ResidentCollectionTranscript(proof))
	if e != nil {
		return e
	}
	b := proof.Binding
	msg := residentJSON(map[string]string{"type": "resident_collected", "generation": b.Generation, "sequence": strconv.FormatUint(b.Sequence, 10), "request_id": b.RequestID, "cleanup": cleanup, "signature": b64.EncodeToString(sig)})
	ctx, stop := context.WithTimeout(s.receiver.Context(), 5*time.Second)
	defer stop()
	e = residentWrite(ctx, s.receiver.ws, msg)
	s.ReleaseWork(i)
	return e
}
