package agentkeys

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"
)

type fixture struct {
	name  string
	style pathStyle
	path  string
	build func([]byte) *fakeKernel
}

var fixtures = []fixture{
	{"darwin", posixPaths, darwinKeyPath, darwinFixture},
	{"windows", drivePaths, windowsKeyPath, windowsFixture},
}

func (f fixture) load(t *testing.T, k *fakeKernel) (ssh.Signer, error) {
	t.Helper()
	calls := 0
	signer, err := load(deadline(t), f.style, k.factory(&calls), Config{Path: f.path, Fingerprint: testFingerprint})
	if calls != 1 {
		t.Fatalf("kernel created %d times", calls)
	}
	return signer, err
}

func (f fixture) refused(t *testing.T, k *fakeKernel, want error) {
	t.Helper()
	signer, err := f.load(t, k)
	if signer != nil || !errors.Is(err, want) {
		t.Fatalf("Load = %v, %v; want refusal %v", signer, err, want)
	}
	k.allClosedOnce(t)
	k.buffersWiped(t)
}

func TestLoadsCurrentAccountKeyAndWipesReadBuffer(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			k := f.build(syntheticKey(t, nil))
			signer, err := f.load(t, k)
			if err != nil || signer == nil {
				t.Fatalf("Load = %v, %v", signer, err)
			}
			if got := ssh.FingerprintSHA256(signer.PublicKey()); got != testFingerprint {
				t.Fatalf("fingerprint = %s", got)
			}
			message := []byte("agent key loader public boundary")
			sig, err := signer.Sign(rand.Reader, message)
			if err != nil || signer.PublicKey().Verify(message, sig) != nil {
				t.Fatal("loaded signer does not verify")
			}
			want := []string{k.rootPath}
			nodes, _ := f.style.nodes(f.path)
			for _, n := range nodes[1:] {
				want = append(want, n.path)
			}
			if !slices.Equal(k.opened, want) || k.enters != 1 || k.exits != 1 || k.readCalls < 2 {
				t.Fatalf("opened %v, enters %d, exits %d, reads %d", k.opened, k.enters, k.exits, k.readCalls)
			}
			k.allClosedOnce(t)
			k.buffersWiped(t) // the file bytes are cleared once the signer exists
		})
	}
}

func TestRefusesUntrustedOwnerBeforeReading(t *testing.T) {
	cases := []struct {
		fixture fixture
		path    string
		owner   string
	}{
		{fixtures[0], darwinKeyPath, "uid:502"},
		{fixtures[0], "/Users/alice", "uid:502"},
		{fixtures[0], "/Users", "uid:80"},
		{fixtures[1], windowsKeyPath, "S-1-5-21-1000000001-1000000002-1000000003-1002"},
		{fixtures[1], `C:\Users\ci-gw\rescue`, "S-1-5-19"}, // LocalService is not implicitly trusted
		// NT SERVICE\TrustedInstaller is not in the trusted set; a drive root
		// it owns is refused without native OS-root evidence (osroot_test.go).
		{fixtures[1], `C:\`, "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"},
	}
	for _, c := range cases {
		t.Run(c.fixture.name+c.path, func(t *testing.T) {
			k := c.fixture.build(syntheticKey(t, nil))
			obj := objectAt(t, k, c.path)
			if obj.posix != nil {
				var uid uint64
				for _, d := range c.owner[len("uid:"):] {
					uid = uid*10 + uint64(d-'0')
				}
				obj.posix.uid = uint32(uid)
			} else {
				obj.win.owner = c.owner
			}
			obj.refresh()
			c.fixture.refused(t, k, ErrDenied)
			if k.readCalls != 0 {
				t.Fatal("key data was read before the owner was refused")
			}
		})
	}
}

// objectAt resolves the root path too, which fakeKernel.at does not.
func objectAt(t *testing.T, k *fakeKernel, path string) *fakeObject {
	t.Helper()
	if path == k.rootPath {
		return k.root
	}
	return k.at(t, path)
}

func TestRefusesUnsafeProtection(t *testing.T) {
	darwin, windows := fixtures[0], fixtures[1]
	posix := func(path string, edit func(o *fakeObject)) func(*testing.T, *fakeKernel) {
		return func(t *testing.T, k *fakeKernel) { o := objectAt(t, k, path); edit(o); o.refresh() }
	}
	win := func(path string, edit func(w *winStat)) func(*testing.T, *fakeKernel) {
		return func(t *testing.T, k *fakeKernel) {
			o := objectAt(t, k, path)
			o.win.aces = append([]winACE(nil), o.win.aces...)
			edit(o.win)
			o.refresh()
		}
	}
	addACE := func(ace winACE) func(w *winStat) { return func(w *winStat) { w.aces = append(w.aces, ace) } }
	cases := []struct {
		name    string
		fixture fixture
		edit    func(*testing.T, *fakeKernel)
	}{
		{"darwin key group-readable", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.posix.mode = 0o100640 })},
		{"darwin key world-readable", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.posix.mode = 0o100604 })},
		{"darwin key with deny-only extended ACL", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.aclPresent = true })},
		{"darwin key with allow ACL", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.aclPresent, o.aclAllow = true, 1 })},
		{"darwin setuid key", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.posix.mode = 0o104600 })},
		{"darwin key is symlink", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.posix.mode = 0o120600 })},
		{"darwin key is fifo", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.posix.mode = 0o010600 })},
		{"darwin key hard-linked", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.posix.nlink = 2 })},
		{"darwin key on non-local volume", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.local = false })},
		{"darwin key ACL unreadable", darwin, posix(darwinKeyPath, func(o *fakeObject) { o.aclPresent, o.aclAllow = true, -1 })},
		{"darwin parent group-writable", darwin, posix("/Users/alice/.rescue", func(o *fakeObject) { o.posix.mode = 0o040770 })},
		{"darwin ancestor world-writable sticky", darwin, posix("/Users", func(o *fakeObject) { o.posix.mode = 0o041777 })},
		{"darwin ancestor allow ACL", darwin, posix("/Users/alice", func(o *fakeObject) { o.aclAllow = 1 })},
		{"darwin parent is symlink", darwin, posix("/Users/alice/.rescue", func(o *fakeObject) { o.posix.mode = 0o120700 })},
		{"darwin parent is a file", darwin, posix("/Users/alice/.rescue", func(o *fakeObject) { o.posix.mode = 0o100700 })},
		{"windows Users read key", windows, win(windowsKeyPath, addACE(allow(sidUsers, 0x10, 0x1200a9)))},
		{"windows Everyone generic read key", windows, win(windowsKeyPath, addACE(allow(sidEveryone, 0, 0x80000000)))},
		{"windows LocalService execute key", windows, win(windowsKeyPath, addACE(allow("S-1-5-19", 0, 0x20)))},
		{"windows create in parent", windows, win(`C:\Users\ci-gw\rescue`, addACE(allow(sidAuthUsers, 0, 0x4)))},
		{"windows delete child on root", windows, win(`C:\`, addACE(allow(sidAuthUsers, 0, 0x40)))},
		{"windows write DAC on ancestor", windows, win(`C:\Users`, addACE(allow(sidUsers, 0, 0x40000)))},
		{"windows generic write on ancestor", windows, win(`C:\Users\ci-gw`, addACE(allow(sidEveryone, 0, 0x40000000)))},
		{"windows maximum allowed bit", windows, win(windowsKeyPath, addACE(allow(sidSystem, 0, 0x02000000)))},
		{"windows callback ACE", windows, win(windowsKeyPath, addACE(winACE{typ: 9, mask: 0x1, sid: sidUsers}))},
		{"windows unknown ACE flag", windows, win(windowsKeyPath, addACE(winACE{flags: 0x40, mask: 0x1, sid: sidSystem}))},
		{"windows NULL DACL", windows, win(windowsKeyPath, func(w *winStat) { w.daclPresent, w.aces = false, nil })},
		{"windows key reparse point", windows, win(windowsKeyPath, func(w *winStat) { w.attrs |= 0x400 })},
		{"windows parent reparse point", windows, win(`C:\Users\ci-gw\rescue`, func(w *winStat) { w.attrs |= 0x400 })},
		{"windows key hard-linked", windows, win(windowsKeyPath, func(w *winStat) { w.links = 2 })},
		{"windows final path case differs", windows, win(windowsKeyPath, func(w *winStat) { w.finalPath = `\\?\C:\Users\ci-gw\rescue\AGENT_ed25519` })},
		{"windows final path without prefix", windows, win(windowsKeyPath, func(w *winStat) { w.finalPath = windowsKeyPath })},
		{"windows parent on another volume", windows, win(`C:\Users\ci-gw\rescue`, func(w *winStat) { w.volume = 0x0bad })},
		{"windows FAT volume", windows, win(windowsKeyPath, func(w *winStat) { w.filesystem = "FAT32" })},
		{"windows removable drive", windows, win(`C:\`, func(w *winStat) { w.fixed = false })},
		{"windows key is a directory", windows, win(windowsKeyPath, func(w *winStat) { w.attrs = 0x10 })},
		{"windows empty key", windows, win(windowsKeyPath, func(w *winStat) { w.size = 0 })},
		{"windows oversized key", windows, win(windowsKeyPath, func(w *winStat) { w.size = maxKeyBytes + 1 })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := c.fixture.build(syntheticKey(t, nil))
			c.edit(t, k)
			c.fixture.refused(t, k, ErrDenied)
			if k.readCalls != 0 {
				t.Fatal("key data was read from an unsafe path")
			}
		})
	}
}

func TestAcceptsProtectionThatGrantsNoUntrustedAccess(t *testing.T) {
	cases := []struct {
		name    string
		fixture fixture
		path    string
		ace     winACE
	}{
		{"deny on key", fixtures[1], windowsKeyPath, winACE{typ: 1, mask: 0x1f01ff, sid: sidEveryone}},
		{"inherit-only write on parent", fixtures[1], `C:\Users\ci-gw\rescue`, allow(sidUsers, 0x0b, 0x1301bf)},
		{"attribute read on key", fixtures[1], windowsKeyPath, allow(sidEveryone, 0, 0x120088)},
		{"create-only on higher ancestor", fixtures[1], `C:\Users\ci-gw`, allow(sidAuthUsers, 0, 0x6)},
		{"owner rights on key", fixtures[1], windowsKeyPath, allow(sidOwnerRights, 0, 0x1f01ff)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := c.fixture.build(syntheticKey(t, nil))
			o := objectAt(t, k, c.path)
			o.win.aces = append(append([]winACE(nil), o.win.aces...), c.ace)
			o.refresh()
			if signer, err := c.fixture.load(t, k); err != nil || signer == nil {
				t.Fatalf("Load = %v, %v", signer, err)
			}
			k.allClosedOnce(t)
		})
	}
}

func TestRefusesParentReplacement(t *testing.T) {
	cases := []struct {
		name    string
		fixture fixture
		edit    func(*testing.T, *fakeKernel)
		read    bool
	}{
		{"darwin home renamed and substituted after read", fixtures[0], func(t *testing.T, k *fakeKernel) {
			k.afterRead = func() { k.bind(t, "/Users/alice", k.at(t, "/Users/alice").substitute()) }
		}, true},
		{"darwin parent substituted before key opens", fixtures[0], func(t *testing.T, k *fakeKernel) {
			k.afterOpen = func(path string) {
				if path == darwinKeyPath {
					k.bind(t, "/Users/alice/.rescue", k.at(t, "/Users/alice/.rescue").substitute())
				}
			}
		}, false},
		{"windows profile substituted after read", fixtures[1], func(t *testing.T, k *fakeKernel) {
			k.afterRead = func() { k.bind(t, `C:\Users\ci-gw`, k.at(t, `C:\Users\ci-gw`).substitute()) }
		}, true},
		{"windows held parent renamed after read", fixtures[1], func(t *testing.T, k *fakeKernel) {
			k.afterRead = func() {
				o := k.at(t, `C:\Users\ci-gw\rescue`)
				o.win.finalPath = `\\?\C:\Users\ci-gw\moved`
				o.refresh()
			}
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := c.fixture.build(syntheticKey(t, nil))
			c.edit(t, k)
			c.fixture.refused(t, k, ErrDenied)
			if (k.readCalls != 0) != c.read {
				t.Fatalf("read calls = %d", k.readCalls)
			}
		})
	}
}

func TestSameHandleRevalidationDetectsKeySubstitution(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name+" substituted after read", func(t *testing.T) {
			k := f.build(syntheticKey(t, nil))
			// The held handle keeps reading the original object while the
			// name now points to an equally protected different file.
			k.afterRead = func() { k.bind(t, f.path, k.at(t, f.path).substitute()) }
			f.refused(t, k, ErrDenied)
		})
		t.Run(f.name+" substituted between open and inspection", func(t *testing.T) {
			k := f.build(syntheticKey(t, nil))
			k.afterOpen = func(path string) {
				if path == f.path {
					k.bind(t, f.path, k.at(t, f.path).substitute())
				}
			}
			f.refused(t, k, ErrDenied)
			if k.readCalls != 0 {
				t.Fatal("substituted key was read")
			}
		})
	}
}

func TestRefusesIdentityChangeDuringRead(t *testing.T) {
	darwin, windows := fixtures[0], fixtures[1]
	cases := []struct {
		name    string
		fixture fixture
		change  func(*testing.T, *fakeKernel)
	}{
		{"darwin key ctime", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, darwinKeyPath).posix.stamp[3]++ }},
		{"darwin key owner", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, darwinKeyPath).posix.uid = 0 }},
		{"darwin key relinked", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, darwinKeyPath).posix.nlink = 2 }},
		{"darwin key flags", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, darwinKeyPath).posix.flags = 0x8000 }},
		{"darwin parent gains ACL", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, "/Users/alice/.rescue").aclPresent = true }},
		{"windows key last write", windows, func(t *testing.T, k *fakeKernel) { k.at(t, windowsKeyPath).win.stamp[3]++ }},
		{"windows key size", windows, func(t *testing.T, k *fakeKernel) { k.at(t, windowsKeyPath).win.size++ }},
		{"windows parent ACL entry order", windows, func(t *testing.T, k *fakeKernel) {
			w := k.at(t, `C:\Users\ci-gw\rescue`).win
			w.aces = []winACE{w.aces[2], w.aces[1], w.aces[0]}
		}},
		// Directories ignore only their volatile stamps, size and link count;
		// a change between trusted owners or to trusted grants still refuses.
		{"darwin home owner", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, "/Users/alice").posix.uid = 0 }},
		{"darwin home flags", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, "/Users/alice").posix.flags = 0x8000 }},
		{"darwin home loses deny-only ACL", darwin, func(t *testing.T, k *fakeKernel) { k.at(t, "/Users/alice").aclPresent = false }},
		{"windows root owner", windows, func(t *testing.T, k *fakeKernel) { k.root.win.owner = sidAdministrators }},
		{"windows root creation time", windows, func(t *testing.T, k *fakeKernel) { k.root.win.stamp[1]++ }},
		{"windows root attributes", windows, func(t *testing.T, k *fakeKernel) { k.root.win.attrs |= 0x1 }},
		{"windows root gains trusted entry", windows, func(t *testing.T, k *fakeKernel) {
			w := k.root.win
			w.aces = append(append([]winACE(nil), w.aces...), allow(sidSystem, 0, 0x1f01ff))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := c.fixture.build(syntheticKey(t, nil))
			k.afterRead = func() {
				c.change(t, k)
				for _, h := range k.handles {
					h.obj.refresh()
				}
			}
			c.fixture.refused(t, k, ErrDenied)
		})
	}
}

func TestToleratesUnrelatedEntryChangesInHeldDirectories(t *testing.T) {
	darwin, windows := fixtures[0], fixtures[1]
	posixEntryAdded := func(path string) func(*testing.T, *fakeKernel) {
		return func(t *testing.T, k *fakeKernel) {
			p := k.at(t, path).posix
			p.stamp = [4]int64{p.stamp[0] + 9, p.stamp[1] + 1, p.stamp[2] + 9, p.stamp[3] + 1}
			p.size += 32
			p.nlink++
		}
	}
	cases := []struct {
		name    string
		fixture fixture
		change  func(*testing.T, *fakeKernel)
	}{
		{"darwin home stamps size and link count", darwin, posixEntryAdded("/Users/alice")},
		{"darwin key parent stamps size and link count", darwin, posixEntryAdded("/Users/alice/.rescue")},
		{"windows C root last write", windows, func(t *testing.T, k *fakeKernel) { k.root.win.stamp[2]++; k.root.win.stamp[3]++ }},
		{"windows key parent last write and link count", windows, func(t *testing.T, k *fakeKernel) {
			w := k.at(t, `C:\Users\ci-gw\rescue`).win
			w.stamp[3]++
			w.links++
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := c.fixture.build(syntheticKey(t, nil))
			k.afterRead = func() {
				c.change(t, k)
				for _, h := range k.handles {
					h.obj.refresh()
				}
			}
			signer, err := c.fixture.load(t, k)
			if err != nil || signer == nil || k.afterRead != nil {
				t.Fatalf("Load = %v, %v; change applied %v", signer, err, k.afterRead == nil)
			}
			k.allClosedOnce(t)
			k.buffersWiped(t)
		})
	}
}

func TestRefusesChangedOrUnconfirmedThreadIdentity(t *testing.T) {
	f := fixtures[0]
	nodes, _ := f.style.nodes(f.path)
	t.Run("enter", func(t *testing.T) {
		for _, k := range []*fakeKernel{
			func() *fakeKernel { k := f.build(syntheticKey(t, nil)); k.enterErr = errFake; return k }(),
			func() *fakeKernel { k := f.build(syntheticKey(t, nil)); k.acct.user = ""; return k }(),
			func() *fakeKernel { k := f.build(syntheticKey(t, nil)); k.acct.user = "uid:0"; return k }(),
			func() *fakeKernel { k := f.build(syntheticKey(t, nil)); k.acct.control = nil; return k }(),
		} {
			data, keep, err := readKey(deadline(t), k, f.style, nodes)
			if data != nil || !keep || err != ErrDenied || len(k.opened) != 0 {
				t.Fatalf("readKey = %d bytes, keep %v, %v, opened %v", len(data), keep, err, k.opened)
			}
		}
	})
	t.Run("exit", func(t *testing.T) {
		for _, k := range []*fakeKernel{
			func() *fakeKernel { k := f.build(syntheticKey(t, nil)); k.exitErr = errFake; return k }(),
			func() *fakeKernel {
				k := f.build(syntheticKey(t, nil))
				k.exitAcct = &account{user: "uid:502", control: []string{"uid:0"}}
				return k
			}(),
		} {
			data, keep, err := readKey(deadline(t), k, f.style, nodes)
			if data != nil || !keep || err != ErrDenied || k.readCalls == 0 {
				t.Fatalf("readKey = %d bytes, keep %v, %v", len(data), keep, err)
			}
			k.allClosedOnce(t)
			k.buffersWiped(t)
		}
	})
	t.Run("load", func(t *testing.T) {
		k := f.build(syntheticKey(t, nil))
		k.exitErr = errFake
		f.refused(t, k, ErrDenied)
	})
}

func TestPartialReadIsRefusedAndWiped(t *testing.T) {
	key := syntheticKey(t, nil)
	cases := []struct {
		name string
		plan []readStep
		data []byte
	}{
		{"error after partial data", []readStep{{n: 100}, {n: 0, err: errFake}}, nil},
		{"error with data", []readStep{{n: 100, err: errFake}}, nil},
		{"no progress", []readStep{{n: 100}, {n: 0}}, nil},
		{"over-reported count", []readStep{{over: true}}, nil},
		{"early EOF", []readStep{{n: 100}, {n: 0, err: io.EOF}}, nil},
		{"file grew after inspection", nil, append(append([]byte(nil), key...), '\n')},
		{"file shrank after inspection", nil, key[:len(key)-1]},
		{"file larger than the limit", nil, make([]byte, maxKeyBytes+10)},
	}
	for _, f := range fixtures {
		for _, c := range cases {
			t.Run(f.name+" "+c.name, func(t *testing.T) {
				k := f.build(key)
				k.plan = c.plan
				if c.data != nil {
					k.at(t, f.path).data = c.data
				}
				f.refused(t, k, ErrDenied)
				if k.readCalls == 0 {
					t.Fatal("read was not attempted")
				}
			})
		}
	}
}

func TestCleanupFailureWithholdsSigner(t *testing.T) {
	for _, f := range fixtures {
		nodes, _ := f.style.nodes(f.path)
		for _, n := range nodes {
			t.Run(f.name+" close "+n.path, func(t *testing.T) {
				k := f.build(syntheticKey(t, nil))
				k.closeErr = map[string]error{n.path: errFake}
				f.refused(t, k, ErrCleanup)
			})
		}
		t.Run(f.name+" close fails after refusal", func(t *testing.T) {
			k := f.build(syntheticKey(t, nil))
			k.plan = []readStep{{n: 10, err: errFake}}
			k.closeErr = map[string]error{nodes[1].path: errFake}
			f.refused(t, k, ErrCleanup)
		})
		t.Run(f.name+" lookup handle cleanup fails", func(t *testing.T) {
			k := f.build(syntheticKey(t, nil))
			k.resolveErr = map[string]error{nodes[2].path: ErrCleanup}
			f.refused(t, k, ErrCleanup)
		})
		t.Run(f.name+" open fails midway", func(t *testing.T) {
			k := f.build(syntheticKey(t, nil))
			k.openErr = map[string]error{nodes[3].path: errFake}
			f.refused(t, k, ErrDenied)
			if len(k.handles) != 3 || k.readCalls != 0 {
				t.Fatalf("handles %d, reads %d", len(k.handles), k.readCalls)
			}
		})
	}
}

func TestRefusesKeyContentAfterProtectedRead(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			k := f.build(append(syntheticKey(t, nil), "trailing\n"...))
			f.refused(t, k, ErrDenied)
		})
		t.Run(f.name+" fingerprint", func(t *testing.T) {
			k := f.build(syntheticKey(t, nil))
			calls := 0
			signer, err := load(deadline(t), f.style, k.factory(&calls), Config{Path: f.path, Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
			if signer != nil || err != ErrDenied {
				t.Fatalf("Load = %v, %v", signer, err)
			}
			k.allClosedOnce(t)
			k.buffersWiped(t)
		})
	}
}

func TestRequiresConfigurationAndDeadlineBeforeAnyIO(t *testing.T) {
	cancelled, cancel := context.WithCancel(deadline(t))
	cancel()
	good := Config{Path: darwinKeyPath, Fingerprint: testFingerprint}
	cases := []struct {
		name string
		ctx  context.Context
		cfg  Config
		want error
	}{
		{"nil context", nil, good, ErrConfig},
		{"no deadline", context.Background(), good, ErrConfig},
		{"cancelled", cancelled, good, ErrDenied},
		{"empty path", deadline(t), Config{Fingerprint: testFingerprint}, ErrConfig},
		{"relative path", deadline(t), Config{Path: "Users/alice/.rescue/agent_ed25519", Fingerprint: testFingerprint}, ErrConfig},
		{"dot-dot", deadline(t), Config{Path: "/Users/alice/../alice/.rescue/k", Fingerprint: testFingerprint}, ErrConfig},
		{"key directly under root", deadline(t), Config{Path: "/agent_ed25519", Fingerprint: testFingerprint}, ErrConfig},
		{"windows path on posix", deadline(t), Config{Path: windowsKeyPath, Fingerprint: testFingerprint}, ErrConfig},
		{"missing fingerprint", deadline(t), Config{Path: darwinKeyPath}, ErrConfig},
		{"MD5 fingerprint", deadline(t), Config{Path: darwinKeyPath, Fingerprint: "MD5:00:11"}, ErrConfig},
		{"padded fingerprint", deadline(t), Config{Path: darwinKeyPath, Fingerprint: testFingerprint + "="}, ErrConfig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := darwinFixture(syntheticKey(t, nil))
			calls := 0
			signer, err := load(c.ctx, posixPaths, k.factory(&calls), c.cfg)
			if signer != nil || err != c.want || calls != 0 || k.enters != 0 {
				t.Fatalf("Load = %v, %v; kernel calls %d", signer, err, calls)
			}
		})
	}
	t.Run("unsupported platform", func(t *testing.T) {
		signer, err := load(deadline(t), posixPaths, func() (kernel, error) { return nil, ErrUnsupported }, good)
		if signer != nil || err != ErrUnsupported {
			t.Fatalf("Load = %v, %v", signer, err)
		}
	})
	t.Run("public entry validates before native access", func(t *testing.T) {
		for _, cfg := range []Config{{}, {Path: "relative", Fingerprint: testFingerprint}, {Path: darwinKeyPath, Fingerprint: "SHA256:"}} {
			if signer, err := LoadCurrentUser(deadline(t), cfg); signer != nil || err != ErrConfig {
				t.Fatalf("LoadCurrentUser = %v, %v", signer, err)
			}
		}
		var none context.Context
		if signer, err := LoadCurrentUser(none, good); signer != nil || err != ErrConfig {
			t.Fatalf("LoadCurrentUser(nil) = %v, %v", signer, err)
		}
	})
}

func TestIdentityProbeCleanupIsPreserved(t *testing.T) {
	f := fixtures[0]
	nodes, _ := f.style.nodes(f.path)
	for _, phase := range []string{"enter", "exit"} {
		for _, wrapped := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "/direct", true: "/wrapped"}[wrapped], func(t *testing.T) {
				k := f.build(syntheticKey(t, nil))
				err := ErrCleanup
				if wrapped {
					err = errors.Join(errFake, ErrCleanup)
				}
				if phase == "enter" {
					k.enterErr = err
				} else {
					k.exitErr = err
				}
				data, keep, got := readKey(deadline(t), k, f.style, nodes)
				if data != nil || !keep || got != ErrCleanup {
					t.Fatalf("readKey = %d bytes, keep %v, %v", len(data), keep, got)
				}
				if phase == "enter" && len(k.opened) != 0 {
					t.Fatal("opened key path after identity cleanup failure")
				}
				k.allClosedOnce(t)
				k.buffersWiped(t)
			})
		}
	}
}
