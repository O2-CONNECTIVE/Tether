//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"unsafe"

	"golang.org/x/sys/windows"
)

func tokenBuffer(ctx context.Context, token windows.Token, class uint32, stage string) ([]byte, []Failure) {
	if ctx.Err() != nil {
		return nil, contextFailures(ctx)
	}
	buffer := make([]byte, maxQueryBytes)
	var size uint32
	if err := windows.GetTokenInformation(token, class, &buffer[0], uint32(len(buffer)), &size); err != nil {
		return nil, failure(stage, err)
	}
	if size == 0 || size > uint32(len(buffer)) {
		return nil, invalid(stage + "_bound")
	}
	return buffer[:size], nil
}

func readToken(ctx context.Context, token windows.Token, expectedServiceSID string) (value TokenObservation, failures []Failure) {
	value.ExpectedServiceSID = expectedServiceSID
	value.Groups, value.RestrictedSIDs, value.Privileges = []Group{}, []Group{}, []Privilege{}
	query := func(class uint32, stage string, decode func([]byte) bool) {
		memory, f := tokenBuffer(ctx, token, class, stage)
		failures = append(failures, f...)
		if len(f) == 0 && !decode(memory) {
			failures = append(failures, invalid(stage+"_layout")...)
		}
	}
	query(windows.TokenUser, "token_user", func(memory []byte) bool {
		var ok bool
		value.User, ok = decodeSIDAttributes(memory)
		return ok
	})
	query(windows.TokenGroups, "token_groups", func(memory []byte) bool {
		var ok bool
		value.Groups, ok = decodeGroups(memory)
		return ok
	})
	query(windows.TokenRestrictedSids, "token_restricted_sids", func(memory []byte) bool {
		var ok bool
		value.RestrictedSIDs, ok = decodeGroups(memory)
		return ok
	})
	query(windows.TokenPrivileges, "token_privileges", func(memory []byte) bool {
		offset, width := int(unsafe.Offsetof(windows.Tokenprivileges{}.Privileges)), int(unsafe.Sizeof(windows.LUIDAndAttributes{}))
		if len(memory) < offset {
			return false
		}
		count := int(binary.LittleEndian.Uint32(memory))
		if count > maxPrivileges || count > (len(memory)-offset)/width {
			return false
		}
		for i := 0; i < count; i++ {
			item := (*windows.LUIDAndAttributes)(unsafe.Pointer(&memory[offset+i*width]))
			value.Privileges = append(value.Privileges, Privilege{LUIDLow: item.Luid.LowPart, LUIDHigh: item.Luid.HighPart, Attributes: item.Attributes})
		}
		return true
	})
	query(windows.TokenOwner, "token_owner", func(memory []byte) bool {
		if len(memory) < int(unsafe.Sizeof(uintptr(0))) {
			return false
		}
		var ok bool
		value.OwnerSID, ok = sidInBuffer(memory, *(**windows.SID)(unsafe.Pointer(&memory[0])))
		return ok
	})
	query(windows.TokenDefaultDacl, "token_default_dacl", func(memory []byte) bool {
		if len(memory) < int(unsafe.Sizeof(uintptr(0))) {
			return false
		}
		acl := *(**windows.ACL)(unsafe.Pointer(&memory[0]))
		value.DefaultDACLNull = acl == nil
		if acl == nil {
			return true
		}
		data, ok := aclInBuffer(memory, acl)
		if !ok {
			return false
		}
		value.DefaultDACLHex = hex.EncodeToString(data)
		return true
	})
	query(windows.TokenIntegrityLevel, "token_integrity", func(memory []byte) bool {
		var ok bool
		value.Integrity, ok = decodeSIDAttributes(memory)
		return ok
	})
	for _, field := range []struct {
		class uint32
		stage string
		out   *uint32
	}{
		{windows.TokenType, "token_type", &value.Type},
		{windows.TokenSessionId, "token_session", &value.SessionID},
		{windows.TokenElevationType, "token_elevation_type", &value.ElevationType},
		{windows.TokenElevation, "token_elevated", &value.Elevated},
		{windows.TokenHasRestrictions, "token_has_restrictions", &value.HasRestrictions},
		{29 /* TokenIsAppContainer (TOKEN_INFORMATION_CLASS) */, "token_is_app_container", &value.IsAppContainer},
	} {
		query(field.class, field.stage, func(memory []byte) bool {
			if len(memory) != 4 {
				return false
			}
			*field.out = binary.LittleEndian.Uint32(memory)
			return true
		})
	}
	query(windows.TokenStatistics, "token_statistics", func(memory []byte) bool {
		// TOKEN_STATISTICS has eight-byte LUID/LARGE_INTEGER alignment on the
		// supported amd64/arm64 targets; its final ModifiedId ends at byte56.
		if len(memory) != 56 {
			return false
		}
		value.Statistics = TokenStatistics{
			TokenIDLow: binary.LittleEndian.Uint32(memory[0:]), TokenIDHigh: int32(binary.LittleEndian.Uint32(memory[4:])),
			AuthenticationIDLow: binary.LittleEndian.Uint32(memory[8:]), AuthenticationIDHigh: int32(binary.LittleEndian.Uint32(memory[12:])),
			ModifiedIDLow: binary.LittleEndian.Uint32(memory[48:]), ModifiedIDHigh: int32(binary.LittleEndian.Uint32(memory[52:])),
		}
		return true
	})
	for _, group := range value.Groups {
		if group.SID == expectedServiceSID && group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
			value.ServiceSIDPresentEnabled = true
		}
	}
	if value.User.SID != "S-1-5-19" {
		failures = append(failures, invalid("identity_not_local_service")...)
	}
	if !value.ServiceSIDPresentEnabled {
		failures = append(failures, invalid("service_sid_missing_or_disabled")...)
	}
	if value.Type != windows.TokenPrimary {
		failures = append(failures, invalid("token_not_primary")...)
	}
	return value, failures
}

func decodeSIDAttributes(memory []byte) (Group, bool) {
	if len(memory) < int(unsafe.Sizeof(windows.SIDAndAttributes{})) {
		return Group{}, false
	}
	item := (*windows.SIDAndAttributes)(unsafe.Pointer(&memory[0]))
	sid, ok := sidInBuffer(memory, item.Sid)
	return Group{SID: sid, Attributes: item.Attributes}, ok
}

func decodeGroups(memory []byte) ([]Group, bool) {
	result := []Group{}
	offset, width := int(unsafe.Offsetof(windows.Tokengroups{}.Groups)), int(unsafe.Sizeof(windows.SIDAndAttributes{}))
	if len(memory) < offset {
		return result, false
	}
	count := int(binary.LittleEndian.Uint32(memory))
	if count > maxGroups || count > (len(memory)-offset)/width {
		return result, false
	}
	for i := 0; i < count; i++ {
		item := (*windows.SIDAndAttributes)(unsafe.Pointer(&memory[offset+i*width]))
		sid, ok := sidInBuffer(memory, item.Sid)
		if !ok {
			return result, false
		}
		result = append(result, Group{SID: sid, Attributes: item.Attributes})
	}
	return result, true
}

func aclInBuffer(memory []byte, acl *windows.ACL) ([]byte, bool) {
	if !contained(memory, unsafe.Pointer(acl), 8) {
		return nil, false
	}
	header := unsafe.Slice((*byte)(unsafe.Pointer(acl)), 8)
	size := uintptr(binary.LittleEndian.Uint16(header[2:]))
	if size < 8 || size > maxQueryBytes || !contained(memory, unsafe.Pointer(acl), size) {
		return nil, false
	}
	valid, _, _ := isValidACL.Call(uintptr(unsafe.Pointer(acl)))
	if valid == 0 {
		return nil, false
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(acl)), int(size)), true
}

var isValidACL = advapi.NewProc("IsValidAcl")
