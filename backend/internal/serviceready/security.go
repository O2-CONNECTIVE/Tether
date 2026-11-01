package serviceready

import (
	"fmt"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"reflect"
	"strconv"
	"strings"
)

func validSID(s string) bool {
	p := strings.Split(s, "-")
	if len(p) < 4 || len(p) > 18 || p[0] != "S" || p[1] != "1" {
		return false
	}
	for i, v := range p[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, e := strconv.ParseUint(v, 10, bits)
		if e != nil || strconv.FormatUint(n, 10) != v {
			return false
		}
	}
	return true
}
func trusted(p bootstrapauth.Profile, s string) bool {
	return s != "" && s != p.Keys.GuestSID && (s == p.Keys.ProcessUserSID || s == p.Keys.ServiceSID || s == p.Keys.OperatorSID || s == "S-1-5-18" || s == "S-1-5-32-544")
}
func validSecurity(p bootstrapauth.Profile, s SecurityPolicy, process bool) bool {
	if !s.Protected || !trusted(p, s.OwnerSID) || !validSID(s.OwnerSID) || s.ACL == nil || len(s.ACL) > 128 {
		return false
	}
	known, readonly := uint32(0xf00f01ff), uint32(0x2008d)
	if process {
		known = 0xf01fffff
		readonly = 0x121400
	}
	for _, a := range s.ACL {
		if a.Type > 1 || a.Flags != 0 || a.Mask == 0 || a.Mask & ^known != 0 || !validSID(a.SID) {
			return false
		}
		// DENYで危険ALLOWを相殺せず、未知主体の許可上限を広げない。
		// process queryは同じ保持tokenの独立検査がある場合だけPrepareで受理する。
		if a.Type == 0 && !trusted(p, a.SID) && a.Mask & ^readonly != 0 {
			return false
		}
	}
	return true
}
func sameSecurity(actual, expected SecurityPolicy) bool {
	return actual.Protected && reflect.DeepEqual(actual, expected)
}
func listenerSecurity(p bootstrapauth.Profile) SecurityPolicy {
	return SecurityPolicy{OwnerSID: p.Keys.ProcessUserSID, Protected: true, ACL: []protectedkey.ACE{{Type: 1, SID: p.Keys.GuestSID, Mask: 0x1f01ff}, {SID: p.Keys.ServiceSID, Mask: 0x1f01ff}, {SID: p.Keys.OperatorSID, Mask: 0x120183}}}
}
func listenerSDDL(p bootstrapauth.Profile) string {
	return fmt.Sprintf("O:%sD:P(D;;0x001f01ff;;;%s)(A;;0x001f01ff;;;%s)(A;;0x00120183;;;%s)", p.Keys.ProcessUserSID, p.Keys.GuestSID, p.Keys.ServiceSID, p.Keys.OperatorSID)
}
