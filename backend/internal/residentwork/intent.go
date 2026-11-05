// Package residentwork defines only the signed resident operation intent.
// It has no file, key, grant, runtime, network or approval producer.
package residentwork

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/url"
	"strings"
)

const Version uint16 = 1

var ErrIntent = errors.New("residentwork: invalid fixed intent")

type Binding struct{ Endpoint, HostLabel, PrincipalLabel string }
type Intent struct {
	Version                                uint16
	SourceNonce, ServerNonce, PolicySHA256 [32]byte
	RequestID, OperationID                 string
	ExpiresUnixMilli                       int64
	Signature                              []byte
}
type Operation struct {
	ID            string
	CommandSHA256 [32]byte
	TimeoutMillis uint32
}

func ValidOperationID(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func ValidRequestID(s string) bool {
	if len(s) < 1 || len(s) > 48 {
		return false
	}
	for _, c := range []byte(s) {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}
func validBinding(b Binding) bool {
	u, e := url.Parse(b.Endpoint)
	return e == nil && len(b.Endpoint) <= 256 && u.Scheme == "wss" && u.Host != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawPath == "" && u.Opaque == "" && u.Path != "" && u.Path != "/" && strings.ToLower(u.Host) == u.Host && u.String() == b.Endpoint && ValidOperationID(b.HostLabel) && ValidOperationID(b.PrincipalLabel) && !strings.Contains(b.HostLabel, "_") && !strings.Contains(b.PrincipalLabel, "_")
}
func prefix(domain string, b Binding, s, n, p [32]byte) (*bytes.Buffer, error) {
	if !validBinding(b) || s == ([32]byte{}) || n == ([32]byte{}) || p == ([32]byte{}) || s == n {
		return nil, ErrIntent
	}
	out := bytes.NewBufferString(domain)
	for _, v := range []string{b.Endpoint, b.HostLabel, b.PrincipalLabel} {
		field(out, v)
	}
	out.Write(s[:])
	out.Write(n[:])
	out.Write(p[:])
	return out, nil
}
func field(out *bytes.Buffer, s string) {
	_ = binary.Write(out, binary.BigEndian, uint32(len(s)))
	out.WriteString(s)
}
func IntentTranscript(b Binding, i Intent) ([]byte, error) {
	if i.Version != Version || !ValidRequestID(i.RequestID) || !ValidOperationID(i.OperationID) || i.ExpiresUnixMilli <= 0 {
		return nil, ErrIntent
	}
	out, e := prefix("rescue-host.resident-work-intent\x00v1\x00", b, i.SourceNonce, i.ServerNonce, i.PolicySHA256)
	if e != nil {
		return nil, e
	}
	field(out, i.RequestID)
	field(out, i.OperationID)
	_ = binary.Write(out, binary.BigEndian, i.ExpiresUnixMilli)
	return out.Bytes(), nil
}
func HostProofTranscript(b Binding, s, n, p [32]byte) ([]byte, error) {
	out, e := prefix("rescue-host.resident-control-host-proof\x00v1\x00", b, s, n, p)
	if e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
