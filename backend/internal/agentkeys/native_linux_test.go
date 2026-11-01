//go:build linux && !android

package agentkeys

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// These tests drive the real linuxKernel and linuxFile code through an
// in-memory linuxSys table. No real file, process, procfs, account or network
// is used, so passing them is not evidence that Linux system calls, procfs or
// filesystems behave as docs/agent-key-loader-linux.md assumes.

type fakeLinuxNode struct {
	dev, ino uint64
	mode     uint32
	uid      uint32
	links    int64
	stamp    [4]int64
	fsType   int64
	acl      map[string][]byte
	aclErr   error
	data     []byte
	gen      func() []byte
	link     string
	onEOF    func()
	entries  map[string]*fakeLinuxNode
}

func (n *fakeLinuxNode) clone(ino uint64) *fakeLinuxNode {
	c := *n
	c.ino = ino
	return &c
}

// setNum assigns to a unix struct field whose integer type differs by GOARCH.
func setNum(p any, v int64) {
	e := reflect.ValueOf(p).Elem()
	if e.CanInt() {
		e.SetInt(v)
	} else {
		e.SetUint(uint64(v))
	}
}

func (n *fakeLinuxNode) stat(st *unix.Stat_t) {
	*st = unix.Stat_t{}
	setNum(&st.Dev, int64(n.dev))
	setNum(&st.Ino, int64(n.ino))
	setNum(&st.Nlink, n.links)
	setNum(&st.Mode, int64(n.mode))
	st.Uid = n.uid
	st.Size = int64(len(n.data))
	if n.mode&unix.S_IFMT == unix.S_IFDIR {
		st.Size = 4096
	}
	st.Mtim = unix.NsecToTimespec(n.stamp[0]*1e9 + n.stamp[1])
	st.Ctim = unix.NsecToTimespec(n.stamp[2]*1e9 + n.stamp[3])
}

type fakeLinuxFD struct {
	node *fakeLinuxNode
	name string
	data []byte
	off  int
}

type fakeLinux struct {
	root      *fakeLinuxNode
	n         map[string]*fakeLinuxNode
	fds       map[int]*fakeLinuxFD
	next      int
	opened    []string
	flags     []int
	closes    map[int]int
	openErr   map[string][]error
	readErr   map[string][]error
	closeErr  map[string]error
	capErr    error
	afterOpen func(name string)

	tid, pid               int
	uid, gid               [3]int
	fsuid, fsgid           int
	capEff, capPrm, capAmb uint64
}

const firstFakeFD = 100

// status renders the thread's status like the kernel, including a process
// name whose escaped newline must not start a line.
func (f *fakeLinux) status() []byte {
	return fmt.Appendf(nil, "Name:\tgw\\nUid:\t0\t0\t0\t0\nUmask:\t0022\nState:\tR (running)\nTgid:\t%d\nNgid:\t0\nPid:\t%d\nPPid:\t1\nTracerPid:\t0\n"+
		"Uid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nFDSize:\t64\nGroups:\t%d \nNSpid:\t%d\n"+
		"CapInh:\t0000000000000000\nCapPrm:\t%016x\nCapEff:\t%016x\nCapBnd:\t000001ffffffffff\nCapAmb:\t%016x\nNoNewPrivs:\t1\n",
		f.pid, f.tid, f.uid[0], f.uid[1], f.uid[2], f.fsuid, f.gid[0], f.gid[1], f.gid[2], f.fsgid, f.gid[0], f.tid, f.capPrm, f.capEff, f.capAmb)
}

func fakeDir(dev, ino uint64, fs int64, uid, mode uint32) *fakeLinuxNode {
	return &fakeLinuxNode{dev: dev, ino: ino, mode: unix.S_IFDIR | mode, uid: uid, links: 3, stamp: [4]int64{1, 2, 3, int64(ino)}, fsType: fs, entries: map[string]*fakeLinuxNode{}}
}

func fakeRegular(dev, ino uint64, fs int64, uid, mode uint32, data []byte) *fakeLinuxNode {
	return &fakeLinuxNode{dev: dev, ino: ino, mode: unix.S_IFREG | mode, uid: uid, links: 1, stamp: [4]int64{5, 6, 7, int64(ino)}, fsType: fs, data: data}
}

// linuxContainer is the synthetic layout of linuxFactsFixture plus a procfs
// for pid 4242 / tid 4243 running as uid and gid 1000.
func linuxContainer(key []byte) *fakeLinux {
	f := &fakeLinux{
		fds: map[int]*fakeLinuxFD{}, next: firstFakeFD, closes: map[int]int{},
		openErr: map[string][]error{}, readErr: map[string][]error{}, closeErr: map[string]error{},
		tid: 4243, pid: 4242, fsuid: containerUID, fsgid: containerUID,
		uid: [3]int{containerUID, containerUID, containerUID}, gid: [3]int{containerUID, containerUID, containerUID},
	}
	root := fakeDir(0x2f, 2, overlayMagic, 0, 0o755)
	home := fakeDir(0x2f, 3, overlayMagic, 0, 0o755)
	gw := fakeDir(0x2f, 4, overlayMagic, containerUID, 0o700)
	rescue := fakeDir(0x30, 2, tmpfsMagic, containerUID, 0o700)
	leaf := fakeRegular(0x30, 3, tmpfsMagic, containerUID, 0o600, key)
	config := fakeRegular(0x30, 4, tmpfsMagic, containerUID, 0o600, []byte("{\"synthetic\":true}\n"))
	root.entries["home"], home.entries["gw"], gw.entries[".rescue"] = home, gw, rescue
	rescue.entries["agent_ed25519"], rescue.entries["gateway.json"] = leaf, config

	proc := fakeDir(5, procRootIno, procMagic, 0, 0o555)
	self := &fakeLinuxNode{dev: 5, ino: 3, mode: unix.S_IFLNK | 0o777, links: 1, fsType: procMagic, link: "4242/task/4243"}
	pid := fakeDir(5, 1000, procMagic, containerUID, 0o555)
	task := fakeDir(5, 1001, procMagic, containerUID, 0o555)
	tid := fakeDir(5, 1002, procMagic, containerUID, 0o555)
	status := fakeRegular(5, 1003, procMagic, containerUID, 0o444, nil)
	status.gen = f.status
	sys := fakeDir(5, 2000, procMagic, 0, 0o555)
	kern := fakeDir(5, 2001, procMagic, 0, 0o555)
	overflow := fakeRegular(5, 2002, procMagic, 0, 0o644, []byte("65534\n"))
	root.entries["proc"] = proc
	proc.entries["thread-self"], proc.entries["4242"], proc.entries["sys"] = self, pid, sys
	pid.entries["task"], task.entries["4243"], tid.entries["status"] = task, tid, status
	sys.entries["kernel"], kern.entries["overflowuid"] = kern, overflow

	f.root = root
	f.n = map[string]*fakeLinuxNode{
		"/": root, "home": home, "gw": gw, ".rescue": rescue, "key": leaf, "config": config,
		"proc": proc, "thread-self": self, "pid": pid, "task": task, "tid": tid, "status": status, "overflowuid": overflow,
	}
	return f
}

func (f *fakeLinux) sys() linuxSys {
	return linuxSys{
		openat:     f.openat,
		fstat:      f.fstat,
		fstatat:    f.fstatat,
		fstatfs:    f.fstatfs,
		fgetxattr:  f.fgetxattr,
		readlinkat: f.readlinkat,
		read:       f.read,
		close:      f.close,
		gettid:     func() int { return f.tid },
		getpid:     func() int { return f.pid },
		getresuid:  func() (int, int, int) { return f.uid[0], f.uid[1], f.uid[2] },
		getresgid:  func() (int, int, int) { return f.gid[0], f.gid[1], f.gid[2] },
		capget:     f.capget,
	}
}

func (f *fakeLinux) openat(dirfd int, name string, flags int) (int, error) {
	f.opened, f.flags = append(f.opened, name), append(f.flags, flags)
	if q := f.openErr[name]; len(q) > 0 {
		f.openErr[name] = q[1:]
		return -1, q[0]
	}
	var n *fakeLinuxNode
	switch {
	case dirfd == unix.AT_FDCWD && name == "/":
		n = f.root
	case dirfd == unix.AT_FDCWD && name == "/proc":
		n = f.root.entries["proc"]
	case dirfd == unix.AT_FDCWD:
		return -1, unix.EINVAL
	default:
		d := f.fds[dirfd]
		if d == nil {
			return -1, unix.EBADF
		}
		n = d.node.entries[name]
	}
	switch {
	case n == nil:
		return -1, unix.ENOENT
	case n.link != "" && flags&unix.O_NOFOLLOW != 0:
		return -1, unix.ELOOP
	case flags&unix.O_DIRECTORY != 0 && n.mode&unix.S_IFMT != unix.S_IFDIR:
		return -1, unix.ENOTDIR
	}
	f.next++
	f.fds[f.next] = &fakeLinuxFD{node: n, name: name}
	if f.afterOpen != nil {
		f.afterOpen(name)
	}
	return f.next, nil
}

func (f *fakeLinux) fstat(fd int, st *unix.Stat_t) error {
	d := f.fds[fd]
	if d == nil {
		return unix.EBADF
	}
	d.node.stat(st)
	return nil
}

func (f *fakeLinux) fstatat(dirfd int, name string, st *unix.Stat_t, flags int) error {
	d := f.fds[dirfd]
	switch {
	case flags&unix.AT_SYMLINK_NOFOLLOW == 0:
		return unix.EINVAL
	case d == nil:
		return unix.EBADF
	case d.node.entries[name] == nil:
		return unix.ENOENT
	}
	d.node.entries[name].stat(st)
	return nil
}

func (f *fakeLinux) fstatfs(fd int, st *unix.Statfs_t) error {
	d := f.fds[fd]
	if d == nil {
		return unix.EBADF
	}
	*st = unix.Statfs_t{}
	setNum(&st.Type, d.node.fsType)
	return nil
}

func (f *fakeLinux) fgetxattr(fd int, name string, dest []byte) (int, error) {
	d := f.fds[fd]
	if d == nil {
		return -1, unix.EBADF
	}
	if d.node.aclErr != nil {
		return -1, d.node.aclErr
	}
	v, ok := d.node.acl[name]
	if !ok {
		return -1, unix.ENODATA
	}
	if len(dest) < len(v) {
		return -1, unix.ERANGE
	}
	return copy(dest, v), nil
}

func (f *fakeLinux) readlinkat(dirfd int, name string, buf []byte) (int, error) {
	d := f.fds[dirfd]
	if d == nil {
		return -1, unix.EBADF
	}
	c := d.node.entries[name]
	if c == nil {
		return -1, unix.ENOENT
	}
	if c.link == "" {
		return -1, unix.EINVAL
	}
	return copy(buf, c.link), nil
}

func (f *fakeLinux) read(fd int, b []byte) (int, error) {
	d := f.fds[fd]
	if d == nil {
		return -1, unix.EBADF
	}
	if q := f.readErr[d.name]; len(q) > 0 {
		f.readErr[d.name] = q[1:]
		return -1, q[0]
	}
	if d.data == nil {
		d.data = d.node.data
		if d.node.gen != nil {
			d.data = d.node.gen()
		}
	}
	if d.off >= len(d.data) {
		if hook := d.node.onEOF; hook != nil {
			d.node.onEOF = nil
			hook()
		}
		return 0, nil
	}
	n := copy(b, d.data[d.off:])
	d.off += n
	return n, nil
}

func (f *fakeLinux) close(fd int) error {
	f.closes[fd]++
	d := f.fds[fd]
	if d == nil {
		return unix.EBADF
	}
	delete(f.fds, fd)
	if err := f.closeErr[d.name]; err != nil {
		delete(f.closeErr, d.name)
		return err
	}
	return nil
}

func (f *fakeLinux) capget(hdr *unix.CapUserHeader, data *unix.CapUserData) error {
	if hdr.Version != unix.LINUX_CAPABILITY_VERSION_3 || hdr.Pid != 0 {
		return unix.EINVAL
	}
	if f.capErr != nil {
		return f.capErr
	}
	d := (*[2]unix.CapUserData)(unsafe.Pointer(data))
	d[0] = unix.CapUserData{Effective: uint32(f.capEff), Permitted: uint32(f.capPrm)}
	d[1] = unix.CapUserData{Effective: uint32(f.capEff >> 32), Permitted: uint32(f.capPrm >> 32)}
	return nil
}

func (f *fakeLinux) factory() func() (kernel, error) {
	return func() (kernel, error) { return &linuxKernel{sys: f.sys()}, nil }
}

func (f *fakeLinux) load(t *testing.T) (ssh.Signer, error) {
	t.Helper()
	return load(deadline(t), posixPaths, f.factory(), Config{Path: containerKeyPath, Fingerprint: testFingerprint})
}

// settled checks that every descriptor was closed exactly once and that every
// open was read-only, no-follow and close-on-exec.
func (f *fakeLinux) settled(t *testing.T) {
	t.Helper()
	if len(f.fds) != 0 || len(f.closes) != f.next-firstFakeFD {
		t.Fatalf("%d descriptors open, %d of %d closed", len(f.fds), len(f.closes), f.next-firstFakeFD)
	}
	for fd, n := range f.closes {
		if n != 1 {
			t.Fatalf("descriptor %d closed %d times", fd, n)
		}
	}
	for i, fl := range f.flags {
		if fl&unix.O_ACCMODE != unix.O_RDONLY || fl&(unix.O_CREAT|unix.O_TRUNC|unix.O_APPEND|unix.O_PATH) != 0 || fl&unix.O_NOFOLLOW == 0 || fl&unix.O_CLOEXEC == 0 {
			t.Fatalf("open %q with flags %#x", f.opened[i], fl)
		}
	}
}

func (f *fakeLinux) refused(t *testing.T, want error) {
	t.Helper()
	if signer, err := f.load(t); signer != nil || !errors.Is(err, want) {
		t.Fatalf("Load = %v, %v; want %v", signer, err, want)
	}
	f.settled(t)
}

var procWalk = []string{"/proc", "4242", "task", "4243", "status", "sys", "kernel", "overflowuid"}

func aclBytes(entries ...[3]uint32) []byte {
	b := binary.LittleEndian.AppendUint32(nil, aclVersion)
	for _, e := range entries {
		b = binary.LittleEndian.AppendUint16(b, uint16(e[0]))
		b = binary.LittleEndian.AppendUint16(b, uint16(e[1]))
		b = binary.LittleEndian.AppendUint32(b, e[2])
	}
	return b
}

var (
	minimalACL = aclBytes([3]uint32{0x01, 6, 0}, [3]uint32{0x04, 4, 0}, [3]uint32{0x20, 4, 0})
	userACL    = aclBytes([3]uint32{0x01, 7, 0}, [3]uint32{0x02, 5, 1001}, [3]uint32{0x04, 5, 0}, [3]uint32{0x10, 5, 0}, [3]uint32{0x20, 0, 0})
)

func TestLinuxNativeKernelIsBoundToSystemCalls(t *testing.T) {
	k, err := nativeKernel()
	lk, ok := k.(*linuxKernel)
	if err != nil || !ok || lk.entered || nativeStyle != posixPaths {
		t.Fatalf("nativeKernel = %T, %v", k, err)
	}
	v := reflect.ValueOf(lk.sys)
	for i := range v.NumField() {
		if v.Field(i).IsNil() {
			t.Fatalf("system call %s is not bound", v.Type().Field(i).Name)
		}
	}
	// Function identity only; nothing is called.
	for _, pair := range [][2]any{{lk.sys.fstat, unix.Fstat}, {lk.sys.fstatat, unix.Fstatat}, {lk.sys.fstatfs, unix.Fstatfs},
		{lk.sys.fgetxattr, unix.Fgetxattr}, {lk.sys.close, unix.Close}, {lk.sys.getresuid, unix.Getresuid}, {lk.sys.capget, unix.Capget}} {
		if reflect.ValueOf(pair[0]).Pointer() != reflect.ValueOf(pair[1]).Pointer() {
			t.Fatal("native system call table is not x/sys/unix")
		}
	}
}

func TestLinuxKernelLoadsContainerKey(t *testing.T) {
	f := linuxContainer(syntheticKey(t, nil))
	signer, err := f.load(t)
	if err != nil || signer == nil || ssh.FingerprintSHA256(signer.PublicKey()) != testFingerprint {
		t.Fatalf("Load = %v, %v", signer, err)
	}
	f.settled(t)
	want := slices.Concat(procWalk, []string{"/", "home", "gw", ".rescue", "agent_ed25519"}, procWalk)
	if !slices.Equal(f.opened, want) {
		t.Fatalf("opened %v", f.opened)
	}
	for i, name := range f.opened {
		leaf, dir := f.flags[i]&unix.O_NONBLOCK != 0, f.flags[i]&unix.O_DIRECTORY != 0
		if name == "agent_ed25519" && (!leaf || dir) || slices.Contains([]string{"/", "home", "gw", ".rescue"}, name) && !dir {
			t.Fatalf("open %q with flags %#x", name, f.flags[i])
		}
	}
}

func TestLinuxKernelReadsTrustConfig(t *testing.T) {
	f := linuxContainer(syntheticKey(t, nil))
	got, err := readTrustConfig(deadline(t), posixPaths, f.factory(), containerConfigPath)
	if err != nil || !bytes.Equal(got, f.n["config"].data) {
		t.Fatalf("readTrustConfig = %q, %v", got, err)
	}
	f.settled(t)

	f = linuxContainer(syntheticKey(t, nil))
	f.n["config"].aclErr = unix.EOPNOTSUPP
	if got, err := readTrustConfig(deadline(t), posixPaths, f.factory(), containerConfigPath); got != nil || err != ErrDenied {
		t.Fatalf("readTrustConfig without ACL support = %q, %v", got, err)
	}
	f.settled(t)
}

func TestLinuxKernelRefusesUnsafeFilesystemFacts(t *testing.T) {
	const (
		fuseMagic  = 0x65735546 // FUSE_SUPER_MAGIC, also virtiofs and fuse-overlayfs
		nfsMagic   = 0x6969
		v9fsMagic  = 0x01021997 // 9p, as used by WSL2 DrvFS
		cifsMagic  = 0xff534d42
		smb2Magic  = 0xfe534d42
		btrfsMagic = 0x9123683e
		xfsMagic   = 0x58465342
		ramfsMagic = 0x858458f6
	)
	set := func(name string, change func(*fakeLinuxNode)) func(*fakeLinux) {
		return func(f *fakeLinux) { change(f.n[name]) }
	}
	fsType := func(name string, magic int64) func(*fakeLinux) {
		return set(name, func(n *fakeLinuxNode) { n.fsType = magic })
	}
	replace := func(dir, name string, n func(*fakeLinux) *fakeLinuxNode) func(*fakeLinux) {
		return func(f *fakeLinux) { f.n[dir].entries[name] = n(f) }
	}
	symlink := func(*fakeLinux) *fakeLinuxNode {
		return &fakeLinuxNode{dev: 0x30, ino: 77, mode: unix.S_IFLNK | 0o777, links: 1, fsType: tmpfsMagic, link: "/elsewhere"}
	}
	cases := []struct {
		name string
		edit func(*fakeLinux)
	}{
		{"minimal access ACL on key", set("key", func(n *fakeLinuxNode) { n.acl = map[string][]byte{aclAccess: minimalACL} })},
		{"minimal access ACL on parent", set(".rescue", func(n *fakeLinuxNode) { n.acl = map[string][]byte{aclAccess: minimalACL} })},
		{"named-user ACL on ancestor", set("home", func(n *fakeLinuxNode) { n.acl = map[string][]byte{aclAccess: userACL} })},
		{"default ACL on parent", set(".rescue", func(n *fakeLinuxNode) { n.acl = map[string][]byte{aclDefault: userACL} })},
		{"malformed ACL on key", set("key", func(n *fakeLinuxNode) { n.acl = map[string][]byte{aclAccess: {2, 0, 0, 0}} })},
		{"ACL unsupported on key", set("key", func(n *fakeLinuxNode) { n.aclErr = unix.EOPNOTSUPP })},
		{"ACL unsupported on root", set("/", func(n *fakeLinuxNode) { n.aclErr = unix.EOPNOTSUPP })},
		{"ACL list too large", set("key", func(n *fakeLinuxNode) { n.aclErr = unix.ERANGE })},
		{"ACL query interrupted", set("key", func(n *fakeLinuxNode) { n.aclErr = unix.EINTR })},
		{"ACL query not permitted", set(".rescue", func(n *fakeLinuxNode) { n.aclErr = unix.EACCES })},
		{"FUSE key", fsType("key", fuseMagic)},
		{"FUSE parent", fsType(".rescue", fuseMagic)},
		{"NFS ancestor", fsType("gw", nfsMagic)},
		{"9p ancestor", fsType("home", v9fsMagic)},
		{"CIFS parent", fsType(".rescue", cifsMagic)},
		{"SMB2 parent", fsType(".rescue", smb2Magic)},
		{"btrfs root", fsType("/", btrfsMagic)},
		{"xfs parent", fsType(".rescue", xfsMagic)},
		{"ramfs root", fsType("/", ramfsMagic)},
		{"procfs key", fsType("key", procMagic)},
		{"unknown filesystem", fsType("gw", 0)},
		{"symlink key", replace(".rescue", "agent_ed25519", symlink)},
		{"symlink parent", replace("gw", ".rescue", symlink)},
		{"FIFO key", set("key", func(n *fakeLinuxNode) { n.mode = unix.S_IFIFO | 0o600 })},
		{"group-readable key", set("key", func(n *fakeLinuxNode) { n.mode = unix.S_IFREG | 0o640 })},
		{"setuid key", set("key", func(n *fakeLinuxNode) { n.mode = unix.S_IFREG | 0o4600 })},
		{"hard-linked key", set("key", func(n *fakeLinuxNode) { n.links = 2 })},
		{"empty key", set("key", func(n *fakeLinuxNode) { n.data = nil })},
		{"oversized key", set("key", func(n *fakeLinuxNode) { n.data = make([]byte, maxKeyBytes+1) })},
		{"foreign key owner", set("key", func(n *fakeLinuxNode) { n.uid = containerUID + 1 })},
		{"parent owned by overflowuid", set(".rescue", func(n *fakeLinuxNode) { n.uid = 65534 })},
		{"world-writable parent", set(".rescue", func(n *fakeLinuxNode) { n.mode = unix.S_IFDIR | 0o777 })},
		{"sticky world-writable ancestor", set("gw", func(n *fakeLinuxNode) { n.mode, n.uid = unix.S_IFDIR|0o1777, 0 })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := linuxContainer(syntheticKey(t, nil))
			c.edit(f)
			f.refused(t, ErrDenied)
		})
	}
}

func TestLinuxKernelRefusesUnprovenIdentityBeforeOpeningPath(t *testing.T) {
	statusWithout := func(key string) func(*fakeLinux) {
		return func(f *fakeLinux) {
			f.n["status"].gen = func() []byte {
				var kept []string
				for _, line := range strings.SplitAfter(string(f.status()), "\n") {
					if !strings.HasPrefix(line, key+":") {
						kept = append(kept, line)
					}
				}
				return []byte(strings.Join(kept, ""))
			}
		}
	}
	cases := []struct {
		name string
		edit func(*fakeLinux)
	}{
		{"root", func(f *fakeLinux) { f.uid, f.gid, f.fsuid, f.fsgid = [3]int{}, [3]int{}, 0, 0 }},
		{"running as overflowuid", func(f *fakeLinux) { f.uid, f.fsuid = [3]int{65534, 65534, 65534}, 65534 }},
		{"overflowuid is root", func(f *fakeLinux) { f.n["overflowuid"].data = []byte("0\n") }},
		{"overflowuid malformed", func(f *fakeLinux) { f.n["overflowuid"].data = []byte("65534") }},
		{"real uid differs", func(f *fakeLinux) { f.uid[0] = containerUID + 1 }},
		{"saved uid differs", func(f *fakeLinux) { f.uid[2] = containerUID + 1 }},
		{"file-system uid differs", func(f *fakeLinux) { f.fsuid = containerUID + 1 }},
		{"saved gid differs", func(f *fakeLinux) { f.gid[2] = containerUID + 1 }},
		{"file-system gid differs", func(f *fakeLinux) { f.fsgid = containerUID + 1 }},
		{"negative uid", func(f *fakeLinux) { f.uid, f.fsuid = [3]int{-1, -1, -1}, -1 }},
		{"effective capability", func(f *fakeLinux) { f.capEff = 1 << 21 }},
		{"permitted capability", func(f *fakeLinux) { f.capPrm = 1 << 33 }},
		{"ambient capability", func(f *fakeLinux) { f.capAmb = 1 << 10 }},
		{"no procfs", func(f *fakeLinux) { delete(f.root.entries, "proc") }},
		{"/proc is tmpfs", func(f *fakeLinux) { f.n["proc"].fsType = tmpfsMagic }},
		{"/proc is not the procfs root", func(f *fakeLinux) { f.n["proc"].ino = 7 }},
		{"thread-self names another pid namespace", func(f *fakeLinux) { f.n["thread-self"].link = "1/task/1" }},
		{"thread-self is not a link", func(f *fakeLinux) { f.n["thread-self"].link = "" }},
		{"task entry is a symlink", func(f *fakeLinux) {
			f.n["task"].entries["4243"] = &fakeLinuxNode{dev: 5, ino: 9, mode: unix.S_IFLNK | 0o777, links: 1, fsType: procMagic, link: "../../1/task/1"}
		}},
		{"status on another device", func(f *fakeLinux) { f.n["status"].dev = 6 }},
		{"status not on procfs", func(f *fakeLinux) { f.n["status"].fsType = tmpfsMagic }},
		{"status without CapAmb", statusWithout("CapAmb")},
		{"status without Uid", statusWithout("Uid")},
		{"capget fails", func(f *fakeLinux) { f.capErr = unix.EPERM }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := linuxContainer(syntheticKey(t, nil))
			c.edit(f)
			f.refused(t, ErrDenied)
			if slices.Contains(f.opened, "/") {
				t.Fatalf("opened the key path after an unproven identity: %v", f.opened)
			}
		})
	}
}

func TestLinuxKernelRechecksIdentityAroundOperations(t *testing.T) {
	cases := []struct {
		name string
		edit func(*fakeLinux)
	}{
		{"effective uid changes during read", func(f *fakeLinux) { f.n["key"].onEOF = func() { f.uid[1] = containerUID + 1 } }},
		{"capability gained after open", func(f *fakeLinux) {
			f.afterOpen = func(name string) {
				if name == ".rescue" {
					f.capEff = 1
				}
			}
		}},
		{"thread changes after open", func(f *fakeLinux) {
			f.afterOpen = func(name string) {
				if name == "gw" {
					f.tid = 9999
				}
			}
		}},
		{"file-system uid differs at exit", func(f *fakeLinux) { f.n["key"].onEOF = func() { f.fsuid = containerUID + 1 } }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := linuxContainer(syntheticKey(t, nil))
			c.edit(f)
			f.refused(t, ErrDenied)
		})
	}
}

func TestLinuxKernelSameHandleRevalidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*fakeLinux)
		want error
	}{
		{"key substituted between open and inspection", func(f *fakeLinux) {
			f.afterOpen = func(name string) {
				if name == "agent_ed25519" {
					f.n[".rescue"].entries[name] = f.n["key"].clone(99)
				}
			}
		}, ErrDenied},
		{"key ctime changes during read", func(f *fakeLinux) { f.n["key"].onEOF = func() { f.n["key"].stamp[3]++ } }, ErrDenied},
		{"parent replaced during read", func(f *fakeLinux) {
			f.n["key"].onEOF = func() { f.n["gw"].entries[".rescue"] = f.n[".rescue"].clone(98) }
		}, ErrDenied},
		{"ACL added to parent during read", func(f *fakeLinux) {
			f.n["key"].onEOF = func() { f.n[".rescue"].acl = map[string][]byte{aclAccess: minimalACL} }
		}, ErrDenied},
		{"ancestor filesystem changes during read", func(f *fakeLinux) {
			f.n["key"].onEOF = func() { f.n["home"].fsType = 0x65735546 }
		}, ErrDenied},
		{"parent stamps change during read", func(f *fakeLinux) {
			f.n["key"].onEOF = func() { f.n[".rescue"].stamp = [4]int64{9, 9, 9, 9} }
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := linuxContainer(syntheticKey(t, nil))
			c.edit(f)
			if c.want == nil {
				if signer, err := f.load(t); signer == nil || err != nil {
					t.Fatalf("Load = %v, %v", signer, err)
				}
				f.settled(t)
				return
			}
			f.refused(t, c.want)
		})
	}
}

func TestLinuxKernelInterruptionAndCleanup(t *testing.T) {
	eintr := func(n int) []error { return slices.Repeat([]error{unix.EINTR}, n) }
	cases := []struct {
		name  string
		edit  func(*fakeLinux)
		want  error
		opens int // expected opens of the key name
	}{
		{"open interrupted twice is retried", func(f *fakeLinux) { f.openErr["agent_ed25519"] = eintr(2) }, nil, 3},
		{"open interrupted persistently", func(f *fakeLinux) { f.openErr["agent_ed25519"] = eintr(8) }, ErrDenied, 8},
		{"read interrupted twice is retried", func(f *fakeLinux) { f.readErr["agent_ed25519"] = eintr(2) }, nil, 1},
		{"read fails", func(f *fakeLinux) { f.readErr["agent_ed25519"] = []error{unix.EIO} }, ErrDenied, 1},
		{"key close interrupted is not retried", func(f *fakeLinux) { f.closeErr["agent_ed25519"] = unix.EINTR }, ErrCleanup, 1},
		{"root close fails", func(f *fakeLinux) { f.closeErr["/"] = unix.EIO }, ErrCleanup, 1},
		{"parent close fails after refusal", func(f *fakeLinux) {
			f.closeErr[".rescue"] = unix.EIO
			f.n["key"].mode = unix.S_IFREG | 0o644
		}, ErrCleanup, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := linuxContainer(syntheticKey(t, nil))
			c.edit(f)
			signer, err := f.load(t)
			if c.want == nil && (signer == nil || err != nil) || c.want != nil && (signer != nil || !errors.Is(err, c.want)) {
				t.Fatalf("Load = %v, %v; want %v", signer, err, c.want)
			}
			f.settled(t)
			if got := countOf(f.opened, "agent_ed25519"); got != c.opens {
				t.Fatalf("key opened %d times, want %d", got, c.opens)
			}
		})
	}
}

func countOf(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}

func TestLinuxStatusParsing(t *testing.T) {
	f := linuxContainer(nil)
	f.capPrm, f.capEff, f.capAmb = 0x1, 0x20, 0x300
	good := string(f.status())
	s, ok := parseStatus([]byte(good))
	want := linuxStatus{tgid: 4242, pid: 4243, uid: [4]int{1000, 1000, 1000, 1000}, gid: [4]int{1000, 1000, 1000, 1000}, capPrm: 0x1, capEff: 0x20, capAmb: 0x300}
	if !ok || s != want {
		t.Fatalf("parseStatus = %+v, %v", s, ok)
	}
	for _, c := range []struct{ name, text string }{
		{"no final newline", strings.TrimSuffix(good, "\n")},
		{"duplicate Uid", good + "Uid:\t0\t0\t0\t0\n"},
		{"duplicate Pid", good + "Pid:\t1\n"},
		{"three Uid fields", strings.Replace(good, "Uid:\t1000\t1000\t1000\t1000", "Uid:\t1000\t1000\t1000", 1)},
		{"non-canonical decimal", strings.Replace(good, "Gid:\t1000", "Gid:\t01000", 1)},
		{"uid beyond 32 bits", strings.Replace(good, "Uid:\t1000", "Uid:\t4294967296", 1)},
		{"uppercase capability", strings.Replace(good, "CapEff:\t0000000000000020", "CapEff:\t000000000000002A", 1)},
		{"short capability", strings.Replace(good, "CapAmb:\t0000000000000300", "CapAmb:\t300", 1)},
		{"space-separated Uid", strings.Replace(good, "Uid:\t1000", "Uid: 1000", 1)},
		{"NUL byte", strings.Replace(good, "State", "St\x00ate", 1)},
		{"empty", ""},
	} {
		if _, ok := parseStatus([]byte(c.text)); ok {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestLinuxPrincipal(t *testing.T) {
	ids := [3]int{containerUID, containerUID, containerUID}
	all := [4]int{containerUID, containerUID, containerUID, containerUID}
	good := linuxEvidence{
		thread:   linuxThread{tid: 4243, pid: 4242, uid: ids, gid: ids},
		status:   linuxStatus{tgid: 4242, pid: 4243, uid: all, gid: all},
		overflow: 65534,
	}
	acct, ok := linuxPrincipal(good)
	if !ok || !reflect.DeepEqual(acct, account{user: "uid:1000", control: []string{"uid:0"}}) || !acct.valid() {
		t.Fatalf("linuxPrincipal = %+v, %v", acct, ok)
	}
	gidZero := good
	gidZero.thread.gid, gidZero.status.gid = [3]int{}, [4]int{}
	if _, ok := linuxPrincipal(gidZero); !ok {
		t.Fatal("gid 0 is refused, but groups are never trusted and Darwin accepts it")
	}
	for _, c := range []struct {
		name string
		edit func(*linuxEvidence)
	}{
		{"root", func(e *linuxEvidence) { e.thread.uid, e.status.uid = [3]int{}, [4]int{} }},
		{"uid is overflowuid", func(e *linuxEvidence) { e.overflow = containerUID }},
		{"overflowuid is root", func(e *linuxEvidence) { e.overflow = 0 }},
		{"effective uid", func(e *linuxEvidence) { e.thread.uid[1] = 1001 }},
		{"saved uid", func(e *linuxEvidence) { e.thread.uid[2] = 1001 }},
		{"status real uid", func(e *linuxEvidence) { e.status.uid[0] = 1001 }},
		{"file-system uid", func(e *linuxEvidence) { e.status.uid[3] = 1001 }},
		{"effective gid", func(e *linuxEvidence) { e.thread.gid[1] = 1001 }},
		{"file-system gid", func(e *linuxEvidence) { e.status.gid[3] = 1001 }},
		{"negative gid", func(e *linuxEvidence) { e.thread.gid, e.status.gid = [3]int{-1, -1, -1}, [4]int{-1, -1, -1, -1} }},
		{"tgid mismatch", func(e *linuxEvidence) { e.status.tgid = 1 }},
		{"tid mismatch", func(e *linuxEvidence) { e.status.pid = 1 }},
		{"no tid", func(e *linuxEvidence) { e.thread.tid, e.status.pid = 0, 0 }},
		{"capget effective", func(e *linuxEvidence) { e.thread.capEff = 1 }},
		{"capget permitted", func(e *linuxEvidence) { e.thread.capPrm = 1 << 40 }},
		{"status effective", func(e *linuxEvidence) { e.status.capEff = 1 }},
		{"status permitted", func(e *linuxEvidence) { e.status.capPrm = 1 }},
		{"status ambient", func(e *linuxEvidence) { e.status.capAmb = 1 }},
	} {
		e := good
		c.edit(&e)
		if _, ok := linuxPrincipal(e); ok {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestLinuxPOSIXACLEncoding(t *testing.T) {
	entries := func(n int) []byte {
		e := make([][3]uint32, n)
		for i := range e {
			e[i] = [3]uint32{0x02, 4, uint32(i)}
		}
		return aclBytes(e...)
	}
	for _, c := range []struct {
		name string
		b    []byte
		n    int
		ok   bool
	}{
		{"minimal", minimalACL, 3, true},
		{"named user with mask", userACL, 5, true},
		{"maxACEs entries", entries(maxACEs), maxACEs, true},
		{"too many entries", entries(maxACEs + 1), 0, false},
		{"header only", aclBytes(), 0, false},
		{"version 1", append([]byte{1, 0, 0, 0}, minimalACL[4:]...), 0, false},
		{"truncated entry", minimalACL[:len(minimalACL)-1], 0, false},
		{"unknown tag", aclBytes([3]uint32{0x40, 4, 0}), 0, false},
		{"unknown permission bit", aclBytes([3]uint32{0x01, 8, 0}), 0, false},
	} {
		if n, ok := posixACLEntries(c.b); n != c.n || ok != c.ok {
			t.Errorf("%s: posixACLEntries = %d, %v", c.name, n, ok)
		}
	}
	// Every stored ACL becomes an untrusted all-rights grant: Linux never
	// produces the deny-only shape that posixState accepts on Darwin directories.
	var st unix.Stat_t
	fakeDir(0x30, 2, tmpfsMagic, containerUID, 0o700).stat(&st)
	acct := account{user: uidPrincipal(containerUID), control: []string{uidPrincipal(0)}}
	parent := node{path: "/home/gw/.rescue", name: ".rescue", role: roleParent}
	if !checkNode(acct, posixPaths, parent, linuxState(&st, tmpfsMagic, false, 0)) || checkNode(acct, posixPaths, parent, linuxState(&st, tmpfsMagic, true, 3)) {
		t.Fatal("POSIX ACL presence is not refused on a directory")
	}
}

func TestLinuxFilesystemAllowlist(t *testing.T) {
	for magic, want := range map[int64]bool{
		0xef53: true, 0x01021994: true, 0x794c7630: true,
		0x6969: false, 0xff534d42: false, 0xfe534d42: false, 0x01021997: false, 0x65735546: false,
		0x9123683e: false, int64(int32(-0x6edc97c2)): false, 0x58465342: false, 0x858458f6: false,
		0x9fa0: false, 0x0187: false, 0: false, 0xef53 | 1<<32: false,
	} {
		if linuxLocal(magic) != want {
			t.Errorf("linuxLocal(%#x) = %v", magic, !want)
		}
	}
}

func TestLinuxIdentityCleanupFailure(t *testing.T) {
	for _, phase := range []string{"enter", "exit"} {
		t.Run(phase, func(t *testing.T) {
			f := linuxContainer(syntheticKey(t, nil))
			if phase == "enter" {
				f.closeErr["status"] = unix.EIO
			} else {
				f.n["key"].onEOF = func() { f.closeErr["status"] = unix.EIO }
			}
			f.refused(t, ErrCleanup)
			if phase == "enter" && slices.Contains(f.opened, "/") {
				t.Fatal("opened key path after identity cleanup failure")
			}
		})
	}
}
