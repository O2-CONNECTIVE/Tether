//go:build windows

package serviceready

import (
	"context"
	"errors"
	"runtime"
	"unicode/utf16"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"golang.org/x/sys/windows"
)

// query-only APIのsourceであり、OSの非returnを解決した実証ではない。
type nativeKernel struct{ auth *bootstrapauth.Factory }

func platformKernel(auth *bootstrapauth.Factory) kernel {
	if auth == nil {
		return nil
	}
	return nativeKernel{auth: auth}
}
func (k nativeKernel) prepare(ctx context.Context, token uintptr) (preparedScope, error) {
	p, err := k.auth.PrepareService(ctx, token)
	if p == nil {
		return nil, err
	}
	return p, err
}
func (nativeKernel) threadID() uint32   { return windows.GetCurrentThreadId() }
func (nativeKernel) currentPID() uint32 { return windows.GetCurrentProcessId() }

type nativeService struct {
	manager, handle windows.Handle
	thread          uint32
}

func (nativeKernel) openService(name string) (serviceRef, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, ErrDenied
	}
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil || manager == 0 {
		return nil, ErrDenied
	}
	out := &nativeService{manager: manager, thread: windows.GetCurrentThreadId()}
	out.handle, err = windows.OpenService(manager, path, windows.READ_CONTROL|windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	if err != nil || out.handle == 0 {
		return out, ErrDenied
	}
	return out, nil
}
func (s *nativeService) inspect() (serviceState, error) {
	if windows.GetCurrentThreadId() != s.thread {
		return serviceState{}, ErrFatal
	}
	if s.handle == 0 {
		return serviceState{}, ErrDenied
	}
	// documented maximum8KiB、pointer境界を検証し、任意のNUL終端メモリを走査しない。
	var storage [1024]uint64
	var pins runtime.Pinner
	pins.Pin(&storage[0])
	defer pins.Unpin()
	config := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&storage[0]))
	var needed uint32
	if windows.QueryServiceConfig(s.handle, config, uint32(unsafe.Sizeof(storage)), &needed) != nil {
		return serviceState{}, ErrDenied
	}
	command, err := boundedString(config.BinaryPathName, unsafe.Pointer(&storage[0]), unsafe.Sizeof(storage), unsafe.Sizeof(*config))
	if err != nil {
		return serviceState{}, err
	}
	var status windows.SERVICE_STATUS_PROCESS
	if windows.QueryServiceStatusEx(s.handle, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed) != nil || status.ServiceType != config.ServiceType {
		return serviceState{}, ErrDenied
	}
	security, err := nativeSecurity(s.handle, windows.SE_SERVICE)
	runtime.KeepAlive(&storage)
	if err != nil {
		return serviceState{}, err
	}
	return serviceState{state: status.CurrentState, pid: status.ProcessId, kind: config.ServiceType, commandLine: command, security: security}, nil
}
func boundedString(ptr *uint16, base unsafe.Pointer, size, min uintptr) (string, error) {
	start := uintptr(unsafe.Pointer(ptr))
	origin := uintptr(base)
	if ptr == nil || start < origin+min || start >= origin+size || (start-origin)%2 != 0 {
		return "", ErrDenied
	}
	units := unsafe.Slice(ptr, int((origin+size-start)/2))
	end := -1
	for i, v := range units {
		if v == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return "", ErrDenied
	}
	units = units[:end]
	for i := 0; i < len(units); i++ {
		v := units[i]
		if v >= 0xd800 && v <= 0xdbff {
			if i+1 == len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", ErrDenied
			}
			i++
		} else if v >= 0xdc00 && v <= 0xdfff {
			return "", ErrDenied
		}
	}
	return string(utf16.Decode(units)), nil
}
func (s *nativeService) close() error {
	if s.handle != 0 {
		if windows.CloseServiceHandle(s.handle) == nil {
			s.handle = 0
		}
	}
	if s.manager != 0 {
		if windows.CloseServiceHandle(s.manager) == nil {
			s.manager = 0
		}
	}
	if s.handle != 0 || s.manager != 0 {
		return ErrUnconfirmed
	}
	return nil
}

type nativeProcess struct {
	handle windows.Handle
	token  windows.Token
	thread uint32
}

func (nativeKernel) openProcess(pid uint32) (processRef, error) {
	handle, err := windows.OpenProcess(windows.READ_CONTROL|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil || handle == 0 {
		return nil, ErrDenied
	}
	return &nativeProcess{handle: handle, thread: windows.GetCurrentThreadId()}, nil
}
func (p *nativeProcess) inspect() (processState, error) {
	if windows.GetCurrentThreadId() != p.thread {
		return processState{}, ErrFatal
	}
	if p.handle == 0 {
		return processState{}, ErrDenied
	}
	alive, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return processState{}, ErrDenied
	}
	pid, err := windows.GetProcessId(p.handle)
	if err != nil || pid == 0 {
		return processState{}, ErrDenied
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(p.handle, &creation, &exit, &kernel, &user) != nil {
		return processState{}, ErrDenied
	}
	security, err := nativeSecurity(p.handle, windows.SE_KERNEL_OBJECT)
	if err != nil {
		return processState{}, err
	}
	alive, err = windows.WaitForSingleObject(p.handle, 0)
	if err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return processState{}, ErrDenied
	}
	return processState{pid: pid, creation: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), alive: true, security: security}, nil
}
func (p *nativeProcess) close() error {
	var result error
	if p.token != 0 {
		if p.token.Close() != nil {
			result = ErrUnconfirmed
		} else {
			p.token = 0
		}
	}
	if p.handle != 0 {
		if windows.CloseHandle(p.handle) != nil {
			return errors.Join(result, ErrUnconfirmed)
		}
		p.handle = 0
	}
	return result
}
func nativeSecurity(handle windows.Handle, kind windows.SE_OBJECT_TYPE) (SecurityPolicy, error) {
	sd, err := windows.GetSecurityInfo(handle, kind, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return SecurityPolicy{}, ErrDenied
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return SecurityPolicy{}, ErrDenied
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return SecurityPolicy{}, ErrDenied
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount > 128 {
		return SecurityPolicy{}, ErrDenied
	}
	out := SecurityPolicy{OwnerSID: owner.String(), Protected: control&windows.SE_DACL_PROTECTED != 0, ACL: make([]protectedkey.ACE, 0, acl.AceCount)}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil || ace.Header.AceType > 1 || ace.Header.AceSize < 16 {
			return SecurityPolicy{}, ErrDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sid.Len() != int(ace.Header.AceSize)-8 {
			return SecurityPolicy{}, ErrDenied
		}
		out.ACL = append(out.ACL, protectedkey.ACE{Type: ace.Header.AceType, Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask), SID: sid.String()})
	}
	return out, nil
}

func (k nativeKernel) checkCaller(ctx context.Context) error { return k.auth.CheckServiceCaller(ctx) }
