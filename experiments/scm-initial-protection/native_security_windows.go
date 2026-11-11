//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	advapi          = windows.NewLazySystemDLL("advapi32.dll")
	getSecurityInfo = advapi.NewProc("GetSecurityInfo")
	convertSDDL     = advapi.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
)

func failure(stage string, err error) []Failure {
	if err == nil {
		return nil
	}
	var code syscall.Errno
	if errors.As(err, &code) {
		return []Failure{{Stage: stage, Win32: uint32(code)}}
	}
	return []Failure{{Stage: stage}}
}

func invalid(stage string) []Failure {
	return []Failure{{Stage: stage, Win32: uint32(windows.ERROR_INVALID_DATA)}}
}

// Allocations returned by the two Win32 descriptor APIs are always released
// explicitly and LocalFree errors propagate. x/sys convenience methods hide
// that error, so this probe uses the underlying read-only APIs directly.
func withSecurity(h windows.Handle, kind windows.SE_OBJECT_TYPE, stage string, inspect func(*windows.SECURITY_DESCRIPTOR, []byte) []Failure) (failures []Failure) {
	var sd *windows.SECURITY_DESCRIPTOR
	code, _, _ := getSecurityInfo.Call(uintptr(h), uintptr(kind), uintptr(windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION), 0, 0, 0, 0, uintptr(unsafe.Pointer(&sd)))
	if sd != nil {
		defer func() {
			_, err := windows.LocalFree(windows.Handle(unsafe.Pointer(sd)))
			failures = append(failures, failure(stage+"_free", err)...)
		}()
	}
	if code != 0 {
		return []Failure{{Stage: stage, Win32: uint32(code)}}
	}
	if sd == nil || !sd.IsValid() {
		return invalid(stage + "_invalid")
	}
	n := sd.Length()
	if n < 20 || n > maxQueryBytes {
		return invalid(stage + "_bound")
	}
	control, _, err := sd.Control()
	if err != nil {
		return failure(stage+"_control", err)
	}
	if control&windows.SE_SELF_RELATIVE == 0 {
		return invalid(stage + "_layout")
	}
	return inspect(sd, unsafe.Slice((*byte)(unsafe.Pointer(sd)), int(n)))
}

func readSecurity(h windows.Handle, kind windows.SE_OBJECT_TYPE, stage string) (s Security, failures []Failure) {
	failures = withSecurity(h, kind, stage, func(sd *windows.SECURITY_DESCRIPTOR, memory []byte) []Failure {
		owner, _, err := sd.Owner()
		if err != nil {
			return failure(stage+"_owner", err)
		}
		var ok bool
		s.OwnerSID, ok = sidInBuffer(memory, owner)
		if !ok {
			return invalid(stage + "_owner_bound")
		}
		control, _, err := sd.Control()
		if err != nil {
			return failure(stage+"_control", err)
		}
		s.DACLPresent = control&windows.SE_DACL_PRESENT != 0
		if s.DACLPresent {
			dacl, _, err := sd.DACL()
			if err != nil {
				return failure(stage+"_dacl", err)
			}
			s.DACLNull = dacl == nil
		}
		var f []Failure
		s.SDDL, f = boundedSDDL(sd, stage)
		return f
	})
	return s, failures
}

func boundedSDDL(sd *windows.SECURITY_DESCRIPTOR, stage string) (value string, failures []Failure) {
	var output *uint16
	var units uint32
	r, _, err := convertSDDL.Call(uintptr(unsafe.Pointer(sd)), 1, uintptr(windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION), uintptr(unsafe.Pointer(&output)), uintptr(unsafe.Pointer(&units)))
	if output != nil {
		defer func() {
			_, err := windows.LocalFree(windows.Handle(unsafe.Pointer(output)))
			failures = append(failures, failure(stage+"_sddl_free", err)...)
		}()
	}
	if r == 0 {
		return "", failure(stage+"_sddl", err)
	}
	if output == nil || units == 0 || units > maxSDDLBytes/2 {
		return "", invalid(stage + "_sddl_bound")
	}
	wide := unsafe.Slice(output, int(units))
	if wide[len(wide)-1] != 0 {
		return "", invalid(stage + "_sddl_termination")
	}
	value = windows.UTF16ToString(wide)
	if len(value) > maxSDDLBytes {
		return "", invalid(stage + "_sddl_bound")
	}
	return value, nil
}

func contained(memory []byte, ptr unsafe.Pointer, size uintptr) bool {
	if len(memory) == 0 || ptr == nil {
		return false
	}
	base, p := uintptr(unsafe.Pointer(&memory[0])), uintptr(ptr)
	return p >= base && p-base <= uintptr(len(memory)) && size <= uintptr(len(memory))-(p-base)
}

// Render numeric SIDs without an account lookup or an allocation needing
// LocalFree. The header and every subauthority must lie in the returned buffer.
func sidInBuffer(memory []byte, sid *windows.SID) (string, bool) {
	if !contained(memory, unsafe.Pointer(sid), 8) {
		return "", false
	}
	header := unsafe.Slice((*byte)(unsafe.Pointer(sid)), 8)
	count := int(header[1])
	if header[0] != 1 || count > 15 || !contained(memory, unsafe.Pointer(sid), uintptr(8+4*count)) {
		return "", false
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(sid)), 8+4*count)
	var authority uint64
	for _, b := range data[2:8] {
		authority = authority<<8 | uint64(b)
	}
	result := "S-1-" + strconv.FormatUint(authority, 10)
	for offset := 8; offset < len(data); offset += 4 {
		result += "-" + strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[offset:])), 10)
	}
	return result, sid.IsValid()
}

func resolveFixedServiceSID() (string, []Failure) {
	name, err := windows.UTF16PtrFromString(`NT SERVICE\` + serviceName)
	if err != nil {
		return "", invalid("fixed_service_name")
	}
	var memory [68]byte
	var domain [256]uint16
	sidSize, domainSize, use := uint32(len(memory)), uint32(len(domain)), uint32(0)
	sid := (*windows.SID)(unsafe.Pointer(&memory[0]))
	if err := windows.LookupAccountName(nil, name, sid, &sidSize, &domain[0], &domainSize, &use); err != nil {
		return "", failure("resolve_fixed_service_sid", err)
	}
	if sidSize > uint32(len(memory)) || domainSize > uint32(len(domain)) {
		return "", invalid("fixed_service_sid_bound")
	}
	value, ok := sidInBuffer(memory[:sidSize], sid)
	if !ok || !strings.HasPrefix(value, "S-1-5-80-") {
		return "", invalid("fixed_service_sid_invalid")
	}
	return value, nil
}
