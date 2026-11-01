package agentkeys

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// The fake kernel is an in-memory object graph. Handles are bound to objects,
// not to names, so tests can rename or substitute a name while the loader
// still holds the original object, which is what same-handle revalidation
// must detect. No real file, account or native API is used.

var errFake = errors.New("fake kernel refusal")

type fakeObject struct {
	posix      *posixStat
	local      bool
	aclPresent bool
	aclAllow   int
	win        *winStat
	st         state
	data       []byte
	entries    map[string]*fakeObject
}

// refresh derives the handle state through the same shared mappings that the
// native files use.
func (o *fakeObject) refresh() *fakeObject {
	if o.win != nil {
		o.st = windowsState(*o.win)
	} else {
		o.st = posixState(*o.posix, o.local, o.aclPresent, o.aclAllow)
	}
	return o
}

// substitute returns an otherwise identical object with a new identity.
func (o *fakeObject) substitute() *fakeObject {
	c := *o
	if o.posix != nil {
		raw := *o.posix
		raw.ino += 1000
		c.posix = &raw
	}
	if o.win != nil {
		raw := *o.win
		raw.aces = append([]winACE(nil), o.win.aces...)
		raw.file[15] ^= 0x80
		c.win = &raw
	}
	return c.refresh()
}

type readStep struct {
	n    int
	err  error
	over bool
}

type fakeKernel struct {
	rootPath   string
	root       *fakeObject
	acct       account
	exitAcct   *account
	enterErr   error
	exitErr    error
	openErr    map[string]error
	closeErr   map[string]error
	resolveErr map[string]error
	inspectErr map[string]error
	plan       []readStep
	afterOpen  func(path string)
	afterRead  func()
	afterClose func(path string)

	enters, exits, readCalls int
	opened, closed           []string
	handles                  []*fakeHandle
	bufs                     [][]byte
}

func (k *fakeKernel) enter() (account, error) {
	k.enters++
	return k.acct, k.enterErr
}

func (k *fakeKernel) exit() (account, error) {
	k.exits++
	if k.exitAcct != nil {
		return *k.exitAcct, k.exitErr
	}
	return k.acct, k.exitErr
}

func (k *fakeKernel) open(parent handle, n node) (handle, error) {
	k.opened = append(k.opened, n.path)
	if err := k.openErr[n.path]; err != nil {
		return nil, err
	}
	var obj *fakeObject
	if parent == nil {
		if n.path != k.rootPath {
			return nil, errFake
		}
		obj = k.root
	} else {
		p, ok := parent.(*fakeHandle)
		if !ok || p.closed {
			return nil, errFake
		}
		obj = p.obj.entries[n.name]
	}
	if obj == nil {
		return nil, errFake
	}
	h := &fakeHandle{k: k, obj: obj, path: n.path}
	k.handles = append(k.handles, h)
	if k.afterOpen != nil {
		k.afterOpen(n.path)
	}
	return h, nil
}

// at walks current names from the root.
func (k *fakeKernel) at(t *testing.T, path string) *fakeObject {
	t.Helper()
	sep := "/"
	if k.rootPath != "/" {
		sep = `\`
	}
	obj := k.root
	for _, part := range strings.Split(strings.TrimPrefix(path, k.rootPath), sep) {
		if obj = obj.entries[part]; obj == nil {
			t.Fatalf("fixture has no %q", path)
		}
	}
	return obj
}

// bind replaces the name of path in its current parent.
func (k *fakeKernel) bind(t *testing.T, path string, obj *fakeObject) {
	t.Helper()
	sep := "/"
	if k.rootPath != "/" {
		sep = `\`
	}
	cut := strings.LastIndex(path, sep)
	parent := k.root
	if dir := path[:cut]; dir+sep != k.rootPath {
		parent = k.at(t, dir)
	}
	parent.entries[path[cut+1:]] = obj
}

type fakeHandle struct {
	k      *fakeKernel
	obj    *fakeObject
	path   string
	offset int
	closed bool
	closes int
}

func (h *fakeHandle) inspect() (state, error) {
	if h.closed {
		return state{}, errFake
	}
	if err := h.k.inspectErr[h.path]; err != nil {
		return state{}, err
	}
	st := h.obj.st
	st.grants = append([]grant(nil), st.grants...)
	return st, nil
}

func (h *fakeHandle) resolve(name string) (objectID, error) {
	if err := h.k.resolveErr[h.path]; err != nil {
		return objectID{}, err
	}
	child := h.obj.entries[name]
	if h.closed || child == nil {
		return objectID{}, errFake
	}
	return child.st.id, nil
}

func (h *fakeHandle) Read(b []byte) (int, error) {
	k := h.k
	k.readCalls++
	k.bufs = append(k.bufs, b)
	if h.closed {
		return 0, errFake
	}
	if len(k.plan) > 0 {
		step := k.plan[0]
		k.plan = k.plan[1:]
		if step.over {
			return len(b) + 1, nil
		}
		n := copy(b[:min(step.n, len(b))], h.obj.data[h.offset:])
		h.offset += n
		return n, step.err
	}
	if h.offset >= len(h.obj.data) {
		if k.afterRead != nil {
			k.afterRead()
			k.afterRead = nil
		}
		return 0, io.EOF
	}
	n := copy(b[:min(len(b), 700)], h.obj.data[h.offset:])
	h.offset += n
	return n, nil
}

func (h *fakeHandle) close() error {
	h.closes++
	h.closed = true
	h.k.closed = append(h.k.closed, h.path)
	if h.k.afterClose != nil {
		h.k.afterClose(h.path)
	}
	return h.k.closeErr[h.path]
}

// allClosedOnce reports whether every opened handle was closed exactly once,
// in reverse order of opening.
func (k *fakeKernel) allClosedOnce(t *testing.T) {
	t.Helper()
	if len(k.closed) != len(k.handles) {
		t.Fatalf("closed %d of %d handles", len(k.closed), len(k.handles))
	}
	for i, h := range k.handles {
		if h.closes != 1 || k.closed[len(k.closed)-1-i] != h.path {
			t.Fatalf("handle %q closed %d times or out of order", h.path, h.closes)
		}
	}
}

// buffersWiped reports whether every buffer region the loader exposed to Read
// has been cleared again.
func (k *fakeKernel) buffersWiped(t *testing.T) {
	t.Helper()
	for _, b := range k.bufs {
		for _, c := range b[:cap(b)] {
			if c != 0 {
				t.Fatal("read buffer was not wiped")
			}
		}
	}
}

func (k *fakeKernel) factory(calls *int) func() (kernel, error) {
	return func() (kernel, error) {
		*calls++
		return k, nil
	}
}

const (
	darwinKeyPath  = "/Users/alice/.rescue/agent_ed25519"
	darwinUser     = "uid:501"
	windowsUser    = "S-1-5-21-1000000001-1000000002-1000000003-1001"
	windowsKeyPath = `C:\Users\ci-gw\rescue\agent_ed25519`
	sidUsers       = "S-1-5-32-545"
	sidAuthUsers   = "S-1-5-11"
	sidEveryone    = "S-1-1-0"
)

// darwinFixture: "/" on the system volume, the rest on a data volume, with a
// deny-only ACL on /Users and the home directory as on a default install.
func darwinFixture(key []byte) *fakeKernel {
	dir := func(dev, ino uint64, mode, uid uint32, acl bool) *fakeObject {
		raw := posixStat{dev: dev, ino: ino, nlink: 4, size: 128, mode: 0o040000 | mode, uid: uid, stamp: [4]int64{1, 2, 3, int64(ino)}}
		return (&fakeObject{posix: &raw, local: true, aclPresent: acl, entries: map[string]*fakeObject{}}).refresh()
	}
	root := dir(1, 2, 0o755, 0, false)
	users := dir(2, 20, 0o755, 0, true)
	home := dir(2, 30, 0o750, 501, true)
	rescue := dir(2, 40, 0o700, 501, false)
	raw := posixStat{dev: 2, ino: 50, nlink: 1, size: uint64(len(key)), mode: 0o100600, uid: 501, stamp: [4]int64{5, 6, 7, 8}}
	leaf := (&fakeObject{posix: &raw, local: true, data: key}).refresh()
	root.entries["Users"] = users
	users.entries["alice"] = home
	home.entries[".rescue"] = rescue
	rescue.entries["agent_ed25519"] = leaf
	return &fakeKernel{rootPath: "/", root: root, acct: account{user: darwinUser, control: []string{"uid:0"}}}
}

func allow(sid string, flags uint8, mask uint32) winACE {
	return winACE{typ: 0, flags: flags, mask: mask, sid: sid}
}

// windowsFixture follows the default Windows layout: the drive root lets
// Authenticated Users create folders (and inherit-only Modify), C:\Users is
// readable by Users and Everyone, and the profile is user/SYSTEM/Administrators.
func windowsFixture(key []byte) *fakeKernel {
	obj := func(path string, id byte, dir bool, owner string, aces ...winACE) *fakeObject {
		raw := &winStat{disk: true, fixed: true, attrs: 0x20, links: 1, volume: 0x5eed, finalPath: `\\?\` + path, filesystem: "NTFS", stamp: [4]int64{1, 2, 3, int64(id)}, owner: owner, aces: aces, daclPresent: true}
		if dir {
			raw.attrs = 0x10
		}
		raw.file[0] = id
		return (&fakeObject{win: raw, entries: map[string]*fakeObject{}}).refresh()
	}
	const inherited = 0x13 // INHERITED | OBJECT_INHERIT | CONTAINER_INHERIT
	profile := []winACE{allow(sidSystem, inherited, 0x1f01ff), allow(sidAdministrators, inherited, 0x1f01ff), allow(windowsUser, inherited, 0x1f01ff)}
	root := obj(`C:\`, 1, true, sidSystem,
		allow(sidAdministrators, 0x03, 0x1f01ff), allow(sidSystem, 0x03, 0x1f01ff),
		allow(sidUsers, 0x03, 0x1200a9),
		allow(sidAuthUsers, 0x0b, 0x1301bf), // inherit-only Modify: not effective on the root
		allow(sidAuthUsers, 0, 0x4))         // create folders on the root itself
	users := obj(`C:\Users`, 2, true, sidSystem,
		allow(sidSystem, 0x03, 0x1f01ff), allow(sidAdministrators, 0x03, 0x1f01ff),
		allow(sidUsers, 0, 0x1200a9), allow(sidUsers, 0x0b, 0xa0000000),
		allow(sidEveryone, 0, 0x1200a9))
	home := obj(`C:\Users\ci-gw`, 3, true, windowsUser, profile...)
	rescue := obj(`C:\Users\ci-gw\rescue`, 4, true, windowsUser, profile...)
	leaf := obj(windowsKeyPath, 5, false, windowsUser, allow(sidSystem, 0x10, 0x1f01ff), allow(sidAdministrators, 0x10, 0x1f01ff), allow(windowsUser, 0x10, 0x1f01ff))
	leaf.win.size, leaf.data = uint64(len(key)), key
	leaf.refresh()
	root.entries["Users"] = users
	users.entries["ci-gw"] = home
	home.entries["rescue"] = rescue
	rescue.entries["agent_ed25519"] = leaf
	return &fakeKernel{rootPath: `C:\`, root: root, acct: account{user: windowsUser, control: []string{sidSystem, sidAdministrators}}}
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}
