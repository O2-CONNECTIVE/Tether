package agenttransport

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

var ErrWorkPending = errors.New("agenttransport: request admitted; native work/result not confirmed")

var ErrWorkUnconfigured = errors.New("agenttransport: resident work factory is not configured")

// ResidentCorrelation is validated wire correlation of one resident intent.
// It names no operation or scope and is never an execution grant. Its zero
// value is invalid; only this package mints one.
type ResidentCorrelation struct {
	generation, requestID                 string
	endpoint, host, principal             string
	sequence                              uint64
	deadline                              time.Time
	original                              context.Context
	receiver                              *ResidentReceiver
	hostFingerprint, principalFingerprint string
}

func (i ResidentCorrelation) OriginalContext() context.Context { return i.original }
func (i ResidentCorrelation) Receiver() *ResidentReceiver      { return i.receiver }
func (i ResidentCorrelation) HostKeyFingerprint() string       { return i.hostFingerprint }
func (i ResidentCorrelation) PrincipalKeyFingerprint() string  { return i.principalFingerprint }
func (i ResidentCorrelation) HostLabel() string                { return i.host }
func (i ResidentCorrelation) PrincipalLabel() string           { return i.principal }
func (i ResidentCorrelation) Endpoint() string                 { return i.endpoint }
func (i ResidentCorrelation) RequestID() string                { return i.requestID }
func (i ResidentCorrelation) Generation() string               { return i.generation }
func (i ResidentCorrelation) Deadline() time.Time              { return i.deadline }
func (c ResidentCorrelation) binding(run agentgateway.ResidentRunScope) agentgateway.ResidentWorkBinding {
	return agentgateway.ResidentWorkBinding{Host: c.host, Principal: c.principal, Generation: c.generation, RequestID: c.requestID, Sequence: c.sequence, DeadlineMillis: c.deadline.UnixMilli(), Run: run}
}

// residentCorrelation names the embedded correlation so that only this package
// can build an intent, while the correlation accessors are still promoted.
type residentCorrelation = ResidentCorrelation

// ResidentIntent is one verified resident intent: a ResidentFixedIntent or a
// ResidentRunIntent. The unexported method keeps other packages from declaring
// their own, but a type embedding one of the two still satisfies it, so code
// that signs an intent's binding accepts only those two concrete types.
type ResidentIntent interface {
	Correlation() ResidentCorrelation
	WorkBinding() agentgateway.ResidentWorkBinding
	encode(residentBinding, ssh.Signer) ([]byte, error)
}

// ResidentFixedIntent is the fixed operation's intent.
type ResidentFixedIntent struct{ residentCorrelation }

func (i ResidentFixedIntent) Correlation() ResidentCorrelation { return i.residentCorrelation }
func (i ResidentFixedIntent) WorkBinding() agentgateway.ResidentWorkBinding {
	return i.binding(agentgateway.ResidentRunScope{})
}
func (ResidentFixedIntent) Operation() string     { return ResidentFixedOperation }
func (ResidentFixedIntent) TimeoutMillis() uint32 { return 15000 }
func (i ResidentFixedIntent) encode(b residentBinding, s ssh.Signer) ([]byte, error) {
	return residentEncodeIntent(b, i, s)
}

func (i ResidentFixedIntent) values() []string {
	return []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, ResidentFixedOperation, strconv.FormatInt(i.deadline.UnixMilli(), 10), "15000"}
}
func residentEncodeIntent(b residentBinding, i ResidentFixedIntent, s ssh.Signer) ([]byte, error) {
	sig, e := sign(s, s.PublicKey(), residentTranscript(b, "gateway-fixed", i.values()...))
	if e != nil {
		return nil, e
	}
	v := i.values()
	return residentJSON(map[string]string{"type": "resident_fixed", "generation": v[0], "sequence": v[1], "request_id": v[2], "operation": v[3], "deadline_unix_ms": v[4], "timeout_ms": v[5], "signature": b64.EncodeToString(sig)}), nil
}
func residentDecodeIntent(b residentBinding, msg []byte, key ssh.PublicKey, now time.Time, next uint64, seen map[string]bool) (ResidentFixedIntent, error) {
	m, ok := residentFields(msg, "resident_fixed", "generation", "sequence", "request_id", "operation", "deadline_unix_ms", "timeout_ms", "signature")
	if !ok {
		return ResidentFixedIntent{}, ErrRefused
	}
	seq, ok := residentDecimal(m["sequence"])
	ms, okTime := residentDecimal(m["deadline_unix_ms"])
	i := ResidentFixedIntent{residentCorrelation: residentCorrelation{endpoint: b.endpoint, host: b.host, principal: b.principal, generation: m["generation"], sequence: seq, requestID: m["request_id"], deadline: time.UnixMilli(int64(ms))}}
	sig, okSig := residentSignature(m)
	if !ok || !okTime || !okSig || len(m["deadline_unix_ms"]) != 13 || i.generation != b.generation || seq != next || seq > residentMaxIntents || !residentHex(i.requestID, 16) || seen[i.requestID] || m["operation"] != ResidentFixedOperation || m["timeout_ms"] != "15000" || !now.Before(i.deadline) || i.deadline.After(now.Add(ResidentFixedTimeout)) || verify(key, residentTranscript(b, "gateway-fixed", i.values()...), sig) != nil {
		return ResidentFixedIntent{}, ErrRefused
	}
	return i, nil
}
func residentEncodeRefusal(b residentBinding, i ResidentCorrelation, s ssh.Signer) ([]byte, error) {
	values := []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, "work_unconfigured"}
	sig, e := sign(s, s.PublicKey(), residentTranscript(b, "host-refused", values...))
	if e != nil {
		return nil, e
	}
	return residentJSON(map[string]string{"type": "resident_refused", "generation": values[0], "sequence": values[1], "request_id": values[2], "code": values[3], "signature": b64.EncodeToString(sig)}), nil
}
func residentVerifyRefusal(b residentBinding, i ResidentCorrelation, msg []byte, key ssh.PublicKey) error {
	m, ok := residentFields(msg, "resident_refused", "generation", "sequence", "request_id", "code", "signature")
	if !ok {
		return ErrRefused
	}
	sig, ok := residentSignature(m)
	v := []string{m["generation"], m["sequence"], m["request_id"], m["code"]}
	if !ok || m["generation"] != i.generation || m["sequence"] != strconv.FormatUint(i.sequence, 10) || m["request_id"] != i.requestID || m["code"] != "work_unconfigured" || verify(key, residentTranscript(b, "host-refused", v...), sig) != nil {
		return ErrRefused
	}
	return ErrWorkUnconfigured
}
func residentEncodePending(b residentBinding, i ResidentCorrelation, s ssh.Signer) ([]byte, error) {
	values := []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, "admission_pending"}
	sig, e := sign(s, s.PublicKey(), residentTranscript(b, "host-pending", values...))
	if e != nil {
		return nil, e
	}
	return residentJSON(map[string]string{"type": "resident_pending", "generation": values[0], "sequence": values[1], "request_id": values[2], "code": values[3], "signature": b64.EncodeToString(sig)}), nil
}
func residentVerifyPending(b residentBinding, i ResidentCorrelation, msg []byte, key ssh.PublicKey) error {
	m, ok := residentFields(msg, "resident_pending", "generation", "sequence", "request_id", "code", "signature")
	if !ok {
		return ErrRefused
	}
	sig, ok := residentSignature(m)
	values := []string{m["generation"], m["sequence"], m["request_id"], m["code"]}
	if !ok || m["generation"] != i.generation || m["sequence"] != strconv.FormatUint(i.sequence, 10) || m["request_id"] != i.requestID || m["code"] != "admission_pending" || verify(key, residentTranscript(b, "host-pending", values...), sig) != nil {
		return ErrRefused
	}
	return ErrWorkPending
}
