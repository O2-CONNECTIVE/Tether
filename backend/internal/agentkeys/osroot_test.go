package agentkeys

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// A default Windows drive root may be owned by NT SERVICE\TrustedInstaller.
// These fake-kernel tests pin that exactly that owner is accepted only on the
// held root handle natively proven to be the running OS volume root, and that
// every other check (grants, lower nodes, revalidation, cleanup) is unchanged.
// The evidence values are synthetic; no native API is called.

const (
	osVolumeGUID    = `\\?\Volume{0123abcd-0000-4000-8000-00000000c0de}\`
	otherVolumeGUID = `\\?\Volume{0123abcd-0000-4000-8000-0000000d15c0}\`
	dKeyPath        = `D:\Users\ci-gw\rescue\agent_ed25519`
)

// osRootFixture is the default Windows layout whose C:\ is owned by
// TrustedInstaller and carries evidence that the OS directory reported by the
// OS is C:\Windows on the same volume as the held root handle.
func osRootFixture(key []byte) *fakeKernel {
	k := windowsFixture(key)
	w := k.root.win
	w.owner, w.volumePath = sidTrustedInstaller, osVolumeGUID
	w.system = &winSystemRoot{api: `C:\WINDOWS`, dosPath: `\\?\C:\Windows`, volumePath: osVolumeGUID + `Windows`, volume: w.volume, attrs: 0x10}
	k.root.refresh()
	return k
}

var (
	osRootWindows = fixture{"windows TrustedInstaller OS root", drivePaths, windowsKeyPath, osRootFixture}
	dDriveWindows = fixture{"windows D:", drivePaths, dKeyPath, nil}
)

func refreshAll(o *fakeObject) {
	o.refresh()
	for _, c := range o.entries {
		refreshAll(c)
	}
}

// onDrive moves every fixture object from C: to drive d, leaving the OS
// evidence untouched.
func onDrive(k *fakeKernel, d string) {
	var move func(o *fakeObject)
	move = func(o *fakeObject) {
		o.win.finalPath = `\\?\` + d + strings.TrimPrefix(o.win.finalPath, `\\?\C:`)
		for _, c := range o.entries {
			move(c)
		}
	}
	move(k.root)
	refreshAll(k.root)
	k.rootPath = d + `\`
}

// acceptedBoth loads a key and reads a trust document through the same
// protected path of freshly built layouts.
func acceptedBoth(t *testing.T, f fixture, build func([]byte) *fakeKernel) {
	t.Helper()
	k := build(syntheticKey(t, nil))
	signer, err := f.load(t, k)
	if err != nil || signer == nil || ssh.FingerprintSHA256(signer.PublicKey()) != testFingerprint {
		t.Fatalf("Load = %v, %v", signer, err)
	}
	k.allClosedOnce(t)
	k.buffersWiped(t)
	payload := []byte("{\"principals\":[]}\n")
	k = build(payload)
	calls := 0
	got, err := readTrustConfig(deadline(t), f.style, k.factory(&calls), f.path)
	if err != nil || !bytes.Equal(got, payload) || calls != 1 {
		t.Fatalf("ReadTrustConfig err=%v", err)
	}
	k.allClosedOnce(t)
	wipe(got)
	k.buffersWiped(t)
}

// refusedBoth requires that the key loader and ReadTrustConfig both refuse
// the same layout before reading any byte.
func refusedBoth(t *testing.T, f fixture, build func([]byte) *fakeKernel, want error) {
	t.Helper()
	k := build(syntheticKey(t, nil))
	f.refused(t, k, want)
	if k.readCalls != 0 {
		t.Fatal("key bytes were read")
	}
	k = build([]byte("{}\n"))
	calls := 0
	got, err := readTrustConfig(deadline(t), f.style, k.factory(&calls), f.path)
	if got != nil || err != want || calls != 1 || k.readCalls != 0 {
		t.Fatalf("config returned data=%v err=%v reads=%d", got != nil, err, k.readCalls)
	}
	k.allClosedOnce(t)
	k.buffersWiped(t)
}

func TestAcceptsTrustedInstallerOwnedOSVolumeRoot(t *testing.T) {
	if !osRootFixture(nil).root.st.osRoot {
		t.Fatal("fixture root lacks OS-root proof")
	}
	acceptedBoth(t, osRootWindows, osRootFixture)
}

func TestTrustedInstallerOwnerNeedsExactSIDAndOSRootProof(t *testing.T) {
	const ti = sidTrustedInstaller
	owner := func(sid string) func(*testing.T, *fakeKernel) {
		return func(_ *testing.T, k *fakeKernel) { k.root.win.owner = sid }
	}
	system := func(edit func(*winSystemRoot)) func(*testing.T, *fakeKernel) {
		return func(_ *testing.T, k *fakeKernel) {
			s := *k.root.win.system
			edit(&s)
			k.root.win.system = &s
		}
	}
	rootACE := func(ace winACE) func(*testing.T, *fakeKernel) {
		return func(_ *testing.T, k *fakeKernel) {
			w := k.root.win
			w.aces = append(append([]winACE(nil), w.aces...), ace)
		}
	}
	// below gives a lower node the same owner and the root's evidence while
	// the root itself stays a valid TrustedInstaller-owned OS root.
	below := func(path string) func(*testing.T, *fakeKernel) {
		return func(t *testing.T, k *fakeKernel) {
			w := k.at(t, path).win
			w.owner, w.volumePath, w.system = ti, k.root.win.volumePath, k.root.win.system
		}
	}
	guid := func(g string) func(*testing.T, *fakeKernel) {
		return func(_ *testing.T, k *fakeKernel) {
			s := *k.root.win.system
			s.volumePath = g + `Windows`
			k.root.win.volumePath, k.root.win.system = g, &s
		}
	}
	cases := []struct {
		name string
		edit func(*testing.T, *fakeKernel)
	}{
		{"no OS directory evidence", func(_ *testing.T, k *fakeKernel) { k.root.win.system = nil }},
		{"lookalike SID last sub-authority", owner(strings.TrimSuffix(ti, "4") + "5")},
		{"lookalike SID zero-padded", owner(strings.Replace(ti, "-956008885-", "-0956008885-", 1))},
		{"lookalike SID extra sub-authority", owner(ti + "-1")},
		{"NT SERVICE ALL SERVICES", owner("S-1-5-80-0")},
		{"other service SID", owner("S-1-5-80-1-2-3-4-5")},
		{"LocalService", owner("S-1-5-19")},
		{"name instead of SID", owner(`NT SERVICE\TrustedInstaller`)},
		{"foreign user", owner("S-1-5-21-1000000001-1000000002-1000000003-1002")},
		{"OS directory on another volume GUID", system(func(s *winSystemRoot) { s.volumePath = otherVolumeGUID + `Windows` })},
		{"OS directory volume serial differs", system(func(s *winSystemRoot) { s.volume = 0xc0de })},
		{"OS directory on another drive", system(func(s *winSystemRoot) { s.api, s.dosPath = `D:\WINDOWS`, `\\?\D:\Windows` })},
		{"API names another drive", system(func(s *winSystemRoot) { s.api = `D:\Windows` })},
		{"API names a UNC path", system(func(s *winSystemRoot) { s.api = `\\server\share\Windows` })},
		{"API and final path name differ", system(func(s *winSystemRoot) { s.api = `C:\Win` })},
		{"OS directory is a reparse point", system(func(s *winSystemRoot) { s.attrs |= 0x400 })},
		{"OS directory is a file", system(func(s *winSystemRoot) { s.attrs = 0x20 })},
		{"OS directory is the drive root", system(func(s *winSystemRoot) { s.api, s.dosPath, s.volumePath = `C:\`, `\\?\C:\`, osVolumeGUID })},
		{"root handle on another volume GUID", func(_ *testing.T, k *fakeKernel) { k.root.win.volumePath = otherVolumeGUID }},
		{"no root volume GUID path", func(_ *testing.T, k *fakeKernel) { k.root.win.volumePath = "" }},
		{"root volume GUID path below the volume root", guid(osVolumeGUID + `Users\`)},
		{"malformed volume GUID", guid(`\\?\Volume{0123abcd-0000-4000-8000-00000000c0dg}\`)},
		{"root final path is not the drive root", func(_ *testing.T, k *fakeKernel) { k.root.win.finalPath = `\\?\C:\Users` }},
		{"root is a reparse point", func(_ *testing.T, k *fakeKernel) { k.root.win.attrs |= 0x400 }},
		{"root on a removable drive", func(_ *testing.T, k *fakeKernel) { k.root.win.fixed = false }},
		// Ownership is allowed, grants are not: the ACL stays untrusted.
		{"TrustedInstaller full control entry", rootACE(allow(ti, 0, 0x1f01ff))},
		{"OWNER RIGHTS full control entry", rootACE(allow(sidOwnerRights, 0, 0x1f01ff))},
		{"foreign delete child on root", rootACE(allow(sidAuthUsers, 0, 0x40))},
		{"foreign WRITE_DAC on root", rootACE(allow(sidUsers, 0, 0x40000))},
		{"LocalService WRITE_OWNER on root", rootACE(allow("S-1-5-19", 0, 0x80000))},
		// The allowance does not reach any node below the root.
		{"same owner on non-root ancestor", below(`C:\Users`)},
		{"same owner on profile ancestor", below(`C:\Users\ci-gw`)},
		{"same owner on key parent", below(`C:\Users\ci-gw\rescue`)},
		{"same owner on key", below(windowsKeyPath)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refusedBoth(t, osRootWindows, func(data []byte) *fakeKernel {
				k := osRootFixture(data)
				c.edit(t, k)
				refreshAll(k.root)
				return k
			}, ErrDenied)
		})
	}
}

// The allowance follows the OS volume reported by the OS, not the letter C:
// and not a root path that merely ends in a backslash.
func TestTrustedInstallerRootOnlyOnTheOSVolume(t *testing.T) {
	cases := []struct {
		name     string
		edit     func(*winStat)
		accepted bool
	}{
		{"OS on C: with its own volume", func(w *winStat) { w.volumePath, w.system.volume = otherVolumeGUID, 0xc0de }, false},
		{"C: evidence with D: volume identity", func(*winStat) {}, false},
		{"OS directory on D:", func(w *winStat) { w.system.api, w.system.dosPath = `D:\WINDOWS`, `\\?\D:\Windows` }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			build := func(data []byte) *fakeKernel {
				k := osRootFixture(data)
				onDrive(k, "D:")
				c.edit(k.root.win)
				k.root.refresh()
				return k
			}
			if c.accepted {
				acceptedBoth(t, dDriveWindows, build)
			} else {
				refusedBoth(t, dDriveWindows, build, ErrDenied)
			}
		})
	}
}

func TestOSRootEvidenceIsRevalidatedOnTheHeldHandle(t *testing.T) {
	cases := []struct {
		name     string
		change   func(*fakeKernel)
		accepted bool
	}{
		{"owner TrustedInstaller to SYSTEM", func(k *fakeKernel) { k.root.win.owner = sidSystem }, false},
		{"OS evidence lost", func(k *fakeKernel) { k.root.win.system = nil }, false},
		{"OS directory on another volume", func(k *fakeKernel) {
			s := *k.root.win.system
			s.volumePath = otherVolumeGUID + `Windows`
			k.root.win.system = &s
		}, false},
		{"root volume GUID changes", func(k *fakeKernel) { k.root.win.volumePath = otherVolumeGUID }, false},
		{"paired GUID changes reprove same held root", func(k *fakeKernel) {
			w := k.root.win
			proof := *w.system
			w.volumePath = otherVolumeGUID
			proof.volumePath = otherVolumeGUID + `Windows`
			w.system = &proof
		}, true},
		{"root identity swapped", func(k *fakeKernel) { k.root.win.file[15] ^= 0x80 }, false},
		{"root gains TrustedInstaller entry", func(k *fakeKernel) {
			w := k.root.win
			w.aces = append(append([]winACE(nil), w.aces...), allow(sidTrustedInstaller, 0, 0x1f01ff))
		}, false},
		{"root gains trusted entry", func(k *fakeKernel) {
			w := k.root.win
			w.aces = append(append([]winACE(nil), w.aces...), allow(sidSystem, 0, 0x1f01ff))
		}, false},
		{"root last write only", func(k *fakeKernel) { k.root.win.stamp[2]++; k.root.win.stamp[3]++ }, true},
	}
	for _, c := range cases {
		for _, phase := range []string{"before read", "after read"} {
			t.Run(c.name+"/"+phase, func(t *testing.T) {
				k := osRootFixture(syntheticKey(t, nil))
				apply := func() {
					c.change(k)
					for _, h := range k.handles {
						h.obj.refresh()
					}
				}
				if phase == "before read" {
					k.afterOpen = func(path string) {
						if path == windowsKeyPath {
							apply()
						}
					}
				} else {
					k.afterRead = apply
				}
				if c.accepted {
					if signer, err := osRootWindows.load(t, k); err != nil || signer == nil {
						t.Fatalf("Load = %v, %v", signer, err)
					}
					k.allClosedOnce(t)
					k.buffersWiped(t)
					return
				}
				osRootWindows.refused(t, k, ErrDenied)
				if phase == "before read" && k.readCalls != 0 {
					t.Fatal("key bytes were read after the root changed")
				}
			})
		}
	}
}

func TestOSRootProbeFailureIsNotHidden(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want error
	}{{"probe close unconfirmed", ErrCleanup, ErrCleanup}, {"probe failed", errFake, ErrDenied}} {
		t.Run(c.name, func(t *testing.T) {
			refusedBoth(t, osRootWindows, func(data []byte) *fakeKernel {
				k := osRootFixture(data)
				k.inspectErr = map[string]error{`C:\`: c.err}
				return k
			}, c.want)
		})
		t.Run(c.name+" on revalidation", func(t *testing.T) {
			k := osRootFixture(syntheticKey(t, nil))
			k.afterRead = func() { k.inspectErr = map[string]error{`C:\`: c.err} }
			osRootWindows.refused(t, k, c.want)
		})
	}
}

func TestOSRootOwnerIsOnlyTheRootNodeAllowance(t *testing.T) {
	acct := windowsFixture(nil).acct
	if acct.trusts(sidTrustedInstaller) {
		t.Fatal("TrustedInstaller became a trusted principal")
	}
	st := osRootFixture(nil).root.st
	root := node{path: `C:\`, role: roleAncestor}
	if !st.osRoot || !checkNode(acct, drivePaths, root, st) {
		t.Fatal("proven OS root refused")
	}
	bare := st
	bare.grants = nil // isolate the owner decision from the root's ACL
	for _, n := range []node{
		{path: `C:\`, name: "Users", role: roleAncestor},
		{path: `C:\`, role: roleParent},
		{path: `C:\`, role: roleLeaf},
	} {
		if checkNode(acct, drivePaths, n, bare) {
			t.Errorf("%+v accepted", n)
		}
	}
	if checkNode(acct, posixPaths, root, bare) {
		t.Error("posix style accepted")
	}
	for _, c := range []struct {
		name string
		edit func(*state)
	}{
		{"no proof", func(s *state) { s.osRoot = false }},
		{"all services SID", func(s *state) { s.owner = "S-1-5-80-0" }},
		{"Users owner", func(s *state) { s.owner = sidUsers }},
		{"TrustedInstaller ACE", func(s *state) {
			s.grants = append(append([]grant(nil), s.grants...), grant{sidTrustedInstaller, rightControl})
		}},
	} {
		s := st
		c.edit(&s)
		if checkNode(acct, drivePaths, root, s) {
			t.Errorf("%s accepted", c.name)
		}
	}
}

func TestOSVolumeRootMappingAndGUIDShape(t *testing.T) {
	base := *osRootFixture(nil).root.win
	if !osVolumeRoot(base) {
		t.Fatal("base evidence refused")
	}
	nonRoot := base
	nonRoot.finalPath = `\\?\C:\Users`
	if osVolumeRoot(nonRoot) || windowsState(nonRoot).osRoot {
		t.Error("non-root handle proven as OS root")
	}
	lower := base
	system := *base.system
	system.dosPath = `\\?\c:\Windows`
	lower.finalPath, lower.system = `\\?\c:\`, &system
	if osVolumeRoot(lower) {
		t.Error("lower-case drive accepted")
	}
	zero := base
	system = *base.system
	system.volume = 0
	zero.volume, zero.system = 0, &system
	if osVolumeRoot(zero) {
		t.Error("zero volume serial accepted")
	}
	for _, g := range []string{osVolumeGUID, `\\?\Volume{0123ABCD-0000-4000-8000-00000000C0DE}\`} {
		if !volumeGUIDRoot(g) {
			t.Errorf("%s refused", g)
		}
	}
	for _, g := range []string{
		"", `\\?\Volume{0123abcd-0000-4000-8000-00000000c0de}`, osVolumeGUID + `x\`, `\\?\volume{0123abcd-0000-4000-8000-00000000c0de}\`,
		`\\.\Volume{0123abcd-0000-4000-8000-00000000c0de}\`, `\\?\Volume{0123abcd0000-4000-8000-00000000c0de-}\`,
		`\\?\Volume{0123abcd-0000-4000-8000-00000000c0d}\`, `\\?\Volume{0123abcd-0000-4000-8000-00000000c0dé}\`, `C:\`,
	} {
		if volumeGUIDRoot(g) {
			t.Errorf("%q accepted", g)
		}
	}
}
