package agentkeys

import "testing"

// Linux evidence reaches the shared policy through posixState: local is true
// only for the ext4/tmpfs/overlayfs magic allowlist, and a stored POSIX ACL
// (Linux ACLs have no deny entries) arrives as aclPresent with every entry
// counted as an allow. These fake-kernel tests run on every GOOS and pin the
// policy outcome for such facts. native_linux_test.go (Linux only) covers the
// mapping from system-call results to these facts.

const (
	containerKeyPath    = "/home/gw/.rescue/agent_ed25519"
	containerConfigPath = "/home/gw/.rescue/gateway.json"
	containerUID        = 1000
)

// linuxFactsFixture: overlayfs root and home owned by root, the account's
// home on the same overlay, and a tmpfs key directory, as in a minimal
// container image. All synthetic.
func linuxFactsFixture(key []byte) *fakeKernel {
	dir := func(dev, ino uint64, mode, uid uint32) *fakeObject {
		raw := posixStat{dev: dev, ino: ino, nlink: 3, size: 4096, mode: 0o040000 | mode, uid: uid, stamp: [4]int64{1, 2, 3, int64(ino)}}
		return (&fakeObject{posix: &raw, local: true, entries: map[string]*fakeObject{}}).refresh()
	}
	root := dir(0x2f, 2, 0o755, 0)
	home := dir(0x2f, 3, 0o755, 0)
	gw := dir(0x2f, 4, 0o700, containerUID)
	rescue := dir(0x30, 2, 0o700, containerUID)
	raw := posixStat{dev: 0x30, ino: 3, nlink: 1, size: uint64(len(key)), mode: 0o100600, uid: containerUID, stamp: [4]int64{5, 6, 7, 8}}
	leaf := (&fakeObject{posix: &raw, local: true, data: key}).refresh()
	root.entries["home"] = home
	home.entries["gw"] = gw
	gw.entries[".rescue"] = rescue
	rescue.entries["agent_ed25519"] = leaf
	return &fakeKernel{rootPath: "/", root: root, acct: account{user: uidPrincipal(containerUID), control: []string{uidPrincipal(0)}}}
}

var linuxFacts = fixture{"linux facts", posixPaths, containerKeyPath, linuxFactsFixture}

func TestLinuxFactsAcceptContainerLayout(t *testing.T) {
	k := linuxFacts.build(syntheticKey(t, nil))
	if signer, err := linuxFacts.load(t, k); err != nil || signer == nil {
		t.Fatalf("Load = %v, %v", signer, err)
	}
	k.allClosedOnce(t)
	k.buffersWiped(t)
}

func TestLinuxFactsRefuseBeforeReading(t *testing.T) {
	edit := func(path string, change func(*fakeObject)) func(*testing.T, *fakeKernel) {
		return func(t *testing.T, k *fakeKernel) {
			o := k.at(t, path)
			change(o)
			o.refresh()
		}
	}
	posixACL := func(o *fakeObject) { o.aclPresent, o.aclAllow = true, 3 } // even a minimal three-entry ACL
	foreign := func(o *fakeObject) { o.local = false }
	cases := []struct {
		name  string
		apply func(*testing.T, *fakeKernel)
	}{
		{"POSIX ACL on key", edit(containerKeyPath, posixACL)},
		{"POSIX ACL on parent", edit("/home/gw/.rescue", posixACL)},
		{"POSIX ACL on ancestor", edit("/home", posixACL)},
		{"key filesystem outside the allowlist", edit(containerKeyPath, foreign)},
		{"parent filesystem outside the allowlist", edit("/home/gw/.rescue", foreign)},
		{"root filesystem outside the allowlist", func(t *testing.T, k *fakeKernel) { k.root.local = false; k.root.refresh() }},
		{"sticky world-writable ancestor", edit("/home/gw", func(o *fakeObject) { o.posix.mode, o.posix.uid = 0o041777, 0 })},
		{"parent owned by overflowuid (unmapped owner)", edit("/home/gw/.rescue", func(o *fakeObject) { o.posix.uid = 65534 })},
		{"group-readable key", edit(containerKeyPath, func(o *fakeObject) { o.posix.mode = 0o100640 })},
		{"group-writable parent", edit("/home/gw/.rescue", func(o *fakeObject) { o.posix.mode = 0o040770 })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := linuxFacts.build(syntheticKey(t, nil))
			c.apply(t, k)
			linuxFacts.refused(t, k, ErrDenied)
			if k.readCalls != 0 {
				t.Fatalf("read %d times before refusal", k.readCalls)
			}
		})
	}
}
