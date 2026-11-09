package residentclient

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// dictionaryCommand validates the entire finite command before any dispatch.
// '-' denotes only the empty list cursor, never an ID or a wildcard scope.
func dictionaryCommand(ctx context.Context, session Session, args []string) (json.RawMessage, error) {
	d, ok := session.(DictionarySession)
	if !ok {
		return nil, ErrUnavailable
	}
	switch args[0] {
	case "list":
		if len(args) != 3 || args[1] == "" {
			return nil, ErrProtocol
		}
		cursor := args[1]
		if cursor == "-" {
			cursor = ""
		}
		n, ok := canonicalNumber(args[2])
		if !ok || n < 1 || n > 32 || (cursor != "" && !agentwire.ValidCommandID(cursor)) {
			return nil, ErrProtocol
		}
		return d.List(ctx, cursor, int(n))
	case "get_execution":
		if len(args) != 2 || !agentwire.ValidCommandID(args[1]) {
			return nil, ErrProtocol
		}
		return d.GetExecution(ctx, args[1])
	case "read_output":
		if len(args) != 4 || !agentwire.ValidCommandID(args[1]) {
			return nil, ErrProtocol
		}
		cursor, ok := canonicalNumber(args[2])
		if !ok || cursor > maxInteger {
			return nil, ErrProtocol
		}
		n, ok := canonicalNumber(args[3])
		if !ok || n < 1 || n > 4096 {
			return nil, ErrProtocol
		}
		return d.ReadOutput(ctx, args[1], cursor, int(n))
	}
	return nil, ErrProtocol
}
func canonicalNumber(s string) (uint64, bool) {
	n, e := strconv.ParseUint(s, 10, 64)
	return n, e == nil && strconv.FormatUint(n, 10) == s
}
