package serviceprovision

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"golang.org/x/crypto/ssh"
)

const agentPurposeProtocol = "rescue-windows-agent-purpose/1"
const agentPurposeName = "single-guest-command"
const agentPurposeBytes = 4096
const agentPurposeMaximum = 5 * time.Minute

type agentPurposeWire struct {
	Protocol           string   `json:"protocol"`
	Purpose            string   `json:"purpose"`
	ServiceDigest      string   `json:"serviceDigest"`
	RuntimeGuestDigest string   `json:"runtimeGuestDigest"`
	OperatorKeys       []string `json:"operatorKeys"`
	MaximumDuration    string   `json:"maximumDurationNs"`
}

type agentPurposeRecord struct {
	serviceDigest, runtimeGuestDigest [32]byte
	keys, fingerprints                []string
	keyDigest                         [32]byte
	maximum                           time.Duration
}

// Separate fixed local input, not a servicecontrol message or approval. Parsing
// derives no authority, and never interprets the old SSH operationMode as agent.
func decodeAgentPurpose(data []byte) (agentPurposeRecord, error) {
	reject := func() (agentPurposeRecord, error) { return agentPurposeRecord{}, ErrRecord }
	if len(data) < 2 || len(data) > agentPurposeBytes || !utf8.Valid(data) || !recordUnicode(data) || data[0] != '{' {
		return reject()
	}
	body := data
	if body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	if len(body) < 2 || body[len(body)-1] != '}' {
		return reject()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := recordValue(decoder, 1)
	if err != nil || !recordShape(value, reflect.TypeOf(agentPurposeWire{})) {
		return reject()
	}
	if _, err = decoder.Token(); err != io.EOF {
		return reject()
	}
	var wire agentPurposeWire
	if json.Unmarshal(body, &wire) != nil || wire.Protocol != agentPurposeProtocol || wire.Purpose != agentPurposeName {
		return reject()
	}
	duration, err := strconv.ParseInt(wire.MaximumDuration, 10, 64)
	if err != nil || duration <= 0 || duration > int64(agentPurposeMaximum) || strconv.FormatInt(duration, 10) != wire.MaximumDuration {
		return reject()
	}
	record := agentPurposeRecord{maximum: time.Duration(duration)}
	for _, pair := range []struct {
		encoded string
		target  *[32]byte
	}{{wire.ServiceDigest, &record.serviceDigest}, {wire.RuntimeGuestDigest, &record.runtimeGuestDigest}} {
		raw, err := hex.DecodeString(pair.encoded)
		if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != pair.encoded {
			return reject()
		}
		copy(pair.target[:], raw)
		if *pair.target == ([32]byte{}) {
			return reject()
		}
	}
	if len(wire.OperatorKeys) < 1 || len(wire.OperatorKeys) > 8 {
		return reject()
	}
	keys := make([]ssh.PublicKey, 0, len(wire.OperatorKeys))
	seen := make(map[string]bool)
	for _, encoded := range wire.OperatorKeys {
		if len(encoded) != 68 || seen[encoded] {
			return reject()
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded {
			return reject()
		}
		key, err := ssh.ParsePublicKey(raw)
		if err != nil || key.Type() != ssh.KeyAlgoED25519 || base64.StdEncoding.EncodeToString(key.Marshal()) != encoded {
			return reject()
		}
		seen[encoded] = true
		keys = append(keys, key)
		record.fingerprints = append(record.fingerprints, ssh.FingerprintSHA256(key))
	}
	record.keyDigest, err = runbackend.KeySetDigest(keys)
	if err != nil {
		return reject()
	}
	record.keys = append([]string(nil), wire.OperatorKeys...)
	slices.Sort(record.fingerprints)
	return record, nil
}
