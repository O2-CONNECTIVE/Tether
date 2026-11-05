package main

import (
	"strconv"
	"strings"
)

// ValidateObservedSubject checks measured facts, not authentication or approval.
// It recomputes service membership from attributes; no reported bool admits it.
func ValidateObservedSubject(v TokenObservation) (failures []Failure) {
	bad := func(stage string) { failures = append(failures, Failure{Stage: stage, Win32: 13}) }
	if v.User.SID != "S-1-5-18" {
		bad("identity_not_system")
	}
	if v.Type != 1 {
		bad("token_not_primary")
	}
	enabled := false
	if serviceSID(v.ExpectedServiceSID) {
		for _, g := range v.Groups {
			if g.SID == v.ExpectedServiceSID && g.Attributes&4 != 0 && g.Attributes&16 == 0 {
				enabled = true
			}
		}
	}
	if !enabled {
		bad("service_sid_missing_or_disabled")
	}
	return
}
func serviceSID(s string) bool {
	p := strings.Split(s, "-")
	if len(p) != 9 || strings.Join(p[:4], "-") != "S-1-5-80" {
		return false
	}
	for _, v := range p[4:] {
		n, e := strconv.ParseUint(v, 10, 32)
		if e != nil || strconv.FormatUint(n, 10) != v {
			return false
		}
	}
	return true
}
