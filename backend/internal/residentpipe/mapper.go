package residentpipe

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"reflect"
	"unicode"
	"unicode/utf8"
)

type Transition struct {
	RequestID uint64 `json:"requestId"`
	Intent    string `json:"intent"`
	Outcome   string `json:"outcome"`
}
type Snapshot struct {
	ProtocolVersion uint64      `json:"protocolVersion"`
	Source          string      `json:"source"`
	State           string      `json:"state"`
	Revision        uint64      `json:"revision"`
	ValidForMS      uint64      `json:"validForMs"`
	Standby         string      `json:"standby"`
	Registration    string      `json:"registration"`
	Caller          *string     `json:"caller"`
	Work            string      `json:"work"`
	Cleanup         string      `json:"cleanup"`
	CanRequestOn    bool        `json:"canRequestOn"`
	CanRequestOff   bool        `json:"canRequestOff"`
	Transition      *Transition `json:"transition"`
}
type statusEnvelope struct {
	ProtocolVersion    uint64   `json:"protocolVersion"`
	Type               string   `json:"type"`
	SourceContinuousMS uint64   `json:"sourceContinuousMs"`
	Snapshot           Snapshot `json:"snapshot"`
}
type receiptEnvelope struct {
	ProtocolVersion uint64  `json:"protocolVersion"`
	Type            string  `json:"type"`
	Receipt         Receipt `json:"receipt"`
}
type mapper struct {
	revision, observedAt, highID uint64
	raw                          residentmac.StandbyObservation
	view                         Snapshot
	pending                      *Intent
	refused                      *Transition
	poisoned                     bool
	cleanupUnknown               bool
}

func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func validRaw(o residentmac.StandbyObservation) bool {
	if o.Revision == 0 || o.Revision > MaxInteger || o.Owned < 0 || !oneOf(o.Standby, "off", "on", "stopping", "unknown") || !oneOf(o.Registration, "disconnected", "connecting", "authenticated", "unknown") || !oneOf(o.Work, "idle", "preparing", "running", "stopping", "unknown") || !oneOf(o.Cleanup, "confirmed", "pending", "unknown") {
		return false
	}
	if o.Caller != nil {
		if !utf8.ValidString(*o.Caller) || utf8.RuneCountInString(*o.Caller) == 0 || utf8.RuneCountInString(*o.Caller) > 80 {
			return false
		}
		for _, r := range *o.Caller {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	if t := o.Transition; t != nil {
		if t.RequestID == 0 || t.RequestID > MaxInteger || !oneOf(t.Intent, "on", "off") || !oneOf(t.Outcome, "pending", "applied", "rejected", "unknown") {
			return false
		}
	}
	return true
}
func (m *mapper) observe(o residentmac.StandbyObservation, now uint64) (statusEnvelope, error) {
	if !validRaw(o) || now > MaxInteger || m.revision == MaxInteger || (m.revision > 0 && (now < m.observedAt || o.Revision < m.raw.Revision || (o.Revision == m.raw.Revision && !reflect.DeepEqual(o, m.raw)))) {
		return statusEnvelope{}, ErrProtocol
	}
	m.revision++
	if t := o.Transition; t != nil {
		if t.RequestID > m.highID {
			m.highID = t.RequestID
		}
		if m.pending == nil && (t.Outcome == "pending" || t.Outcome == "unknown") {
			m.pending = &Intent{RequestID: t.RequestID, Intent: t.Intent}
		}
	}
	if o.Cleanup == "unknown" {
		m.cleanupUnknown = true
	}
	s := Snapshot{ProtocolVersion: 1, Source: "resident-owner", State: "source-observation", Revision: m.revision, ValidForMS: 3000, Standby: o.Standby, Registration: o.Registration, Caller: o.Caller, Work: o.Work, Cleanup: o.Cleanup}
	off := o.AdmissionSealed && o.Owned == 0 && o.Cleanup == "confirmed" && o.Work == "idle" && o.Standby == "off"
	historicalAuth := oneOf(o.Registration, "connecting", "disconnected") && o.TransportGeneration != "" && o.Transition != nil && o.Transition.Intent == "on" && o.Transition.Outcome == "applied"
	on := !o.AdmissionSealed && (o.Registration == "authenticated" || historicalAuth) && o.Standby == "on" && o.Cleanup != "unknown"
	if (o.Standby == "off" && !off) || (o.Standby == "on" && !on) {
		s.Standby = "unknown"
	}
	if t := o.Transition; t != nil {
		s.Transition = &Transition{t.RequestID, t.Intent, t.Outcome}
		if t.Outcome == "applied" && ((t.Intent == "on" && !on) || (t.Intent == "off" && !off)) {
			if m.pending != nil && m.pending.RequestID == t.RequestID && m.pending.Intent == t.Intent {
				s.Transition.Outcome = "pending"
			} else {
				s.Transition = nil
			}
		}
	}
	if m.cleanupUnknown {
		s.Cleanup = "unknown"
		s.Standby = "unknown"
		s.Work = "unknown"
		if s.Transition != nil {
			s.Transition.Outcome = "unknown"
		}
	}
	if m.cleanupUnknown || (s.Transition != nil && s.Transition.Outcome == "unknown") {
		m.poisoned = true
	}
	if m.pending != nil && s.Transition != nil {
		t := s.Transition
		p := m.pending
		if t.RequestID != p.RequestID || t.Intent != p.Intent {
			m.poisoned = true
		} else if t.Outcome == "applied" || t.Outcome == "rejected" {
			m.pending = nil
		}
	}
	if m.poisoned {
		s.Standby = "unknown"
		s.Work = "unknown"
		if s.Transition != nil {
			s.Transition.Outcome = "unknown"
		}
	}
	// Only a new command refused here before owner dispatch earns correlated
	// rejection evidence. Refresh the owner first: never hide pending, Unknown,
	// late cleanup failure, or a newer owner identity with a local refusal.
	if t := m.refused; t != nil {
		if !m.poisoned && m.pending == nil && (o.Transition == nil || o.Transition.RequestID < t.RequestID) {
			s.Transition = t
		}
		m.refused = nil // one ordered publication, not retained owner evidence
	}
	s.CanRequestOn = off && o.CanRequestOn && !m.poisoned && m.pending == nil
	s.CanRequestOff = on && o.CanRequestOff && !m.poisoned && m.pending == nil
	m.raw = o
	m.view = s
	m.observedAt = now
	return statusEnvelope{1, "status", now, s}, nil
}
func (m *mapper) submit(owner *residentmac.Standby, r Intent, now uint64) Receipt {
	out := Receipt{1, r.RequestID, r.Intent, "rejected"}
	allowance := r.Intent == "allow-run" || r.Intent == "deny-run"
	if r.RequestID <= m.highID {
		out.Disposition = "stale"
		return out
	}
	m.highID = r.RequestID
	if m.poisoned {
		out.Disposition = "unknown"
		return out
	}
	if m.pending != nil {
		return out
	}
	if m.revision == 0 || now < m.observedAt || now-m.observedAt >= m.view.ValidForMS || r.ExpectedRevision != m.revision || !reflect.DeepEqual(owner.Snapshot(), m.raw) {
		out.Disposition = "stale"
		if !allowance {
			m.refused = &Transition{r.RequestID, r.Intent, "rejected"}
		}
		return out
	}
	if (r.Intent == "on" && !m.view.CanRequestOn) || (r.Intent == "off" && !m.view.CanRequestOff) {
		return out
	}
	var receipt residentmac.StandbyReceipt
	switch r.Intent {
	case "on":
		receipt = owner.RequestOn(r.RequestID, m.raw.Revision)
	case "allow-run":
		receipt = owner.AllowRun(r.RequestID, m.raw.Revision, residentmac.MaxRunAllowance)
	case "deny-run":
		receipt = owner.DenyRun(r.RequestID, m.raw.Revision)
	default:
		receipt = owner.RequestOff(r.RequestID, m.raw.Revision)
	}
	if receipt.RequestID != r.RequestID || receipt.Intent != r.Intent || !oneOf(receipt.Disposition, "accepted", "rejected", "stale", "unknown") {
		m.poisoned = true
		out.Disposition = "unknown"
		return out
	}
	out.Disposition = receipt.Disposition
	// The allowance is not a standby transition: nothing to wait for.
	if !allowance && (receipt.Disposition == "accepted" || receipt.Disposition == "unknown") {
		copy := r
		m.pending = &copy
	}
	if receipt.Disposition == "unknown" {
		m.poisoned = true
	}
	return out
}
