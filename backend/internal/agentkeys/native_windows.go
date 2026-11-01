//go:build windows

package agentkeys

import (
	"io"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file is compiled for windows only and is never executed by the
// repository tests; they use the fake kernel and the shared windowsState and
// windowsGrants mappings. It only queries tokens, opens read-only, queries and
// closes. It never impersonates, enables privileges or changes any ACL.

const nativeStyle = drivePaths

func nativeKernel() (kernel, error) { return &windowsKernel{}, nil }

type windowsKernel struct {
	thread uint32
	fixed  bool
}

func (k *windowsKernel) enter() (account, error) {
	k.thread = windows.GetCurrentThreadId()
	return windowsAccount()
}

func (k *windowsKernel) exit() (account, error) {
	if windows.GetCurrentThreadId() != k.thread {
		return account{}, ErrDenied
	}
	return windowsAccount()
}

// windowsAccount requires that the calling thread has no impersonation token,
// and that the process primary token is a non-elevated local or domain
// account whose BUILTIN\Administrators membership, if any, is deny-only.
func windowsAccount() (account, error) {
	var value, returned uint32
	// Only ERROR_NO_TOKEN proves that this thread is not impersonating.
	if err := windows.GetTokenInformation(windows.GetCurrentThreadToken(), windows.TokenType, (*byte)(unsafe.Pointer(&value)), 4, &returned); err != windows.ERROR_NO_TOKEN {
		return account{}, ErrDenied
	}
	token := windows.GetCurrentProcessToken()
	var elevated uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &returned); err != nil || returned != 4 || elevated != 0 {
		return account{}, ErrDenied
	}
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return account{}, ErrDenied
	}
	sid := user.User.Sid.String()
	if !accountSID(sid) {
		return account{}, ErrDenied
	}
	groups, err := token.GetTokenGroups()
	if err != nil || groups == nil || groups.GroupCount > 1024 {
		return account{}, ErrDenied
	}
	for _, group := range groups.AllGroups() {
		if group.Sid == nil || !group.Sid.IsValid() {
			return account{}, ErrDenied
		}
		if group.Sid.String() == sidAdministrators && group.Attributes&0x10 == 0 { // not SE_GROUP_USE_FOR_DENY_ONLY
			return account{}, ErrDenied
		}
	}
	runtime.KeepAlive(user)
	runtime.KeepAlive(groups)
	return account{user: sid, control: []string{sidSystem, sidAdministrators}}, nil
}

func (k *windowsKernel) open(parent handle, n node) (handle, error) {
	if windows.GetCurrentThreadId() != k.thread {
		return nil, ErrDenied
	}
	if parent == nil {
		if n.name != "" || len(n.path) != 3 {
			return nil, ErrDenied
		}
		root, err := windows.UTF16PtrFromString(n.path)
		if err != nil {
			return nil, ErrDenied
		}
		k.fixed = windows.GetDriveType(root) == windows.DRIVE_FIXED
	} else if p, ok := parent.(*windowsFile); !ok || p.closed || n.name == "" {
		return nil, ErrDenied
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if n.role == roleLeaf {
		access |= windows.FILE_READ_DATA // with share-read only: no concurrent writer or deleter
	}
	h, err := openNoFollow(n.path, access, windows.FILE_SHARE_READ)
	if err != nil {
		return nil, ErrDenied
	}
	return &windowsFile{handle: h, path: n.path, fixed: k.fixed, thread: k.thread, root: parent == nil}, nil
}

func openNoFollow(path string, access, share uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, ErrDenied
	}
	h, err := windows.CreateFile(name, access, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil || h == windows.InvalidHandle || h == 0 {
		return 0, ErrDenied
	}
	return h, nil
}

type windowsFile struct {
	handle windows.Handle
	path   string
	fixed  bool
	thread uint32
	root   bool // opened as the drive root (no parent handle)
	closed bool
}

// volumeNameGUID is VOLUME_NAME_GUID for GetFinalPathNameByHandle; 0 is
// VOLUME_NAME_DOS with FILE_NAME_NORMALIZED.
const volumeNameGUID = 0x1

type fileIDInfo struct {
	Volume uint64
	ID     [16]byte
}

func (f *windowsFile) usable() bool {
	return !f.closed && windows.GetCurrentThreadId() == f.thread
}

func (f *windowsFile) inspect() (state, error) {
	if !f.usable() {
		return state{}, ErrDenied
	}
	raw := winStat{fixed: f.fixed}
	kind, err := windows.GetFileType(f.handle)
	if err != nil {
		return state{}, ErrDenied
	}
	raw.disk = kind == windows.FILE_TYPE_DISK
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(f.handle, &info) != nil {
		return state{}, ErrDenied
	}
	raw.attrs, raw.links = info.FileAttributes, info.NumberOfLinks
	raw.size = uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	raw.stamp = [4]int64{int64(info.CreationTime.HighDateTime), int64(info.CreationTime.LowDateTime), int64(info.LastWriteTime.HighDateTime), int64(info.LastWriteTime.LowDateTime)}
	var identity fileIDInfo
	if windows.GetFileInformationByHandleEx(f.handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity))) != nil {
		return state{}, ErrDenied
	}
	raw.volume, raw.file = identity.Volume, identity.ID
	var ok bool
	if raw.finalPath, ok = finalPathName(f.handle, 0); !ok {
		return state{}, ErrDenied
	}
	var filesystem [64]uint16
	if windows.GetVolumeInformationByHandle(f.handle, nil, 0, nil, nil, nil, &filesystem[0], uint32(len(filesystem))) != nil {
		return state{}, ErrDenied
	}
	raw.filesystem = windows.UTF16ToString(filesystem[:])
	if err := windowsSecurity(f.handle, &raw); err != nil {
		return state{}, err
	}
	// Only a drive root owned by exactly TrustedInstaller needs OS-root
	// evidence, gathered again on every inspection of this same held handle.
	// Missing evidence leaves the owner untrusted (osVolumeRoot fails closed).
	if f.root && raw.owner == sidTrustedInstaller {
		raw.volumePath, _ = finalPathName(f.handle, volumeNameGUID)
		system, err := systemRoot()
		if err != nil {
			return state{}, err
		}
		raw.system = system
	}
	return windowsState(raw), nil
}

func finalPathName(h windows.Handle, flags uint32) (string, bool) {
	path := make([]uint16, 2048)
	n, err := windows.GetFinalPathNameByHandle(h, &path[0], uint32(len(path)), flags)
	if err != nil || n == 0 || n >= uint32(len(path)) {
		return "", false
	}
	return windows.UTF16ToString(path[:n]), true
}

// systemRoot asks the OS for the shared Windows directory
// (GetSystemWindowsDirectory, not %SystemRoot% or any caller value) and reads
// the directory's actual volume through a separate no-follow, attribute-only
// handle. Any failure returns nil evidence; a failed close is ErrCleanup.
func systemRoot() (*winSystemRoot, error) {
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil || dir == "" {
		return nil, nil
	}
	h, err := openNoFollow(dir, uint32(windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE), windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return nil, nil
	}
	system := &winSystemRoot{api: dir}
	var info windows.ByHandleFileInformation
	var identity fileIDInfo
	ok := windows.GetFileInformationByHandle(h, &info) == nil &&
		windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity))) == nil
	if ok {
		system.attrs, system.volume = info.FileAttributes, identity.Volume
		system.dosPath, ok = finalPathName(h, 0)
	}
	if ok {
		system.volumePath, ok = finalPathName(h, volumeNameGUID)
	}
	if windows.CloseHandle(h) != nil {
		return nil, ErrCleanup
	}
	if !ok {
		return nil, nil
	}
	return system, nil
}

// windowsSecurity copies the owner and the DACL entries of the open handle.
// Entry types other than ACCESS_ALLOWED/ACCESS_DENIED are refused here
// because their SID is not at the same offset.
func windowsSecurity(h windows.Handle, raw *winStat) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return ErrDenied
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return ErrDenied
	}
	raw.owner = owner.String()
	acl, _, err := sd.DACL()
	if err != nil {
		return ErrDenied
	}
	if acl == nil { // NULL DACL
		return nil
	}
	if acl.AceCount > maxACEs {
		return ErrDenied
	}
	raw.daclPresent = true
	raw.aces = make([]winACE, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var native *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &native) != nil || native == nil || native.Header.AceType > 1 || native.Header.AceSize < 16 {
			return ErrDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&native.SidStart))
		if !sid.IsValid() || sid.Len() != int(native.Header.AceSize)-8 {
			return ErrDenied
		}
		raw.aces = append(raw.aces, winACE{typ: native.Header.AceType, flags: native.Header.AceFlags, mask: uint32(native.Mask), sid: sid.String()})
	}
	return nil
}

// resolve opens the child name without following a reparse point and with
// attribute-only access, reads its file ID and closes it. Windows binds names
// by path, so this checks the current namespace; the held handles' final paths
// and IDs are checked separately.
func (f *windowsFile) resolve(name string) (objectID, error) {
	if !f.usable() || name == "" {
		return objectID{}, ErrDenied
	}
	child := f.path
	if !strings.HasSuffix(child, `\`) {
		child += `\`
	}
	h, err := openNoFollow(child+name, uint32(windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE), windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE)
	if err != nil {
		return objectID{}, ErrDenied
	}
	var identity fileIDInfo
	err = windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity)))
	if windows.CloseHandle(h) != nil {
		return objectID{}, ErrCleanup
	}
	if err != nil {
		return objectID{}, ErrDenied
	}
	return objectID{volume: identity.Volume, file: identity.ID}, nil
}

func (f *windowsFile) Read(dst []byte) (int, error) {
	if !f.usable() {
		return 0, ErrDenied
	}
	var n uint32
	err := windows.ReadFile(f.handle, dst, &n, nil)
	if err == windows.ERROR_HANDLE_EOF || err == nil && n == 0 {
		return 0, io.EOF
	}
	if err != nil || int(n) > len(dst) {
		return 0, ErrDenied
	}
	return int(n), nil
}

// close is attempted once and never retried on another thread.
func (f *windowsFile) close() error {
	if f.closed {
		return nil
	}
	if windows.GetCurrentThreadId() != f.thread {
		return ErrCleanup
	}
	f.closed = true
	if windows.CloseHandle(f.handle) != nil {
		return ErrCleanup
	}
	return nil
}
