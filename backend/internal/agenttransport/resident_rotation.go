package agenttransport

// RotateResident is a trusted gateway stop-only port. Call only after genuine
// gateway work/host-owner collection is confirmed for the whole generation.
// It cannot approve work, change enrollment or resume/extend an existing grant.
func (h *Handler) RotateResident(host, principal, generation, requestID string) {
	h.mu.Lock()
	p := h.residents[residentMapKey(host, principal)]
	if h.closed || p == nil || !p.ready || p.ctx.Err() != nil || p.binding.generation != generation {
		h.mu.Unlock()
		return
	}
	p.mu.Lock()
	intent, found := p.intents[requestID]
	rotate := found && intent.sequence == residentMaxIntents && p.sequence == residentMaxIntents && p.pending == nil
	if rotate {
		p.ready = false
	}
	p.mu.Unlock()
	h.mu.Unlock()
	if rotate {
		_ = p.ws.Close(4000, "resident generation collected")
	}
}
