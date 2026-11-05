package agenttransport

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

const (
	ResidentSubprotocol    = "rescue-agent-resident.v1"
	ResidentFixedOperation = "tether-fixed-command-v1"
	ResidentFixedTimeout   = 15000 * time.Millisecond
	residentDomain         = "rescue-host.agenttransport.resident\x00v1\x00"
	residentMaxIntents     = 32
)

type residentBinding struct {
	endpoint, host, principal, generation           string
	clientNonce, serverNonce, hostKey, principalKey []byte
}

func residentTranscript(b residentBinding, role string, values ...string) []byte {
	var out []byte
	field := func(v []byte) { out = binary.BigEndian.AppendUint32(out, uint32(len(v))); out = append(out, v...) }
	for _, s := range []string{residentDomain, role, b.endpoint, b.host, b.principal, b.generation} {
		field([]byte(s))
	}
	for _, v := range [][]byte{b.clientNonce, b.serverNonce, b.hostKey, b.principalKey} {
		field(v)
	}
	for _, s := range values {
		field([]byte(s))
	}
	return out
}

func residentJSON(fields map[string]string) []byte { b, _ := json.Marshal(fields); return b }
func residentFields(msg []byte, kind string, fields ...string) (map[string]string, bool) {
	if len(msg) > MaxRegistrationMessage {
		return nil, false
	}
	m, ok := stringFields(msg, append([]string{"type"}, fields...)...)
	return m, ok && m["type"] == kind
}
func residentHex(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == n && hex.EncodeToString(b) == s
}
func residentDecimal(s string) (uint64, bool) {
	n, e := strconv.ParseUint(s, 10, 64)
	return n, e == nil && strconv.FormatUint(n, 10) == s
}
func residentSignature(m map[string]string) ([]byte, bool) { return decodeB64(m["signature"]) }
