package serviceprovision

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"golang.org/x/crypto/ssh"
)

func validValues(r Record) bool {
	path := r.Profile.SupervisorImage.Path
	exactCommand := r.ServiceCommandLine == `"`+path+`"` || !strings.ContainsAny(path, " \t") && r.ServiceCommandLine == path
	if !exactCommand || r.Endpoint != `\\.\pipe\rescue-service-`+r.Profile.ServiceName || len(r.Endpoint) > 256 || !validSecurity(r.Profile, r.ServiceSecurity, false) || !validSecurity(r.Profile, r.ProcessSecurity, true) {
		return false
	}
	for _, d := range []time.Duration{r.ReadyCleanupTimeout, r.Budgets.Startup, r.Budgets.Cleanup, r.Budgets.Observe, r.PollInterval, r.OperationTimeout, r.StdioCleanupTimeout, r.Limits.CleanupTimeout} {
		if d <= 0 {
			return false
		}
	}
	if !token(r.PolicyRef) || !token(r.HostKeyRef) || r.StderrBytes == 0 || r.Limits.FrameBytes != bootstrap.RecordBytes || r.Limits.Frames < 1 || int64(r.Limits.Frames) > 2147483647 {
		return false
	}
	if _, ok := r.Profile.Keys.Catalog[r.HostKeyRef]; !ok {
		return false
	}
	at := r.Profile.Keys.Provisioning.ProtectedAt
	parsed, err := time.Parse(time.RFC3339Nano, at.UTC().Format(time.RFC3339Nano))
	if err != nil || at.Location() != time.UTC || !parsed.Equal(at) {
		return false
	}
	return validBackend(r.Backend)
}
func validBackend(c bootstrap.Config) bool {
	if _, err := backend.PolicyDigest(c.OperationMode); err != nil {
		return false
	}
	if c.MaximumDuration <= 0 || c.MaximumDuration > 2*time.Hour || c.LeaseDuration <= 0 || c.LeaseDuration > time.Minute || c.LeaseDuration > c.MaximumDuration || c.MaxChannels < 1 || c.MaxChannels > 32 || c.MaxHandshakes < 1 || c.MaxHandshakes > 64 || c.MaxTransports < 1 || c.MaxTransports > 64 || len(c.AllowedKeys) < 1 || len(c.AllowedKeys) > 8 {
		return false
	}
	keys := make([]ssh.PublicKey, 0, len(c.AllowedKeys))
	for _, value := range c.AllowedKeys {
		if len(value) != 68 {
			return false
		}
		data, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || base64.StdEncoding.EncodeToString(data) != value {
			return false
		}
		key, err := ssh.ParsePublicKey(data)
		if err != nil || key.Type() != ssh.KeyAlgoED25519 || base64.StdEncoding.EncodeToString(key.Marshal()) != value {
			return false
		}
		keys = append(keys, key)
	}
	_, err := runbackend.KeySetDigest(keys)
	return err == nil
}
func validSecurity(p bootstrapauth.Profile, s serviceready.SecurityPolicy, process bool) bool {
	if !s.Protected || !trusted(p, s.OwnerSID) || !sid(s.OwnerSID) || s.ACL == nil || len(s.ACL) > 128 {
		return false
	}
	known, readonly := uint32(0xf00f01ff), uint32(0x2008d)
	if process {
		known = 0xf01fffff
		readonly = 0x121400
	}
	for _, a := range s.ACL {
		if a.Type > 1 || a.Flags != 0 || a.Mask == 0 || a.Mask & ^known != 0 || !sid(a.SID) {
			return false
		}
		// servicereadyと同じ静的候補上限。queryを含む場合は実Prepareで
		// 同じprocessのtoken objectを検査し、値の検証だけでreadyとしない。
		// DENYで危険ALLOWを相殺しない。
		if a.Type == 0 && !trusted(p, a.SID) && a.Mask & ^readonly != 0 {
			return false
		}
	}
	return true
}
func trusted(p bootstrapauth.Profile, s string) bool {
	return s != "" && s != p.Keys.GuestSID && (s == p.Keys.ProcessUserSID || s == p.Keys.ServiceSID || s == p.Keys.OperatorSID || s == "S-1-5-18" || s == "S-1-5-32-544")
}
func sid(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 4 || len(parts) > 18 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for i, p := range parts[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, err := strconv.ParseUint(p, 10, bits)
		if err != nil || strconv.FormatUint(n, 10) != p {
			return false
		}
	}
	return true
}
func token(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
