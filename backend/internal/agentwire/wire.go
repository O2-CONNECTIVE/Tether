// Package agentwire carries agent command requests for one bound
// agentexec.Session over an already authenticated byte stream. Frames are a
// 4-byte big-endian length followed by one strict JSON object. The wire has no
// Binding, mode, profile, epoch, host, path, token or issuer field: the Server
// injects the fixed Binding and the policy-derived mode of its Session, and
// reports the Session profile read-only. It does not authenticate peers, open
// sockets, register with a relay, retry, or resume after the stream ends.
package agentwire

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

const (
	// ProtocolVersion is the only accepted "v" value in either direction.
	ProtocolVersion = 1
	MaxFrame        = 128 << 10
	MaxCommandBytes = 64 << 10 // base64 of this still fits in MaxFrame
	MaxOutputBytes  = 48 << 10 // data bytes requested by one Output
	MaxRequestID    = 48       // the Server adds a 13-byte per-stream namespace
	maxServerCalls  = 4        // concurrent handlers per connection
	maxClientCalls  = 8        // outstanding requests per Client
	MaxAbandoned    = 32       // calls given up without a reply, awaiting a late one
	chunkCost       = 96       // JSON bytes charged per chunk besides its data
	chunkBudget     = MaxFrame - 4096
	prefixBytes     = 6 // 48 random bits; hex plus "." keeps core IDs within 64
)

var (
	ErrUnconfigured = errors.New("agentwire: endpoint is unconfigured")
	ErrUsed         = errors.New("agentwire: server already served a stream")
	ErrProtocol     = errors.New("agentwire: peer violated the wire protocol")
	ErrClosed       = errors.New("agentwire: stream is closed")
	// ErrNotSent proves the request was never handed to the stream.
	ErrNotSent = errors.New("agentwire: request was not sent")
	// ErrUncertain means the request may have reached the server but no reply
	// was observed. The Client never resends it.
	ErrUncertain      = errors.New("agentwire: request outcome is unknown")
	ErrRemoteInternal = errors.New("agentwire: server reported an internal error")
	// ErrPeer rejects a channel peer key outside the grant's approved key set.
	ErrPeer = errors.New("agentwire: channel peer is not an approved key of this grant")
	// ErrPeerLost reports that the owner's liveness signal declared the peer gone.
	ErrPeerLost = errors.New("agentwire: channel liveness was lost")
)

// Status is the core status plus the Session profile, which the Server reports
// read-only so that a caller can tell which guarantees apply.
type Status struct {
	agentexec.Status
	Profile agentexec.Profile
}

const (
	opStart  = "start"
	opStatus = "status"
	opOutput = "output"
	opCancel = "cancel"
)

// opKeys lists every member each request carries; all are required.
var opKeys = map[string][]string{
	opStart:  {"v", "id", "op", "requestId", "command", "timeoutMillis"},
	opStatus: {"v", "id", "op", "commandId"},
	opOutput: {"v", "id", "op", "commandId", "cursor", "maxBytes"},
	opCancel: {"v", "id", "op", "commandId"},
}

var statusKeys = []string{"profile", "commandId", "phase", "exitKnown", "exitCode", "stop", "cleanup", "terminal", "outputEnd", "truncated", "dropped"}
var outputKeys = []string{"chunks", "next", "gap", "truncated", "dropped", "complete"}
var chunkKeys = []string{"stream", "offset", "data"}

// coreErrors is the exact mapping between core sentinels and wire codes.
var coreErrors = []struct {
	code string
	err  error
}{
	{"unconfigured", agentexec.ErrUnconfigured},
	{"version", agentexec.ErrVersion},
	{"invalid", agentexec.ErrInvalid},
	{"binding", agentexec.ErrBinding},
	{"denied", agentexec.ErrDenied},
	{"restricted_undefined", agentexec.ErrRestrictedUndefined},
	{"request_mismatch", agentexec.ErrRequestMismatch},
	{"busy", agentexec.ErrBusy},
	{"cleanup_unknown", agentexec.ErrCleanupUnknown},
	{"not_found", agentexec.ErrNotFound},
	{"cursor", agentexec.ErrCursor},
	{"exhausted", agentexec.ErrExhausted},
	{"policy", agentexec.ErrPolicy},
	{"profile", agentexec.ErrProfile},
	{"identifier", agentexec.ErrIdentifier},
	{"session_claimed", agentexec.ErrSessionClaimed},
}

// CoreCode returns the stable code of an agentexec sentinel in err, or "".
func CoreCode(err error) string {
	for _, e := range coreErrors {
		if errors.Is(err, e.err) {
			return e.code
		}
	}
	return ""
}

var phaseNames = map[agentexec.Phase]string{
	agentexec.PhaseStarting: "starting", agentexec.PhaseRunning: "running", agentexec.PhaseExited: "exited",
	agentexec.PhaseCanceled: "canceled", agentexec.PhaseFailed: "failed", agentexec.PhaseStartUnknown: "start_unknown",
}
var stopNames = map[agentexec.StopReason]string{
	agentexec.StopNone: "none", agentexec.StopCanceled: "canceled", agentexec.StopCommandTimeout: "command_timeout",
	agentexec.StopRevoked: "revoked", agentexec.StopHardDeadline: "hard_deadline", agentexec.StopTransportLost: "transport_lost",
	agentexec.StopRuntimeFault: "runtime_fault",
}
var cleanupNames = map[agentexec.Cleanup]string{
	agentexec.CleanupPending: "pending", agentexec.CleanupConfirmed: "confirmed",
	agentexec.CleanupUnknown: "unknown", agentexec.CleanupNotRequired: "not_required",
}
var streamNames = map[agentexec.Stream]string{agentexec.Stdout: "stdout", agentexec.Stderr: "stderr"}
var profileNames = map[agentexec.Profile]string{
	agentexec.ProfileWindowsGuest: "windows_guest", agentexec.ProfileMacCurrentUser: "mac_current_user",
}

// PhaseName, StopName, CleanupName, StreamName and ProfileName are the shared
// vocabulary of the wire and the MCP adapter. Unknown values map to "".
func PhaseName(p agentexec.Phase) string     { return phaseNames[p] }
func StopName(r agentexec.StopReason) string { return stopNames[r] }
func CleanupName(c agentexec.Cleanup) string { return cleanupNames[c] }
func StreamName(s agentexec.Stream) string   { return streamNames[s] }
func ProfileName(p agentexec.Profile) string { return profileNames[p] }

func lookup[K comparable](m map[K]string, name string) (K, bool) {
	for k, v := range m {
		if v == name {
			return k, true
		}
	}
	var zero K
	return zero, false
}

// ValidCommandID reports the opaque 32-digit lowercase hex form of the core.
func ValidCommandID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; !isDigit(c) && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidRequestID reports a caller-chosen idempotency key: 1 to MaxRequestID
// printable ASCII bytes without spaces.
func ValidRequestID(id string) bool {
	if len(id) == 0 || len(id) > MaxRequestID {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

func readFrame(r io.Reader) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err // io.EOF only at a frame boundary
	}
	n := binary.BigEndian.Uint32(head[:])
	if n == 0 || n > MaxFrame {
		return nil, ErrProtocol
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

func writeFrame(w io.Writer, b []byte) error {
	if len(b) == 0 || len(b) > MaxFrame {
		return ErrProtocol
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	_, err := w.Write(buf)
	return err
}

type request struct {
	id        uint64
	op        string
	requestID string
	command   []byte
	timeout   uint32
	commandID string
	cursor    uint64
	maxBytes  int
}

func (r request) encode() ([]byte, error) {
	m := map[string]any{"v": ProtocolVersion, "id": r.id, "op": r.op}
	switch r.op {
	case opStart:
		m["requestId"], m["command"], m["timeoutMillis"] = r.requestID, base64.StdEncoding.EncodeToString(r.command), r.timeout
	case opStatus, opCancel:
		m["commandId"] = r.commandID
	case opOutput:
		m["commandId"], m["cursor"], m["maxBytes"] = r.commandID, r.cursor, r.maxBytes
	default:
		return nil, ErrProtocol
	}
	return json.Marshal(m)
}

// decodeRequest accepts exactly the members of one op, none of them null.
// Value ranges are left to the Server and the core.
func decodeRequest(b []byte) (request, error) {
	if CheckJSON(b, false) != nil {
		return request{}, ErrProtocol
	}
	m, err := Fields(b)
	if err != nil {
		return request{}, ErrProtocol
	}
	var r request
	var v uint16
	if !field(m, "v", &v) || v != ProtocolVersion || !field(m, "id", &r.id) || r.id == 0 || !field(m, "op", &r.op) {
		return request{}, ErrProtocol
	}
	keys := opKeys[r.op]
	if keys == nil || len(m) != len(keys) {
		return request{}, ErrProtocol
	}
	for _, k := range keys {
		if _, present := m[k]; !present {
			return request{}, ErrProtocol
		}
	}
	var ok bool
	switch r.op {
	case opStart:
		r.command, ok = decodeBase64(m["command"])
		ok = ok && field(m, "requestId", &r.requestID) && field(m, "timeoutMillis", &r.timeout)
	case opStatus, opCancel:
		ok = field(m, "commandId", &r.commandID)
	case opOutput:
		ok = field(m, "commandId", &r.commandID) && field(m, "cursor", &r.cursor) && field(m, "maxBytes", &r.maxBytes)
	}
	if !ok {
		return request{}, ErrProtocol
	}
	return r, nil
}

func field(m map[string]json.RawMessage, key string, v any) bool {
	raw, ok := m[key]
	return ok && json.Unmarshal(raw, v) == nil
}

// decodeBase64 accepts only the canonical padded standard encoding.
func decodeBase64(raw json.RawMessage) ([]byte, bool) {
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return nil, false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || base64.StdEncoding.EncodeToString(b) != s {
		return nil, false
	}
	return b, true
}

func encodeReply(id uint64, key string, body any) []byte {
	b, err := json.Marshal(map[string]any{"v": ProtocolVersion, "id": id, key: body})
	if err != nil {
		b, _ = json.Marshal(map[string]any{"v": ProtocolVersion, "id": id, "error": "internal"})
	}
	return b
}

func errorReply(id uint64, err error) []byte {
	code := CoreCode(err)
	if code == "" {
		code = "internal"
	}
	return encodeReply(id, "error", code)
}

// statusReply reports an unnamed enum as "internal". After a Start the Client
// turns that into an uncertain outcome, never into "not started".
func statusReply(id uint64, st agentexec.Status, p agentexec.Profile) []byte {
	phase, okP := phaseNames[st.Phase]
	stop, okS := stopNames[st.Stop]
	cleanup, okC := cleanupNames[st.Cleanup]
	profile, okR := profileNames[p]
	if !okP || !okS || !okC || !okR || !ValidCommandID(st.CommandID) {
		return encodeReply(id, "error", "internal")
	}
	return encodeReply(id, "status", map[string]any{
		"profile": profile, "commandId": st.CommandID, "phase": phase, "exitKnown": st.Exit.Known, "exitCode": st.Exit.Code,
		"stop": stop, "cleanup": cleanup, "terminal": st.Terminal, "outputEnd": st.OutputEnd,
		"truncated": st.Truncated, "dropped": st.Dropped,
	})
}

// outputReply trims trailing chunks so that the frame stays bounded even when
// the guest wrote many tiny interleaved records. Trimmed bytes are not
// acknowledged: Next moves back to the first omitted chunk.
func outputReply(id uint64, out agentexec.Output) []byte {
	chunks := make([]any, 0, len(out.Chunks))
	used := 0
	for i, ch := range out.Chunks {
		stream, ok := streamNames[ch.Stream]
		if !ok || len(ch.Data) == 0 {
			return encodeReply(id, "error", "internal")
		}
		cost := chunkCost + base64.StdEncoding.EncodedLen(len(ch.Data))
		if i > 0 && used+cost > chunkBudget {
			out.Next, out.Complete = ch.Offset, false
			break
		}
		used += cost
		chunks = append(chunks, map[string]any{"stream": stream, "offset": ch.Offset, "data": base64.StdEncoding.EncodeToString(ch.Data)})
	}
	return encodeReply(id, "output", map[string]any{
		"chunks": chunks, "next": out.Next, "gap": out.Gap, "truncated": out.Truncated, "dropped": out.Dropped, "complete": out.Complete,
	})
}

func decodeStatus(raw json.RawMessage) (Status, bool) {
	m, err := Fields(raw, statusKeys...)
	if err != nil || len(m) != len(statusKeys) {
		return Status{}, false
	}
	var st Status
	var profile, phase, stop, cleanup string
	ok := field(m, "profile", &profile) && field(m, "commandId", &st.CommandID) && field(m, "phase", &phase) && field(m, "exitKnown", &st.Exit.Known) &&
		field(m, "exitCode", &st.Exit.Code) && field(m, "stop", &stop) && field(m, "cleanup", &cleanup) &&
		field(m, "terminal", &st.Terminal) && field(m, "outputEnd", &st.OutputEnd) && field(m, "truncated", &st.Truncated) &&
		field(m, "dropped", &st.Dropped)
	var okR, okP, okS, okC bool
	st.Profile, okR = lookup(profileNames, profile)
	st.Phase, okP = lookup(phaseNames, phase)
	st.Stop, okS = lookup(stopNames, stop)
	st.Cleanup, okC = lookup(cleanupNames, cleanup)
	return st, ok && okR && okP && okS && okC && ValidCommandID(st.CommandID)
}

// decodeOutput also checks that chunks are non-empty and contiguous from the
// requested cursor and carry at most maxBytes data bytes, so a peer cannot
// reorder, overlap or exceed the requested byte sequence.
func decodeOutput(raw json.RawMessage, cursor uint64, maxBytes int) (agentexec.Output, bool) {
	m, err := Fields(raw, outputKeys...)
	if err != nil || len(m) != len(outputKeys) {
		return agentexec.Output{}, false
	}
	var out agentexec.Output
	var list []json.RawMessage
	if !field(m, "next", &out.Next) || !field(m, "gap", &out.Gap) || !field(m, "truncated", &out.Truncated) ||
		!field(m, "dropped", &out.Dropped) || !field(m, "complete", &out.Complete) || !field(m, "chunks", &list) {
		return agentexec.Output{}, false
	}
	expect := cursor + out.Gap
	if expect < cursor {
		return agentexec.Output{}, false
	}
	total := 0
	for _, item := range list {
		cm, err := Fields(item, chunkKeys...)
		if err != nil || len(cm) != len(chunkKeys) {
			return agentexec.Output{}, false
		}
		var ch agentexec.Chunk
		var stream string
		var ok bool
		if !field(cm, "stream", &stream) || !field(cm, "offset", &ch.Offset) {
			return agentexec.Output{}, false
		}
		if ch.Stream, ok = lookup(streamNames, stream); !ok {
			return agentexec.Output{}, false
		}
		if ch.Data, ok = decodeBase64(cm["data"]); !ok || len(ch.Data) == 0 || ch.Offset != expect {
			return agentexec.Output{}, false
		}
		total += len(ch.Data)
		expect += uint64(len(ch.Data))
		if expect < ch.Offset || total > maxBytes || total > MaxOutputBytes {
			return agentexec.Output{}, false
		}
		out.Chunks = append(out.Chunks, ch)
	}
	return out, out.Next == expect
}
