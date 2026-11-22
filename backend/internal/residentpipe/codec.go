package residentpipe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"
	"unicode/utf8"
)

const MaxLine = 4096 // includes the mandatory LF
const MaxInteger = uint64(9007199254740991)

var ErrProtocol = errors.New("invalid resident pipe protocol")

type Intent struct {
	ProtocolVersion  uint64 `json:"protocolVersion"`
	RequestID        uint64 `json:"requestId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	Intent           string `json:"intent"`
	// StandbyFor is how long an on or extend intent keeps standby on, and zero
	// otherwise. The JSON dialect defaults an omitted on to two hours (#399).
	StandbyFor time.Duration `json:"-"`
}
type jsonIntentRequest struct {
	ProtocolVersion  uint64  `json:"protocolVersion"`
	RequestID        uint64  `json:"requestId"`
	ExpectedRevision uint64  `json:"expectedRevision"`
	Intent           string  `json:"intent"`
	StandbyHours     *uint64 `json:"standbyHours,omitempty"`
}
type intentEnvelope struct {
	ProtocolVersion uint64            `json:"protocolVersion"`
	Type            string            `json:"type"`
	Request         jsonIntentRequest `json:"request"`
}
type Receipt struct {
	ProtocolVersion uint64 `json:"protocolVersion"`
	RequestID       uint64 `json:"requestId"`
	Intent          string `json:"intent"`
	Disposition     string `json:"disposition"`
}

// walk rejects duplicate keys at every level and all noncanonical numbers.
func walk(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return ErrProtocol
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return ErrProtocol
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return ErrProtocol
				}
				seen[s] = true
				if e = walk(d); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim('}') {
				return ErrProtocol
			}
		case '[':
			return ErrProtocol
		default:
			return ErrProtocol
		}
	case json.Number:
		s := string(v)
		n, e := strconv.ParseUint(s, 10, 64)
		if e != nil || n > MaxInteger || strconv.FormatUint(n, 10) != s {
			return ErrProtocol
		}
	}
	return nil
}
func Decode(line []byte) (Intent, error) {
	var envelope intentEnvelope
	if validateFrame(line) != nil {
		return Intent{}, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(line))
	d.UseNumber()
	if walk(d) != nil {
		return Intent{}, ErrProtocol
	}
	if _, e := d.Token(); e != io.EOF {
		return Intent{}, ErrProtocol
	}
	d = json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if d.Decode(&envelope) != nil {
		return Intent{}, ErrProtocol
	}
	wireRequest := envelope.Request
	r := Intent{ProtocolVersion: wireRequest.ProtocolVersion, RequestID: wireRequest.RequestID,
		ExpectedRevision: wireRequest.ExpectedRevision, Intent: wireRequest.Intent}
	// Required-field presence must not be confused with legitimate zero revision.
	var root map[string]json.RawMessage
	_ = json.Unmarshal(line, &root)
	var request map[string]json.RawMessage
	_ = json.Unmarshal(root["request"], &request)
	_, hasStandbyHours := request["standbyHours"]
	baseRequest := exactKeys(request, "protocolVersion", "requestId", "expectedRevision", "intent")
	requestWithHours := exactKeys(request, "protocolVersion", "requestId", "expectedRevision", "intent", "standbyHours")
	if !exactKeys(root, "protocolVersion", "type", "request") || (!baseRequest && !requestWithHours) || bytes.Equal(bytes.TrimSpace(request["expectedRevision"]), []byte("null")) || envelope.ProtocolVersion != 1 || envelope.Type != "intent" || r.ProtocolVersion != 1 || r.RequestID == 0 || (r.Intent != "on" && r.Intent != "off" && r.Intent != "extend") {
		return Intent{}, ErrProtocol
	}
	if hasStandbyHours {
		if bytes.Equal(bytes.TrimSpace(request["standbyHours"]), []byte("null")) || wireRequest.StandbyHours == nil || r.Intent == "off" {
			return Intent{}, ErrProtocol
		}
		switch *wireRequest.StandbyHours {
		case 2, 4, 8, 12, 24:
			r.StandbyFor = time.Duration(*wireRequest.StandbyHours) * time.Hour
		default:
			return Intent{}, ErrProtocol
		}
	} else if r.Intent == "extend" {
		return Intent{}, ErrProtocol
	}
	return r, nil
}
func Encode(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil || len(b)+1 > MaxLine {
		return nil, ErrProtocol
	}
	return append(b, '\n'), nil
}

func exactKeys(m map[string]json.RawMessage, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := m[key]; !ok {
			return false
		}
	}
	return true
}

func validateFrame(line []byte) error {
	if len(line) < 2 || len(line) > MaxLine || line[len(line)-1] != '\n' || bytes.ContainsAny(line[:len(line)-1], "\r\n") || !utf8.Valid(line) {
		return ErrProtocol
	}
	return nil
}
