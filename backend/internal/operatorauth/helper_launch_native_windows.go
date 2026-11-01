//go:build windows

package operatorauth

import (
	"context"
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	helperLaunchQueryProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryInformationProcess")
	helperLaunchQueryFile    = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryInformationFile")
)

// helperLaunchNativeWitness retains every reference it obtains in this child
// and never reopens a PID or inherited handle number after acquire. Source only:
// SCM/process/token/pipe access from the helper identity is unverified. The
// supervisor process query is used only with its held token's owner/DACL; if
// provisioning grants neither, acquisition is denied rather than substituted.
type helperLaunchNativeWitness struct {
	acquired                                bool
	fixed                                   helperLaunchFixed
	originals                               [3]uintptr
	manager, parentService, supplierService windows.Handle
	parent, supplier                        windows.Handle
	parentToken, selfToken, supplierToken   windows.Token
	pipes                                   [3]windows.Handle
}

func nativeHelperLaunchWitness() helperLaunchWitness { return &helperLaunchNativeWitness{} }

func (w *helperLaunchNativeWitness) acquire(ctx context.Context, fixed helperLaunchFixed, loc helperLaunchLocators) error {
	if w.acquired || !fixed.valid() || !reportContextValid(ctx) {
		return ErrDenied
	}
	w.acquired, w.fixed = true, fixed
	w.originals = [3]uintptr{loc.operatorRead, loc.reportWrite, loc.profileRead}
	var err error
	if w.manager, err = windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT); err != nil || w.manager == 0 {
		return ErrDenied
	}
	for _, s := range []struct {
		name   string
		handle *windows.Handle
	}{{fixed.parentService, &w.parentService}, {fixed.supplierService, &w.supplierService}} {
		name, e := windows.UTF16PtrFromString(s.name)
		if e != nil {
			return ErrDenied
		}
		if *s.handle, e = windows.OpenService(w.manager, name, windows.SERVICE_QUERY_STATUS); e != nil || *s.handle == 0 {
			return ErrDenied
		}
	}
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &w.selfToken) != nil || w.selfToken == 0 || !reportContextValid(ctx) {
		return ErrDenied
	}
	parent, err := helperLaunchInheritedFrom()
	if err != nil {
		return err
	}
	if w.parent, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, parent); err != nil || w.parent == 0 {
		return ErrDenied
	}
	if windows.OpenProcessToken(w.parent, windows.TOKEN_QUERY|windows.READ_CONTROL, &w.parentToken) != nil || w.parentToken == 0 {
		return ErrDenied
	}
	supplier, err := helperLaunchServiceState(w.supplierService)
	if err != nil {
		return err
	}
	if w.supplier, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, supplier.pid); err != nil || w.supplier == 0 {
		return ErrDenied
	}
	if windows.OpenProcessToken(w.supplier, windows.TOKEN_QUERY|windows.READ_CONTROL, &w.supplierToken) != nil || w.supplierToken == 0 {
		return ErrDenied
	}
	// The originals are still inheritable here: prepare precedes adoption. Each
	// duplicate keeps the exact rights and is never inheritable.
	for i, original := range w.originals {
		kind, e := windows.GetFileType(windows.Handle(original))
		if e != nil || kind != windows.FILE_TYPE_PIPE || launchHandleAccess(original, true, helperLaunchPipeAccess[i]) != nil || !reportContextValid(ctx) {
			return ErrDenied
		}
		e = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(original), windows.CurrentProcess(), &w.pipes[i], helperLaunchPipeAccess[i], false, 0)
		if e != nil || w.pipes[i] == 0 {
			return ErrDenied
		}
	}
	return nil
}

func (w *helperLaunchNativeWitness) inspect(ctx context.Context) (helperLaunchObservation, error) {
	var o helperLaunchObservation
	if !w.acquired || w.manager == 0 || w.parentService == 0 || w.supplierService == 0 || w.parent == 0 || w.supplier == 0 ||
		w.parentToken == 0 || w.selfToken == 0 || w.supplierToken == 0 || w.pipes[0] == 0 || w.pipes[1] == 0 || w.pipes[2] == 0 || !reportContextValid(ctx) {
		return o, ErrDenied
	}
	var err error
	if o.parentService, err = helperLaunchServiceState(w.parentService); err != nil {
		return helperLaunchObservation{}, err
	}
	if o.supplierService, err = helperLaunchServiceState(w.supplierService); err != nil {
		return helperLaunchObservation{}, err
	}
	if o.inheritedFrom, err = helperLaunchInheritedFrom(); err != nil {
		return helperLaunchObservation{}, err
	}
	for _, p := range []struct {
		handle windows.Handle
		out    *helperLaunchProcess
	}{{w.parent, &o.parent}, {w.supplier, &o.supplier}, {windows.CurrentProcess(), &o.self}} {
		if *p.out, err = helperLaunchProcessState(p.handle); err != nil {
			return helperLaunchObservation{}, err
		}
	}
	if o.parentToken, err = helperLaunchTokenState(w.parentToken, w.fixed.parentServiceSID, true); err != nil {
		return helperLaunchObservation{}, err
	}
	if o.supplierToken, err = helperLaunchTokenState(w.supplierToken, w.fixed.helperServiceSID, true); err != nil {
		return helperLaunchObservation{}, err
	}
	if o.selfToken, err = helperLaunchTokenState(w.selfToken, w.fixed.helperServiceSID, false); err != nil {
		return helperLaunchObservation{}, err
	}
	for i := range w.pipes {
		if o.pipes[i], err = helperLaunchPipeState(w.originals[i], w.pipes[i], i); err != nil || !reportContextValid(ctx) {
			return helperLaunchObservation{}, ErrDenied
		}
	}
	// SCM is read again after the held processes, so a stop or PID reuse
	// between the observations rejects instead of binding a new process.
	for _, s := range []struct {
		handle windows.Handle
		prior  helperLaunchService
	}{{w.parentService, o.parentService}, {w.supplierService, o.supplierService}} {
		state, e := helperLaunchServiceState(s.handle)
		if e != nil || state != s.prior {
			return helperLaunchObservation{}, ErrDenied
		}
	}
	if !reportContextValid(ctx) {
		return helperLaunchObservation{}, ErrDenied
	}
	return o, nil
}

// close releases only references this witness owns and keeps any failed one
// for a bounded retry by the same lease.
func (w *helperLaunchNativeWitness) close(ctx context.Context) error {
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	var errs []error
	for _, h := range []*windows.Handle{&w.pipes[0], &w.pipes[1], &w.pipes[2], &w.parent, &w.supplier} {
		if *h != 0 {
			if err := windows.CloseHandle(*h); err != nil {
				errs = append(errs, err)
			} else {
				*h = 0
			}
		}
	}
	for _, t := range []*windows.Token{&w.parentToken, &w.supplierToken, &w.selfToken} {
		if *t != 0 {
			if err := t.Close(); err != nil {
				errs = append(errs, err)
			} else {
				*t = 0
			}
		}
	}
	for _, h := range []*windows.Handle{&w.parentService, &w.supplierService, &w.manager} {
		if *h != 0 {
			if err := windows.CloseServiceHandle(*h); err != nil {
				errs = append(errs, err)
			} else {
				*h = 0
			}
		}
	}
	if len(errs) != 0 {
		return errors.Join(ErrUnconfirmed, errors.Join(errs...))
	}
	return nil
}

func helperLaunchServiceState(h windows.Handle) (helperLaunchService, error) {
	var value windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if h == 0 || windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &needed) != nil || value.ProcessId == 0 {
		return helperLaunchService{}, ErrDenied
	}
	return helperLaunchService{pid: value.ProcessId, state: value.CurrentState}, nil
}

// PROCESS_BASIC_INFORMATION is six pointer-sized words on every Windows target:
// UniqueProcessId is word 4 and InheritedFromUniqueProcessId word 5.
func helperLaunchInheritedFrom() (uint32, error) {
	if helperLaunchQueryProcess.Find() != nil {
		return 0, ErrDenied
	}
	var basic [6]uintptr
	var size uint32
	status, _, _ := helperLaunchQueryProcess.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&basic[0])), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&size)))
	if status != 0 || size != uint32(unsafe.Sizeof(basic)) || basic[4] != uintptr(windows.GetCurrentProcessId()) || basic[5] == 0 || uint64(basic[5]) > uint64(^uint32(0)) {
		return 0, ErrDenied
	}
	return uint32(basic[5]), nil
}

func helperLaunchProcessState(h windows.Handle) (helperLaunchProcess, error) {
	alive, err := windows.WaitForSingleObject(h, 0)
	if err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return helperLaunchProcess{}, ErrDenied
	}
	pid, err := windows.GetProcessId(h)
	if err != nil || pid == 0 {
		return helperLaunchProcess{}, ErrDenied
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return helperLaunchProcess{}, ErrDenied
	}
	var path [32768]uint16
	size := uint32(len(path))
	if windows.QueryFullProcessImageName(h, 0, &path[0], &size) != nil || size == 0 || size >= uint32(len(path)) {
		return helperLaunchProcess{}, ErrDenied
	}
	if alive, err = windows.WaitForSingleObject(h, 0); err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return helperLaunchProcess{}, ErrDenied
	}
	return helperLaunchProcess{pid: pid, creation: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), imagePath: windows.UTF16ToString(path[:size]), alive: true}, nil
}

// The statistics are read before and after the group/security reads so one
// observation cannot mix two token states.
func helperLaunchTokenState(t windows.Token, serviceSID string, security bool) (helperLaunchToken, error) {
	before, err := launchPrimaryProjection(t)
	if err != nil {
		return helperLaunchToken{}, ErrDenied
	}
	out := helperLaunchToken{tokenID: before.tokenID, modifiedID: before.modifiedID, primary: before.tokenProjection}
	if out.serviceGroup, err = launchServiceGroup(t, serviceSID); err != nil {
		return helperLaunchToken{}, ErrDenied
	}
	if security {
		if out.owner, out.acl, err = helperLaunchTokenSecurity(t); err != nil {
			return helperLaunchToken{}, err
		}
	}
	after, err := launchPrimaryProjection(t)
	if err != nil || after != before {
		return helperLaunchToken{}, ErrDenied
	}
	return out, nil
}

func helperLaunchTokenSecurity(t windows.Token) (string, []helperLaunchACE, error) {
	sd, err := windows.GetSecurityInfo(windows.Handle(t), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return "", nil, ErrDenied
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return "", nil, ErrDenied
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return "", nil, ErrDenied
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount > 32 {
		return "", nil, ErrDenied
	}
	out := make([]helperLaunchACE, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil || ace.Header.AceType > 1 || ace.Header.AceSize < 16 {
			return "", nil, ErrDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sid.Len() != int(ace.Header.AceSize)-8 {
			return "", nil, ErrDenied
		}
		out = append(out, helperLaunchACE{kind: ace.Header.AceType, flags: ace.Header.AceFlags, mask: uint32(ace.Mask), sid: sid.String()})
	}
	return owner.String(), out, nil
}

// FileModeInformation(16) without synchronous flags is OVERLAPPED; only the
// nonalert synchronous mode is accepted for the anonymous profile reader.
// GetNamedPipeInfo is not called on the write-only report client because its
// rights would have to broaden; the server PID query there is unverified.
func helperLaunchPipeState(original uintptr, h windows.Handle, slot int) (helperLaunchPipe, error) {
	out := helperLaunchPipe{original: original, retained: uintptr(h)}
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_PIPE || launchGetHandleInfo.Find() != nil || launchQueryObject.Find() != nil || helperLaunchQueryFile.Find() != nil {
		return helperLaunchPipe{}, ErrDenied
	}
	if ok, _, _ := launchGetHandleInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&out.flags))); ok == 0 {
		return helperLaunchPipe{}, ErrDenied
	}
	var basic [14]uint32
	var size uint32
	status, _, _ := launchQueryObject.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&basic[0])), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&size)))
	if status != 0 || size != uint32(unsafe.Sizeof(basic)) {
		return helperLaunchPipe{}, ErrDenied
	}
	out.access = basic[1]
	var block [2]uintptr
	var mode uint32
	status, _, _ = helperLaunchQueryFile.Call(uintptr(h), uintptr(unsafe.Pointer(&block[0])), uintptr(unsafe.Pointer(&mode)), unsafe.Sizeof(mode), 16)
	if status != 0 {
		return helperLaunchPipe{}, ErrDenied
	}
	switch mode & 0x30 {
	case 0:
		out.mode = 1
	case 0x20:
		out.mode = 2
	}
	if slot != 1 && windows.GetNamedPipeInfo(h, &out.info, nil, nil, nil) != nil {
		return helperLaunchPipe{}, ErrDenied
	}
	if windows.GetNamedPipeServerProcessId(h, &out.server) != nil || out.server == 0 {
		return helperLaunchPipe{}, ErrDenied
	}
	return out, nil
}
