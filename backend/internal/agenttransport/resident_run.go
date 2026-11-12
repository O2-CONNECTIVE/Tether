package agenttransport

import (
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

const (
	ResidentRunOperation  = agentgateway.ResidentRunID
	ResidentRunMaxTimeout = 240000 * time.Millisecond
	residentRunMaxCwd     = 1024
)

// residentRunScope is the command scope the gateway signed for a command-run
// intent. The zero value means the intent is the fixed operation.
type residentRunScope struct {
	commandSHA256, cwd string
	timeoutMillis      uint32
}

func (i ResidentFixedIntent) isRun() bool { return i.run != residentRunScope{} }

// CommandSHA256 is the lowercase hex SHA-256 of the command bytes, or "" for the
// fixed operation. The command itself travels on the agent channel; the
// executor must compare it to this hash.
func (i ResidentFixedIntent) CommandSHA256() string { return i.run.commandSHA256 }

// Cwd is a canonical slash-separated path relative to the Mac's work root, or
// "" for the fixed operation. It is lexically inside that root; the executor
// still owns symlink resolution.
func (i ResidentFixedIntent) Cwd() string { return i.run.cwd }

func (i ResidentFixedIntent) runValues() []string {
	return []string{i.generation, strconv.FormatUint(i.sequence, 10), i.requestID, ResidentRunOperation, strconv.FormatInt(i.deadline.UnixMilli(), 10), strconv.FormatUint(uint64(i.run.timeoutMillis), 10), i.run.commandSHA256, i.run.cwd}
}

// runWellFormed holds every bound that needs no clock or receiver state. Both ends
// call it: the gateway never signs, and the Mac never accepts, a value outside it.
// Format controls are refused with control characters because the Mac shows cwd
// to the person who allows the command.
func (i ResidentFixedIntent) runWellFormed(b residentBinding) bool {
	c := i.run.cwd
	return i.generation == b.generation && i.sequence >= 1 && i.sequence <= residentMaxIntents && residentHex(i.requestID, 16) &&
		len(strconv.FormatInt(i.deadline.UnixMilli(), 10)) == 13 && i.run.timeoutMillis >= 1 && time.Duration(i.run.timeoutMillis)*time.Millisecond <= ResidentRunMaxTimeout && residentHex(i.run.commandSHA256, 32) &&
		c != "" && len(c) <= residentRunMaxCwd && utf8.ValidString(c) && strings.IndexFunc(c, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0 &&
		path.Clean(c) == c && c[0] != '/' && c != ".." && !strings.HasPrefix(c, "../")
}

func residentEncodeRun(b residentBinding, i ResidentFixedIntent, s ssh.Signer) ([]byte, error) {
	if !i.runWellFormed(b) {
		return nil, ErrRefused
	}
	v := i.runValues()
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

func residentDecodeRun(b residentBinding, msg []byte, key ssh.PublicKey, now time.Time, next uint64, seen map[string]bool) (ResidentFixedIntent, error) {
	m, ok := residentFields(msg, "resident_run", "generation", "sequence", "request_id", "operation", "deadline_unix_ms", "timeout_ms", "command_sha256", "cwd", "signature")
	if !ok {
		return ResidentFixedIntent{}, ErrRefused
	}
	seq, ok := residentDecimal(m["sequence"])
	ms, okTime := residentDecimal(m["deadline_unix_ms"])
	timeout, okTimeout := residentDecimal(m["timeout_ms"])
	sig, okSig := residentSignature(m)
	if !ok || !okTime || !okTimeout || !okSig || timeout > uint64(ResidentRunMaxTimeout/time.Millisecond) {
		return ResidentFixedIntent{}, ErrRefused
	}
	i := ResidentFixedIntent{endpoint: b.endpoint, host: b.host, principal: b.principal, generation: m["generation"], sequence: seq, requestID: m["request_id"], deadline: time.UnixMilli(int64(ms)), run: residentRunScope{commandSHA256: m["command_sha256"], cwd: m["cwd"], timeoutMillis: uint32(timeout)}}
	if !i.runWellFormed(b) || seq != next || seen[i.requestID] || m["operation"] != ResidentRunOperation || !now.Before(i.deadline) || i.deadline.After(now.Add(time.Duration(i.run.timeoutMillis)*time.Millisecond)) || verify(key, residentTranscript(b, "gateway-run", i.runValues()...), sig) != nil {
		return ResidentFixedIntent{}, ErrRefused
	}
	return i, nil
}
