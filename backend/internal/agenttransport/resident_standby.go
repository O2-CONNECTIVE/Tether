package agenttransport

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"

// ReportStandby is not implemented yet (#412 red).
func (s *ResidentSourceLease) ReportStandby(r agentgateway.ResidentStandby) error {
	return ErrRefused
}

// ResidentStandby is not implemented yet (#412 red).
func (h *Handler) ResidentStandby(host, principal, generation string) (agentgateway.ResidentStandby, bool) {
	return agentgateway.ResidentStandby{}, false
}
