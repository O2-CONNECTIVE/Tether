package residentclient

import (
	"context"
	"encoding/json"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// DictionarySession is an additive read port on the original session. It does
// not select a host, extend authority or acknowledge retained output.
type DictionarySession interface {
	List(context.Context, string, int) (json.RawMessage, error)
	GetExecution(context.Context, string) (json.RawMessage, error)
	ReadOutput(context.Context, string, uint64, int) (json.RawMessage, error)
}

func (c *client) List(ctx context.Context, cursor string, limit int) (json.RawMessage, error) {
	if (cursor != "" && !agentwire.ValidCommandID(cursor)) || limit < 1 || limit > 32 {
		return nil, ErrProtocol
	}
	return c.dictionaryCall(ctx, "list", map[string]any{"cursor": cursor, "limit": limit}, "", cursor, 0, limit)
}
func (c *client) GetExecution(ctx context.Context, id string) (json.RawMessage, error) {
	if !agentwire.ValidCommandID(id) {
		return nil, ErrProtocol
	}
	return c.dictionaryCall(ctx, "get_execution", map[string]any{"request_id": id}, id, "", 0, 0)
}
func (c *client) ReadOutput(ctx context.Context, id string, cursor uint64, maxBytes int) (json.RawMessage, error) {
	if !agentwire.ValidCommandID(id) || cursor > maxInteger || maxBytes < 1 || maxBytes > 4096 {
		return nil, ErrProtocol
	}
	return c.dictionaryCall(ctx, "read_output", map[string]any{"request_id": id, "cursor": cursor, "max_bytes": maxBytes}, id, "", cursor, maxBytes)
}
func (c *client) dictionaryCall(ctx context.Context, op string, args any, id, after string, cursor uint64, budget int) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.opened || c.sid == "" || c.failed || c.closed {
		return nil, ErrUnavailable
	}
	// No discovery is added to legacy startup. Read-only discovery is performed
	// once, lazily, on the same SID; missing optional tools never trigger calls.
	if c.dictionary == nil {
		raw, h, e := c.rpc(ctx, "tools/list", map[string]any{})
		if e != nil {
			return nil, e
		}
		if !c.sameSession(h) {
			c.failed = true
			return nil, ErrProtocol
		}
		available, e := dictionaryNames(raw)
		if e != nil {
			c.failed = true
			return nil, e
		}
		c.dictionary = available
	}
	name := "tether_fixed_" + op
	if !c.dictionary[name] {
		return json.RawMessage(`{"error":"unsupported"}`), nil
	}
	raw, h, e := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if e != nil {
		return nil, e
	}
	if !c.sameSession(h) {
		c.failed = true
		return nil, ErrProtocol
	}
	value, e := decodeDictionary(raw, op, id, after, cursor, budget)
	if e != nil {
		c.failed = true
	}
	return value, e
}
func dictionaryNames(raw []byte) (map[string]bool, error) {
	m, e := fields(raw, "tools")
	if e != nil || !exact(m, "tools") {
		return nil, ErrProtocol
	}
	var tools []json.RawMessage
	if len(m["tools"]) == 0 || m["tools"][0] != '[' || json.Unmarshal(m["tools"], &tools) != nil || len(tools) > 32 {
		return nil, ErrProtocol
	}
	names := map[string]bool{}
	for _, v := range tools {
		t, e := fields(v, "name", "title", "description", "inputSchema", "outputSchema", "annotations", "_meta")
		name, ok := str(t["name"])
		if e != nil || !ok || len(name) == 0 || len(name) > 128 || names[name] {
			return nil, ErrProtocol
		}
		if _, e = fields(t["inputSchema"]); e != nil {
			return nil, ErrProtocol
		}
		names[name] = true
	}
	return names, nil
}
