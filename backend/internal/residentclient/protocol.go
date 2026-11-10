// Package residentclient is a narrow client of the existing authenticated MCP
// service. It issues no grant, selects no principal and owns no host process.
package residentclient

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

var ErrProtocol = errors.New("work client: invalid bounded protocol")
var ErrUnavailable = errors.New("work client: session unavailable or uncertain")

const maxInteger = uint64(9007199254740991)

func fields(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || agentwire.CheckJSON(raw, false) != nil { return nil, ErrProtocol }
	m, err := agentwire.Fields(raw, names...)
	if err != nil { return nil, ErrProtocol }
	return m, nil
}
func exact(m map[string]json.RawMessage, names ...string) bool {
	if len(m) != len(names) { return false }
	for _, name := range names { if _, ok := m[name]; !ok { return false } }
	return true
}
func str(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw)==0 || raw[0]!='"' || json.Unmarshal(raw,&s)!=nil {return "",false}
	return s,true
}
func text(raw json.RawMessage) string { s, _ := str(raw); return s }
func boolean(raw json.RawMessage) (bool, bool) {
	if string(raw)=="true" { return true,true }
	return false,string(raw)=="false"
}
func integer(raw json.RawMessage) (uint64, bool) {
	n,ok:=counter(raw)
	return n,ok && n<=maxInteger
}
// Existing output counters span uint64, independently of JSON-safe RPC IDs.
func counter(raw json.RawMessage) (uint64, bool) {
	n,e:=strconv.ParseUint(string(raw),10,64)
	return n,e==nil && strconv.FormatUint(n,10)==string(raw)
}
func enum(raw json.RawMessage, values ...string) bool {
	s,ok:=str(raw);if !ok{return false}
	for _,v:=range values {if s==v{return true}}
	return false
}
func operation(op string) string {
	switch op {
	case "start": return "tether_fixed_start"
	case "result": return "tether_fixed_status"
	case "cancel": return "tether_fixed_cancel"
	case "release": return "tether_fixed_release"
	}
	return ""
}
