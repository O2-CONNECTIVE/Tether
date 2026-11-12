package agenttransport

import (
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

const (
	ResidentRunOperation  = "tether-run-command-v1"
	ResidentRunMaxTimeout = 240000 * time.Millisecond
	residentRunMaxCwd     = 1024
)

// ResidentRunIntent is validated wire correlation plus the command scope the
// gateway signed, never an execution grant. Its zero value is invalid. It is a
// separate type from ResidentFixedIntent so the fixed-operation path, which
// promises one known command, cannot be handed a run intent.
type ResidentRunIntent struct {
	generation, requestID, commandSHA256, cwd string
	sequence                                  uint64
	deadline                                  time.Time
	timeoutMillis                             uint32
}

func (i ResidentRunIntent) RequestID() string     { return i.requestID }
func (i ResidentRunIntent) Generation() string    { return i.generation }
func (i ResidentRunIntent) Deadline() time.Time   { return i.deadline }
func (i ResidentRunIntent) Operation() string     { return ResidentRunOperation }
func (i ResidentRunIntent) TimeoutMillis() uint32 { return i.timeoutMillis }

// CommandSHA256 is the lowercase hex SHA-256 of the command bytes. The command
// itself travels on the agent channel; the executor must compare it to this hash.
func (i ResidentRunIntent) CommandSHA256() string { return i.commandSHA256 }

// Cwd is a canonical slash-separated path relative to the Mac's work root. It
// is lexically inside that root; the executor still owns symlink resolution.
func (i ResidentRunIntent) Cwd() string { return i.cwd }

func (i ResidentRunIntent) values() []string {
	return []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, ResidentRunOperation, strconv.FormatInt(i.deadline.UnixMilli(), 10), strconv.FormatUint(uint64(i.timeoutMillis), 10), i.commandSHA256, i.cwd}
}

// wellFormed holds every bound that needs no clock or receiver state. Both ends
// call it: the gateway never signs, and the Mac never accepts, a value outside it.
// Format controls are refused with control characters because the Mac shows cwd
// to the person who allows the command.
func (i ResidentRunIntent) wellFormed(b residentBinding) bool {
	c := i.cwd
	return i.generation == b.generation && i.sequence >= 1 && i.sequence <= residentMaxIntents && residentHex(i.requestID, 16) &&
		len(strconv.FormatInt(i.deadline.UnixMilli(), 10)) == 13 && i.timeoutMillis >= 1 && time.Duration(i.timeoutMillis)*time.Millisecond <= ResidentRunMaxTimeout && residentHex(i.commandSHA256, 32) &&
		c != "" && len(c) <= residentRunMaxCwd && utf8.ValidString(c) && strings.IndexFunc(c, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0 &&
		path.Clean(c) == c && c[0] != '/' && c != ".." && !strings.HasPrefix(c, "../")
}

func residentEncodeRun(b residentBinding, i ResidentRunIntent, s ssh.Signer) ([]byte, error) {
	if !i.wellFormed(b) {
		return nil, ErrRefused
	}
	v := i.values()
	sig, e := sign(s, s.PublicKey(), residentTranscript(b, "gateway-run", v...))
	if e != nil {
		return nil, e
	}
	msg := residentJSON(map[string]string{"type": "resident_run", "generation": v[0], "sequence": v[1], "request_id": v[2], "operation": v[3], "deadline_unix_ms": v[4], "timeout_ms": v[5], "command_sha256": v[6], "cwd": v[7], "signature": b64.EncodeToString(sig)})
	if len(msg) > MaxRegistrationMessage {
		return nil, ErrRefused
	}
	return msg, nil
}

func residentDecodeRun(b residentBinding, msg []byte, key ssh.PublicKey, now time.Time, next uint64, seen map[string]bool) (ResidentRunIntent, error) {
	m, ok := residentFields(msg, "resident_run", "generation", "sequence", "request_id", "operation", "deadline_unix_ms", "timeout_ms", "command_sha256", "cwd", "signature")
	if !ok {
		return ResidentRunIntent{}, ErrRefused
	}
	seq, ok := residentDecimal(m["sequence"])
	ms, okTime := residentDecimal(m["deadline_unix_ms"])
	timeout, okTimeout := residentDecimal(m["timeout_ms"])
	sig, okSig := residentSignature(m)
	if !ok || !okTime || !okTimeout || !okSig || timeout > uint64(ResidentRunMaxTimeout/time.Millisecond) {
		return ResidentRunIntent{}, ErrRefused
	}
	i := ResidentRunIntent{generation: m["generation"], sequence: seq, requestID: m["request_id"], deadline: time.UnixMilli(int64(ms)), timeoutMillis: uint32(timeout), commandSHA256: m["command_sha256"], cwd: m["cwd"]}
	if !i.wellFormed(b) || seq != next || seen[i.requestID] || m["operation"] != ResidentRunOperation || !now.Before(i.deadline) || i.deadline.After(now.Add(time.Duration(i.timeoutMillis)*time.Millisecond)) || verify(key, residentTranscript(b, "gateway-run", i.values()...), sig) != nil {
		return ResidentRunIntent{}, ErrRefused
	}
	return i, nil
}
