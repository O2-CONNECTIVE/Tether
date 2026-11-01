//go:build linux && !android

package agentkeys

import (
	"encoding/binary"
	"io"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// This file is the Linux native kernel: golang.org/x/sys/unix and the standard
// library only, no cgo. It only opens read-only, queries and closes; it never
// creates, chmods, chowns, sets ACLs or credentials (including setfsuid(2) as a
// query) and never follows a link. Repository tests drive it through a fake
// linuxSys table and never reach the real system calls. Unverified platform
// facts are listed in docs/agent-key-loader-linux.md.

const nativeStyle = posixPaths

func nativeKernel() (kernel, error) { return &linuxKernel{sys: nativeSys}, nil }

// Filesystem magic numbers (linux/magic.h). A key path may only cross
// filesystems whose ownership, mode bits and POSIX ACLs the local kernel
// enforces for this mount namespace: ext4 (also mounts ext2/ext3), tmpfs and
// overlayfs. Everything else, including NFS, CIFS/SMB, 9p (WSL2 DrvFS), FUSE
// (virtiofs, fuse-overlayfs), autofs, ramfs, btrfs, xfs and unknown types, is
// refused. For overlayfs only the merged view is checked; the backing layers
// belong to the container engine and host, which are trusted operators.
const (
	ext4Magic    = 0xef53     // EXT4_SUPER_MAGIC
	tmpfsMagic   = 0x01021994 // TMPFS_MAGIC
	overlayMagic = 0x794c7630 // OVERLAYFS_SUPER_MAGIC
	procMagic    = 0x9fa0     // PROC_SUPER_MAGIC: identity evidence only, never a key path

	procRootIno  = 1 // PROC_ROOT_INO
	maxProcBytes = 16 * 1024

	aclAccess  = "system.posix_acl_access"
	aclDefault = "system.posix_acl_default"
	aclVersion = 2 // POSIX_ACL_XATTR_VERSION
)

func linuxLocal(fsType int64) bool {
	switch fsType {
	case ext4Magic, tmpfsMagic, overlayMagic:
		return true
	}
	return false
}

// linuxSys is the fixed set of system calls this kernel uses. nativeSys binds
// it to x/sys/unix; tests substitute an in-memory table.
type linuxSys struct {
	openat     func(dirfd int, name string, flags int) (int, error)
	fstat      func(fd int, st *unix.Stat_t) error
	fstatat    func(dirfd int, name string, st *unix.Stat_t, flags int) error
	fstatfs    func(fd int, st *unix.Statfs_t) error
	fgetxattr  func(fd int, name string, dest []byte) (int, error)
	readlinkat func(dirfd int, name string, buf []byte) (int, error)
	read       func(fd int, b []byte) (int, error)
	close      func(fd int) error
	gettid     func() int
	getpid     func() int
	getresuid  func() (int, int, int)
	getresgid  func() (int, int, int)
	capget     func(hdr *unix.CapUserHeader, data *unix.CapUserData) error
}

var nativeSys = linuxSys{
	openat:     func(dirfd int, name string, flags int) (int, error) { return unix.Openat(dirfd, name, flags, 0) },
	fstat:      unix.Fstat,
	fstatat:    unix.Fstatat,
	fstatfs:    unix.Fstatfs,
	fgetxattr:  unix.Fgetxattr,
	readlinkat: unix.Readlinkat,
	read:       unix.Read,
	close:      unix.Close,
	gettid:     unix.Gettid,
	getpid:     unix.Getpid,
	getresuid:  unix.Getresuid,
	getresgid:  unix.Getresgid,
	capget:     unix.Capget,
}

// linuxThread is read by system calls that only query the calling thread:
// gettid, getpid, getresuid(2), getresgid(2) and capget(2) with pid 0. The
// file-system IDs have no query-only system call, so they come from
// linuxStatus.
type linuxThread struct {
	tid, pid       int
	uid, gid       [3]int // real, effective, saved
	capEff, capPrm uint64
}

// linuxStatus is the part of /proc/<tgid>/task/<tid>/status this loader
// requires. Each key must appear exactly once.
type linuxStatus struct {
	tgid, pid              int
	uid, gid               [4]int // real, effective, saved, filesystem
	capPrm, capEff, capAmb uint64
}

// linuxEvidence is the complete identity evidence; enter and exit must read
// identical values.
type linuxEvidence struct {
	thread   linuxThread
	status   linuxStatus
	overflow int // /proc/sys/kernel/overflowuid: the uid shown for unmapped owners
}

// linuxPrincipal accepts only a non-root user whose real, effective, saved and
// file-system UIDs agree (likewise GIDs) in both the system calls and the
// thread's procfs status, with no effective, permitted or ambient capability.
// A process in that state cannot change its own credentials, and a
// multi-threaded process cannot enter another user namespace. The uid must
// differ from overflowuid, which must not be root: otherwise owners unmapped
// in this user namespace would look like the current user or root.
func linuxPrincipal(ev linuxEvidence) (account, bool) {
	t, s := ev.thread, ev.status
	uid, gid := t.uid[0], t.gid[0]
	if uid <= 0 || int64(uid) >= 1<<32-1 || gid < 0 || int64(gid) >= 1<<32-1 || ev.overflow <= 0 || uid == ev.overflow || t.pid <= 0 || t.tid <= 0 {
		return account{}, false
	}
	if t.uid != [3]int{uid, uid, uid} || t.gid != [3]int{gid, gid, gid} ||
		s.uid != [4]int{uid, uid, uid, uid} || s.gid != [4]int{gid, gid, gid, gid} {
		return account{}, false
	}
	if s.tgid != t.pid || s.pid != t.tid || t.capEff|t.capPrm|s.capEff|s.capPrm|s.capAmb != 0 {
		return account{}, false
	}
	return account{user: uidPrincipal(uint32(uid)), control: []string{uidPrincipal(0)}}, true
}

func linuxThreadIDs(sys linuxSys) (linuxThread, bool) {
	var t linuxThread
	t.tid, t.pid = sys.gettid(), sys.getpid()
	t.uid[0], t.uid[1], t.uid[2] = sys.getresuid()
	t.gid[0], t.gid[1], t.gid[2] = sys.getresgid()
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3} // Pid 0: the calling thread
	var data [2]unix.CapUserData
	if sys.capget(&hdr, &data[0]) != nil || hdr.Version != unix.LINUX_CAPABILITY_VERSION_3 {
		return linuxThread{}, false
	}
	t.capEff = uint64(data[1].Effective)<<32 | uint64(data[0].Effective)
	t.capPrm = uint64(data[1].Permitted)<<32 | uint64(data[0].Permitted)
	return t, true
}

// linuxAccount reads the thread's IDs, then its procfs evidence, then the IDs
// again; any change in between is refused.
func linuxAccount(sys linuxSys) (account, linuxEvidence, error) {
	var ev linuxEvidence
	var ok bool
	if ev.thread, ok = linuxThreadIDs(sys); !ok {
		return account{}, linuxEvidence{}, ErrDenied
	}
	var err error
	if ev.status, ev.overflow, err = procEvidence(sys, ev.thread.pid, ev.thread.tid); err != nil {
		return account{}, linuxEvidence{}, err
	}
	if again, ok := linuxThreadIDs(sys); !ok || again != ev.thread {
		return account{}, linuxEvidence{}, ErrDenied
	}
	acct, ok := linuxPrincipal(ev)
	if !ok {
		return account{}, linuxEvidence{}, ErrDenied
	}
	return acct, ev, nil
}

// procEvidence reads this thread's status and overflowuid from procfs without
// following any link. /proc is opened (one component below the process root,
// so nothing to substitute but /proc itself) and must be procfs with the root
// inode. The magic thread-self link is read, not followed, and must name
// exactly "<getpid>/task/<gettid>", which proves the mount belongs to this
// pid namespace; each of those components and "status" is then opened with
// O_NOFOLLOW relative to the held parent, and must be procfs on the same
// device. Every descriptor is closed once in reverse order; a failed close is
// ErrCleanup, preserved through the shared identity boundary.
func procEvidence(sys linuxSys, pid, tid int) (status linuxStatus, overflow int, result error) {
	var fds []int
	defer func() {
		for i := len(fds) - 1; i >= 0; i-- {
			if sys.close(fds[i]) != nil {
				result = ErrCleanup
			}
		}
	}()
	hold := func(dirfd int, name string, dir bool) (int, *unix.Stat_t, bool) {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NOCTTY
		want := uint32(unix.S_IFREG)
		if dir {
			flags |= unix.O_DIRECTORY
			want = unix.S_IFDIR
		}
		fd, err := openRetry(sys, dirfd, name, flags)
		if err != nil {
			return -1, nil, false
		}
		fds = append(fds, fd)
		var fs unix.Statfs_t
		var st unix.Stat_t
		if sys.fstatfs(fd, &fs) != nil || int64(fs.Type) != procMagic || sys.fstat(fd, &st) != nil || uint32(st.Mode)&unix.S_IFMT != want {
			return -1, nil, false
		}
		return fd, &st, true
	}
	root, st, ok := hold(unix.AT_FDCWD, "/proc", true)
	if !ok || st.Ino != procRootIno {
		return linuxStatus{}, 0, ErrDenied
	}
	procDev := st.Dev
	self := strconv.Itoa(pid) + "/task/" + strconv.Itoa(tid)
	link := make([]byte, 64)
	n, err := sys.readlinkat(root, "thread-self", link)
	if err != nil || n != len(self) || string(link[:n]) != self {
		return linuxStatus{}, 0, ErrDenied
	}
	dir := root
	for _, name := range [...]string{strconv.Itoa(pid), "task", strconv.Itoa(tid)} {
		fd, st, ok := hold(dir, name, true)
		if !ok || st.Dev != procDev {
			return linuxStatus{}, 0, ErrDenied
		}
		dir = fd
	}
	fd, st, ok := hold(dir, "status", false)
	if !ok || st.Dev != procDev {
		return linuxStatus{}, 0, ErrDenied
	}
	b, ok := readProc(sys, fd)
	if ok {
		status, ok = parseStatus(b)
	}
	if !ok {
		return linuxStatus{}, 0, ErrDenied
	}
	// overflowuid is a global sysctl; /proc/sys is often a separate read-only
	// bind mount, so only the procfs type is required here.
	dir = root
	for _, name := range [...]string{"sys", "kernel"} {
		if dir, _, ok = hold(dir, name, true); !ok {
			return linuxStatus{}, 0, ErrDenied
		}
	}
	if fd, _, ok = hold(dir, "overflowuid", false); !ok {
		return linuxStatus{}, 0, ErrDenied
	}
	if b, ok = readProc(sys, fd); ok {
		var text string
		if text, ok = strings.CutSuffix(string(b), "\n"); ok {
			overflow, ok = linuxDecimal(text)
		}
	}
	if !ok {
		return linuxStatus{}, 0, ErrDenied
	}
	return status, overflow, nil
}

func readProc(sys linuxSys, fd int) ([]byte, bool) {
	buf := make([]byte, maxProcBytes+1)
	n, err := readBounded(fdReader{sys: sys, fd: fd}, buf)
	return buf[:n], err == nil
}

// parseStatus requires Tgid, Pid, Uid, Gid, CapPrm, CapEff and CapAmb exactly
// once each, in the kernel's "Key:\tvalue" form with tab-separated canonical
// decimals and 16 lowercase hex digits. The kernel escapes newlines in Name,
// so a process name cannot start a line.
func parseStatus(b []byte) (linuxStatus, bool) {
	var s linuxStatus
	text, ok := strings.CutSuffix(string(b), "\n")
	if !ok || strings.ContainsRune(text, 0) {
		return linuxStatus{}, false
	}
	seen := 0
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":\t")
		if !ok {
			continue
		}
		fields := strings.Split(value, "\t")
		var bit int
		switch key {
		case "Tgid":
			bit, ok = 1, statusInts(fields, &s.tgid)
		case "Pid":
			bit, ok = 2, statusInts(fields, &s.pid)
		case "Uid":
			bit, ok = 4, statusInts(fields, &s.uid[0], &s.uid[1], &s.uid[2], &s.uid[3])
		case "Gid":
			bit, ok = 8, statusInts(fields, &s.gid[0], &s.gid[1], &s.gid[2], &s.gid[3])
		case "CapPrm":
			bit, ok = 16, statusCap(fields, &s.capPrm)
		case "CapEff":
			bit, ok = 32, statusCap(fields, &s.capEff)
		case "CapAmb":
			bit, ok = 64, statusCap(fields, &s.capAmb)
		default:
			continue
		}
		if !ok || seen&bit != 0 {
			return linuxStatus{}, false
		}
		seen |= bit
	}
	return s, seen == 127
}

func statusInts(fields []string, out ...*int) bool {
	if len(fields) != len(out) {
		return false
	}
	for i, f := range fields {
		v, ok := linuxDecimal(f)
		if !ok {
			return false
		}
		*out[i] = v
	}
	return true
}

func statusCap(fields []string, out *uint64) bool {
	if len(fields) != 1 || len(fields[0]) != 16 {
		return false
	}
	for _, c := range fields[0] {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	v, err := strconv.ParseUint(fields[0], 16, 64)
	*out = v
	return err == nil
}

// linuxDecimal accepts a canonical unsigned 32-bit decimal.
func linuxDecimal(s string) (int, bool) {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || strconv.FormatUint(v, 10) != s {
		return 0, false
	}
	return int(v), true
}

// linuxACL reads the POSIX ACL attributes of the held descriptor itself
// (fgetxattr(2), never a path); a directory's default ACL is read too. ENODATA
// is the only accepted "no ACL" answer. ENOTSUP (ACLs unsupported or disabled
// on the mount), ERANGE (more than maxACEs entries) and any other error fail
// closed instead of falling back to the mode bits.
func linuxACL(sys linuxSys, fd int, directory bool) (present bool, entries int, ok bool) {
	names := []string{aclAccess}
	if directory {
		names = append(names, aclDefault)
	}
	buf := make([]byte, 4+8*maxACEs)
	for _, name := range names {
		n, err := sys.fgetxattr(fd, name, buf)
		if err == unix.ENODATA {
			continue
		}
		if err != nil || n <= 0 || n > len(buf) {
			return false, 0, false
		}
		c, ok := posixACLEntries(buf[:n])
		if !ok {
			return false, 0, false
		}
		present, entries = true, entries+c
	}
	return present, entries, true
}

// posixACLEntries validates the kernel's POSIX ACL xattr encoding (version 2
// header, then little-endian {tag u16, perm u16, id u32} entries) and returns
// the entry count. Linux POSIX ACLs have no deny entries, so every stored
// entry counts as an allow: posixState turns any stored access or default ACL,
// even a minimal one, into an untrusted all-rights grant and the node is
// refused. Unknown tags, permission bits or encodings are unsupported.
func posixACLEntries(b []byte) (int, bool) {
	if len(b) < 4+8 || (len(b)-4)%8 != 0 || binary.LittleEndian.Uint32(b) != aclVersion {
		return 0, false
	}
	n := (len(b) - 4) / 8
	if n > maxACEs {
		return 0, false
	}
	for i := 4; i < len(b); i += 8 {
		switch binary.LittleEndian.Uint16(b[i:]) {
		case 0x01, 0x02, 0x04, 0x08, 0x10, 0x20: // USER_OBJ, USER, GROUP_OBJ, GROUP, MASK, OTHER
		default:
			return 0, false
		}
		if binary.LittleEndian.Uint16(b[i+2:])&^7 != 0 {
			return 0, false
		}
	}
	return n, true
}

func linuxRawStat(st *unix.Stat_t) posixStat {
	size := ^uint64(0) // a negative size is unsupported
	if st.Size >= 0 {
		size = uint64(st.Size)
	}
	return posixStat{
		dev: uint64(st.Dev), ino: uint64(st.Ino), nlink: uint64(st.Nlink), size: size,
		mode: uint32(st.Mode), uid: st.Uid,
		stamp: [4]int64{int64(st.Mtim.Sec), int64(st.Mtim.Nsec), int64(st.Ctim.Sec), int64(st.Ctim.Nsec)},
	}
}

// linuxState maps one descriptor's evidence through the shared posixState.
// Linux has no st_flags; attrs stays zero.
func linuxState(st *unix.Stat_t, fsType int64, aclPresent bool, aclEntries int) state {
	return posixState(linuxRawStat(st), linuxLocal(fsType), aclPresent, aclEntries)
}

// linuxKernel holds the identity evidence taken by enter. Before every open,
// inspect, resolve and read, and after every inspect, resolve and read, the
// calling thread's syscall IDs must still equal it; exit re-reads the full
// evidence after the last close.
type linuxKernel struct {
	sys     linuxSys
	entered bool
	ev      linuxEvidence
}

func (k *linuxKernel) enter() (account, error) {
	if k.entered {
		return account{}, ErrDenied
	}
	acct, ev, err := linuxAccount(k.sys)
	if err != nil {
		return account{}, err
	}
	k.entered, k.ev = true, ev
	return acct, nil
}

func (k *linuxKernel) exit() (account, error) {
	acct, ev, err := linuxAccount(k.sys)
	if err != nil {
		return account{}, err
	}
	if !k.entered || ev != k.ev {
		return account{}, ErrDenied
	}
	return acct, nil
}

func (k *linuxKernel) same() bool {
	t, ok := linuxThreadIDs(k.sys)
	return ok && k.entered && t == k.ev.thread
}

func (k *linuxKernel) open(parent handle, n node) (handle, error) {
	if !k.same() {
		return nil, ErrDenied
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NOCTTY
	if n.role == roleLeaf {
		flags |= unix.O_NONBLOCK // a FIFO must not block; non-regular files are refused later
	} else {
		flags |= unix.O_DIRECTORY
	}
	dirfd, name := unix.AT_FDCWD, "/"
	if parent == nil {
		if n.path != "/" || n.name != "" {
			return nil, ErrDenied
		}
	} else {
		p, ok := parent.(*linuxFile)
		if !ok || p.closed || p.k != k || n.name == "" || strings.Contains(n.name, "/") {
			return nil, ErrDenied
		}
		dirfd, name = p.fd, n.name
	}
	fd, err := openRetry(k.sys, dirfd, name, flags)
	if err != nil {
		return nil, ErrDenied
	}
	return &linuxFile{k: k, fd: fd}, nil
}

func openRetry(sys linuxSys, dirfd int, name string, flags int) (int, error) {
	for range 8 {
		fd, err := sys.openat(dirfd, name, flags)
		if err == unix.EINTR {
			continue
		}
		if err != nil || fd < 0 {
			return -1, ErrDenied
		}
		return fd, nil
	}
	return -1, ErrDenied
}

type linuxFile struct {
	k      *linuxKernel
	fd     int
	closed bool
}

func (f *linuxFile) inspect() (state, error) {
	if f.closed || !f.k.same() {
		return state{}, ErrDenied
	}
	var st unix.Stat_t
	var fs unix.Statfs_t
	if f.k.sys.fstat(f.fd, &st) != nil || f.k.sys.fstatfs(f.fd, &fs) != nil {
		return state{}, ErrDenied
	}
	present, entries, ok := linuxACL(f.k.sys, f.fd, uint32(st.Mode)&unix.S_IFMT == unix.S_IFDIR)
	if !ok || !f.k.same() {
		return state{}, ErrDenied
	}
	return linuxState(&st, int64(fs.Type), present, entries), nil
}

func (f *linuxFile) resolve(name string) (objectID, error) {
	if f.closed || name == "" || strings.Contains(name, "/") || !f.k.same() {
		return objectID{}, ErrDenied
	}
	var st unix.Stat_t
	if f.k.sys.fstatat(f.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT) != nil || !f.k.same() {
		return objectID{}, ErrDenied
	}
	return posixState(linuxRawStat(&st), false, false, 0).id, nil
}

func (f *linuxFile) Read(b []byte) (int, error) {
	if f.closed || !f.k.same() {
		return 0, ErrDenied
	}
	n, err := fdRead(f.k.sys, f.fd, b)
	if (err != nil && err != io.EOF) || !f.k.same() {
		return 0, ErrDenied
	}
	return n, err
}

// close is attempted once. A failed close(2), EINTR included, is not retried:
// Linux releases the descriptor number regardless, and it may be reused.
func (f *linuxFile) close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.k.sys.close(f.fd) != nil {
		return ErrCleanup
	}
	return nil
}

type fdReader struct {
	sys linuxSys
	fd  int
}

func (r fdReader) Read(b []byte) (int, error) { return fdRead(r.sys, r.fd, b) }

func fdRead(sys linuxSys, fd int, b []byte) (int, error) {
	for range 8 {
		n, err := sys.read(fd, b)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n < 0 || n > len(b) {
			return 0, ErrDenied
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
	return 0, ErrDenied
}
