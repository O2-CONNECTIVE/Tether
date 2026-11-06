package agentgateway

import "context"

const MaxResidentResults = 32

// Match the existing process-lifetime attachment replay bound. Exhaustion is
// explicit refusal, never eviction/restart/permission to execute an old ID.
const MaxResidentReplayRecords = MaxAttachments

type residentTombstone struct {
	generation string
	value      map[string]any
}

func tombstoneValue(t residentTombstone, generation string) map[string]any {
	if t.generation != generation {
		return map[string]any{"error": "request_generation_mismatch"}
	}
	v := make(map[string]any, len(t.value))
	for k, x := range t.value {
		v[k] = x
	}
	// Allocate afresh: callers cannot mutate retained replay evidence.
	v["chunks"] = []map[string]any{}
	if exit, ok := t.value["exit"].(map[string]any); ok {
		copy := map[string]any{}
		for k, x := range exit {
			copy[k] = x
		}
		v["exit"] = copy
	}
	return v
}
func (g *Gateway) residentResultCountLocked() int {
	n := 0
	if g.resident != nil {
		for _, e := range g.resident.entries {
			n += len(e.works)
		}
	}
	return n
}
func (g *Gateway) residentRecordCountLocked() int {
	n := 0
	if g.resident != nil {
		for _, e := range g.resident.entries {
			n += len(e.works) + len(e.tombstones)
		}
	}
	return n
}
func (g *Gateway) residentGenerationCollectedLocked(e *residentEntry, generation string) bool {
	for _, w := range e.works {
		if w.binding.Generation == generation && (!w.done || w.ownerCleanup != "confirmed" || w.gatewayCleanup != "confirmed") {
			return false
		}
	}
	return e.current == nil
}

// FixedRelease is explicit payload acknowledgement. It issues no work/stop or
// cleanup. The old session generation may acknowledge its original result after
// transport rotation, but cannot release another generation's work.
func (s *residentSession) FixedRelease(ctx context.Context, id string) map[string]any {
	if ctx == nil || ctx.Err() != nil {
		return map[string]any{"error": "not_released"}
	}
	g := s.gateway
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := s.entry.tombstones[id]; ok {
		return tombstoneValue(t, s.generation)
	}
	w := s.entry.works[id]
	if w == nil || w.binding.Generation != s.generation {
		return map[string]any{"error": "not_found"}
	}
	joined := false
	if w.completed != nil {
		select {
		case <-w.completed:
			joined = true
		default:
		}
	}
	if !joined || !w.done || w.phase != "finished" || w.ownerCleanup != "confirmed" || w.gatewayCleanup != "confirmed" || s.entry.current == w {
		return map[string]any{"error": "cleanup_not_confirmed"}
	}
	v := residentValue(w)
	v["chunks"] = []map[string]any{}
	v["result_released"] = true
	if s.entry.tombstones == nil {
		s.entry.tombstones = map[string]residentTombstone{}
	}
	t := residentTombstone{generation: s.generation, value: v}
	s.entry.tombstones[id] = t
	delete(s.entry.works, id)
	return tombstoneValue(t, s.generation)
}
