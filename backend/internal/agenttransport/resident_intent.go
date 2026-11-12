package agenttransport

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"strconv"
	"time"
)

var ErrWorkPending = errors.New("agenttransport: request admitted; native work/result not confirmed")

var ErrWorkUnconfigured = errors.New("agenttransport: resident work factory is not configured")

// ResidentFixedIntent is validated wire correlation, never an execution grant.
// Its zero value is invalid. No command, profile or approval flag is accepted.
// The same type also correlates command-run work (see resident_run.go): then
// run is set, and Operation and TimeoutMillis report the run values, which the
// fixed-operation admission refuses.
type ResidentFixedIntent struct {
	generation, requestID                 string
	endpoint, host, principal             string
	sequence                              uint64
	deadline                              time.Time
	original                              context.Context
	receiver                              *ResidentReceiver
	hostFingerprint, principalFingerprint string
	run                                   residentRunScope
}

func (i ResidentFixedIntent) OriginalContext() context.Context { return i.original }
func (i ResidentFixedIntent) Receiver() *ResidentReceiver      { return i.receiver }
func (i ResidentFixedIntent) HostKeyFingerprint() string       { return i.hostFingerprint }
func (i ResidentFixedIntent) PrincipalKeyFingerprint() string  { return i.principalFingerprint }
func (i ResidentFixedIntent) HostLabel() string                { return i.host }
func (i ResidentFixedIntent) PrincipalLabel() string           { return i.principal }
func (i ResidentFixedIntent) Endpoint() string                 { return i.endpoint }
func (i ResidentFixedIntent) TimeoutMillis() uint32 {
	if i.isRun() {
		return i.run.timeoutMillis
	}
	return 15000
}
func (i ResidentFixedIntent) RequestID() string   { return i.requestID }
func (i ResidentFixedIntent) Generation() string  { return i.generation }
func (i ResidentFixedIntent) Deadline() time.Time { return i.deadline }
func (i ResidentFixedIntent) Operation() string {
	if i.isRun() {
		return ResidentRunOperation
	}
	return ResidentFixedOperation
}
func (i ResidentFixedIntent) values() []string {
	return []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, ResidentFixedOperation, strconv.FormatInt(i.deadline.UnixMilli(), 10), "15000"}
}
func residentEncodeIntent(b residentBinding, i ResidentFixedIntent, s ssh.Signer) ([]byte, error) {
	if i.isRun() {
		return nil, ErrRefused // never sign a command-run intent as the fixed operation
	}
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
	i := ResidentFixedIntent{endpoint: b.endpoint, host: b.host, principal: b.principal, generation: m["generation"], sequence: seq, requestID: m["request_id"], deadline: time.UnixMilli(int64(ms))}
	sig, okSig := residentSignature(m)
	if !ok || !okTime || !okSig || len(m["deadline_unix_ms"]) != 13 || i.generation != b.generation || seq != next || seq > residentMaxIntents || !residentHex(i.requestID, 16) || seen[i.requestID] || m["operation"] != ResidentFixedOperation || m["timeout_ms"] != "15000" || !now.Before(i.deadline) || i.deadline.After(now.Add(ResidentFixedTimeout)) || verify(key, residentTranscript(b, "gateway-fixed", i.values()...), sig) != nil {
		return ResidentFixedIntent{}, ErrRefused
	}
	return i, nil
}
func residentEncodeRefusal(b residentBinding, i ResidentFixedIntent, s ssh.Signer) ([]byte, error) {
	values := []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, "work_unconfigured"}
	sig, e := sign(s, s.PublicKey(), residentTranscript(b, "host-refused", values...))
	if e != nil {
		return nil, e
	}
	return residentJSON(map[string]string{"type": "resident_refused", "generation": values[0], "sequence": values[1], "request_id": values[2], "code": values[3], "signature": b64.EncodeToString(sig)}), nil
}
func residentVerifyRefusal(b residentBinding, i ResidentFixedIntent, msg []byte, key ssh.PublicKey) error {
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

func residentEncodePending(b residentBinding, i ResidentFixedIntent, s ssh.Signer) ([]byte, error) {
	values := []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, "admission_pending"}
	sig, e := sign(s, s.PublicKey(), residentTranscript(b, "host-pending", values...))
	if e != nil {
		return nil, e
	}
	return residentJSON(map[string]string{"type": "resident_pending", "generation": values[0], "sequence": values[1], "request_id": values[2], "code": values[3], "signature": b64.EncodeToString(sig)}), nil
}
func residentVerifyPending(b residentBinding, i ResidentFixedIntent, msg []byte, key ssh.PublicKey) error {
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
