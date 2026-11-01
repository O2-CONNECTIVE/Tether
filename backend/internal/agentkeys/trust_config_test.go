package agentkeys

import (
	"bytes"
	"context"
	"testing"
)

func TestTrustConfigUsesSameProtectedReadAndCleanup(t *testing.T) {
	payload := []byte("{\"principals\":[{\"subject\":\"synthetic-subject\"}]}\n")
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			k := f.build(payload)
			calls := 0
			got, err := readTrustConfig(deadline(t), f.style, k.factory(&calls), f.path)
			if err != nil || !bytes.Equal(got, payload) || calls != 1 {
				t.Fatalf("protected config err=%v", err)
			}
			k.allClosedOnce(t)
			wipe(got)
			k.buffersWiped(t)
		})
	}
}
func TestTrustConfigRejectsSubstitutionAndCleanupFailure(t *testing.T) {
	for _, f := range fixtures {
		for _, fault := range []string{"substitution", "cleanup", "owner"} {
			t.Run(f.name+"/"+fault, func(t *testing.T) {
				k := f.build([]byte("{}\n"))
				calls := 0
				want := ErrDenied
				switch fault {
				case "substitution":
					k.afterRead = func() { k.bind(t, f.path, k.at(t, f.path).substitute()) }
				case "cleanup":
					k.closeErr = map[string]error{f.path: errFake}
					want = ErrCleanup
				case "owner":
					o := k.at(t, f.path)
					if o.posix != nil {
						o.posix.uid = 502
					} else {
						o.win.owner = sidUsers
					}
					o.refresh()
				}
				got, err := readTrustConfig(deadline(t), f.style, k.factory(&calls), f.path)
				if got != nil || err != want {
					t.Fatalf("config returned data=%v err=%v", got != nil, err)
				}
				k.allClosedOnce(t)
				k.buffersWiped(t)
			})
		}
	}
}

// Public trust documents get no looser file policy than private keys.
func TestTrustConfigRequiresPrivateFileProtection(t *testing.T) {
	darwin, windows := fixtures[0], fixtures[1]
	addACE := func(ace winACE) func(*fakeObject) {
		return func(o *fakeObject) { o.win.aces = append(append([]winACE(nil), o.win.aces...), ace) }
	}
	cases := []struct {
		name    string
		fixture fixture
		edit    func(*fakeObject)
	}{
		{"darwin mode 0644", darwin, func(o *fakeObject) { o.posix.mode = 0o100644 }},
		{"darwin world writable", darwin, func(o *fakeObject) { o.posix.mode = 0o100602 }},
		{"darwin deny-only ACL", darwin, func(o *fakeObject) { o.aclPresent = true }},
		{"darwin allow ACL", darwin, func(o *fakeObject) { o.aclPresent, o.aclAllow = true, 1 }},
		{"windows Users read", windows, addACE(allow(sidUsers, 0x10, 0x1200a9))},
		{"windows Everyone write", windows, addACE(allow(sidEveryone, 0, 0x120116))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := c.fixture.build([]byte("{}\n"))
			o := k.at(t, c.fixture.path)
			c.edit(o)
			o.refresh()
			calls := 0
			got, err := readTrustConfig(deadline(t), c.fixture.style, k.factory(&calls), c.fixture.path)
			if got != nil || err != ErrDenied || k.readCalls != 0 {
				t.Fatalf("config returned data=%v err=%v reads=%d", got != nil, err, k.readCalls)
			}
			k.allClosedOnce(t)
			k.buffersWiped(t)
		})
	}
}

func TestTrustConfigWithholdsBytesAfterDeadline(t *testing.T) {
	for _, f := range fixtures {
		for _, phase := range []string{"after read", "while closing the leaf", "after the last close"} {
			t.Run(f.name+"/"+phase, func(t *testing.T) {
				k := f.build([]byte("{}\n"))
				ctx, cancel := context.WithCancel(deadline(t))
				defer cancel()
				switch phase {
				case "after read":
					k.afterRead = cancel
				case "while closing the leaf":
					k.afterClose = func(path string) {
						if path == f.path {
							cancel()
						}
					}
				case "after the last close":
					k.afterClose = func(path string) {
						if path == k.rootPath {
							cancel()
						}
					}
				}
				calls := 0
				got, err := readTrustConfig(ctx, f.style, k.factory(&calls), f.path)
				if got != nil || err != ErrDenied || k.readCalls == 0 {
					t.Fatalf("config returned data=%v err=%v reads=%d", got != nil, err, k.readCalls)
				}
				k.allClosedOnce(t)
				k.buffersWiped(t)
			})
		}
	}
}

func TestReservedDeviceNamesRefusedBeforeOpen(t *testing.T) {
	for _, p := range []string{`C:\Users\ci-gw\rescue\COM¹`, `C:\Users\ci-gw\rescue\com².json`, `C:\Users\LPT³\k`, `C:\Users\ci-gw\lpt¹\agent_ed25519`} {
		k := windowsFixture(syntheticKey(t, nil))
		calls := 0
		if got, err := readTrustConfig(deadline(t), drivePaths, k.factory(&calls), p); got != nil || err != ErrConfig || calls != 0 {
			t.Fatalf("config %q: err=%v kernel calls %d", p, err, calls)
		}
		if signer, err := load(deadline(t), drivePaths, k.factory(&calls), Config{Path: p, Fingerprint: testFingerprint}); signer != nil || err != ErrConfig || calls != 0 {
			t.Fatalf("key %q: err=%v kernel calls %d", p, err, calls)
		}
		if k.enters != 0 || len(k.opened) != 0 {
			t.Fatalf("%q reached the kernel", p)
		}
	}
}

func TestTrustConfigValidatesBeforeNativeAccess(t *testing.T) {
	for _, p := range []string{"", "relative"} {
		if b, err := ReadTrustConfig(deadline(t), p); b != nil || err != ErrConfig {
			t.Fatalf("bad path accepted: %v", err)
		}
	}
	if b, err := ReadTrustConfig(context.Background(), darwinKeyPath); b != nil || err != ErrConfig {
		t.Fatalf("missing deadline: %v", err)
	}
}
