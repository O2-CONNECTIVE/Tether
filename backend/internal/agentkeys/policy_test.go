package agentkeys

import (
	"reflect"
	"strings"
	"testing"
)

func TestPathStylesProduceRootToKeyNodes(t *testing.T) {
	cases := []struct {
		style pathStyle
		path  string
		want  []node
	}{
		{posixPaths, "/Users/a/k", []node{{"/", "", roleAncestor}, {"/Users", "Users", roleAncestor}, {"/Users/a", "a", roleParent}, {"/Users/a/k", "k", roleLeaf}}},
		{posixPaths, "/d/k", []node{{"/", "", roleAncestor}, {"/d", "d", roleParent}, {"/d/k", "k", roleLeaf}}},
		{drivePaths, `C:\Users\a\k`, []node{{`C:\`, "", roleAncestor}, {`C:\Users`, "Users", roleAncestor}, {`C:\Users\a`, "a", roleParent}, {`C:\Users\a\k`, "k", roleLeaf}}},
	}
	for _, c := range cases {
		got, ok := c.style.nodes(c.path)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("nodes(%q) = %v, %v", c.path, got, ok)
		}
	}
}

func TestPathStylesRefuseNoncanonicalPaths(t *testing.T) {
	posix := []string{
		"", "/", "/k", "relative/k", "./a/k", "/a//k", "/a/k/", "/a/./k", "/a/../k", "/a/k\x00x", "/a/k\nx", "/a/\x7f/k",
		"/" + strings.Repeat("a", 256) + "/k", "/" + strings.Repeat("a/", 32) + "k", "/" + strings.Repeat("a", 1100) + "/k",
		"/a/\xff/k", `C:\a\k`,
	}
	for _, p := range posix {
		if _, ok := posixPaths.nodes(p); ok {
			t.Errorf("posix path %q accepted", p)
		}
	}
	drive := []string{
		"", `C:\`, `C:\k`, `c:\a\k`, `C:a\k`, `C:/a/k`, `\\server\share\a\k`, `\\?\C:\a\k`, `C:\a\\k`, `C:\a\k\`,
		`C:\a\..\k`, `C:\a.\k`, `C:\a \k`, `C:\a\k:stream`, `C:\a\k*`, `C:\a/b\k`, `C:\CON\k`, `C:\a\nul.txt`, `C:\a\COM1`, `C:\a\lpt9.key`,
		"/Users/a/k", "C:\\a\\k\x01", `C:\a\COM¹`, `C:\a\com².json`, `C:\LPT³\k`, `C:\a\lpt¹`, `C:\a\COM0`,
	}
	for _, p := range drive {
		if _, ok := drivePaths.nodes(p); ok {
			t.Errorf("drive path %q accepted", p)
		}
	}
	for _, p := range []string{`C:\COMMON\LPT\k`, `C:\a\COM10`, `C:\a\lpt¹x`} {
		if _, ok := drivePaths.nodes(p); !ok {
			t.Errorf("drive path %q refused", p)
		}
	}
	if _, ok := pathStyle(0).nodes("/a/k"); ok {
		t.Error("unknown style accepted")
	}
}

func TestWindowsRightsExpandGenericMasks(t *testing.T) {
	cases := map[uint32]rights{
		0x00120089: rightRead,                 // FILE_GENERIC_READ
		0x001200a0: rightRead,                 // FILE_GENERIC_EXECUTE
		0x00120116: rightCreate | rightModify, // FILE_GENERIC_WRITE
		0x001f01ff: allRights,
		0x80000000: rightRead,
		0x40000000: rightCreate | rightModify,
		0x20000000: rightRead,
		0x10000000: allRights,
		0x00000004: rightCreate,
		0x00000040: rightReplace,
		0x00010000: rightReplace,
		0x00040000: rightControl,
		0x00080000: rightControl,
		0x00000100: rightModify,
		0x00120088: 0, // READ_CONTROL, SYNCHRONIZE, READ_ATTRIBUTES, READ_EA
	}
	for mask, want := range cases {
		if got := windowsRights(mask); got != want {
			t.Errorf("windowsRights(%#x) = %b, want %b", mask, got, want)
		}
	}
}

func TestWindowsGrantsSkipOnlyNoneffectiveEntries(t *testing.T) {
	owner := windowsUser
	grants, ok := windowsGrants(owner, []winACE{
		{typ: 1, mask: 0x1f01ff, sid: sidEveryone},                 // deny: never offsets an allow
		{typ: 0, flags: 0x0b, mask: 0x1f01ff, sid: sidEveryone},    // inherit-only: not effective here
		{typ: 0, flags: 0x10, mask: 0x1f01ff, sid: sidOwnerRights}, // owner rights → owner
		{typ: 0, mask: 0x120089, sid: sidUsers},
		{typ: 0, mask: 0x100000, sid: sidAuthUsers}, // SYNCHRONIZE only: no grant
	})
	want := []grant{{who: owner, rights: allRights}, {who: sidUsers, rights: rightRead}}
	if !ok || !reflect.DeepEqual(grants, want) {
		t.Fatalf("windowsGrants = %v, %v", grants, ok)
	}
	for name, ace := range map[string]winACE{
		"object ACE":             {typ: 5, mask: 0x1, sid: sidUsers},
		"callback ACE":           {typ: 9, mask: 0x1, sid: sidUsers},
		"audit flag":             {typ: 0, flags: 0x40, mask: 0x1, sid: sidUsers},
		"access system security": {typ: 0, mask: 0x01000000, sid: sidUsers},
		"maximum allowed":        {typ: 0, mask: 0x02000000, sid: sidUsers},
		"reserved bits":          {typ: 0, mask: 0x00200000, sid: sidUsers},
		"invalid SID":            {typ: 0, mask: 0x1, sid: "S-1-5-021"},
		"empty SID":              {typ: 0, mask: 0x1},
	} {
		if _, ok := windowsGrants(owner, []winACE{ace}); ok {
			t.Errorf("%s accepted", name)
		}
	}
	if _, ok := windowsGrants(owner, make([]winACE, maxACEs+1)); ok {
		t.Error("oversized DACL accepted")
	}
}

func TestPosixGrantsTreatDirectoryWriteAsReplace(t *testing.T) {
	cases := []struct {
		mode    uint32
		dir     bool
		want    []grant
		special bool
	}{
		{0o600, false, nil, false},
		{0o640, false, []grant{{"group", rightRead}}, false},
		{0o601, false, []grant{{"other", rightRead}}, false},
		{0o620, false, []grant{{"group", rightCreate | rightModify}}, false},
		{0o4600, false, nil, true},
		{0o1600, false, nil, true},
		{0o755, true, nil, false},
		{0o775, true, []grant{{"group", rightCreate | rightReplace}}, false},
		{0o1777, true, []grant{{"group", rightCreate | rightReplace}, {"other", rightCreate | rightReplace}}, false},
	}
	for _, c := range cases {
		grants, special := posixGrants(c.mode, c.dir)
		if !reflect.DeepEqual(grants, c.want) || special != c.special {
			t.Errorf("posixGrants(%o, %v) = %v, %v", c.mode, c.dir, grants, special)
		}
	}
}

func TestPosixStateMapsKindsAndACLEvidence(t *testing.T) {
	raw := posixStat{dev: 7, ino: 0x0102030405060708, nlink: 1, size: 10, mode: 0o100600, uid: 501}
	st := posixState(raw, true, false, 0)
	if st.kind != kindRegular || st.owner != "uid:501" || st.id.volume != 7 || st.id.file[0] != 0x08 || st.id.file[7] != 0x01 || st.unsupported || st.extended || len(st.grants) != 0 {
		t.Fatalf("regular file state = %+v", st)
	}
	for mode, want := range map[uint32]kind{0o040700: kindDirectory, 0o020600: kindOther, 0o060600: kindOther, 0o140600: kindOther} {
		raw.mode = mode
		if got := posixState(raw, true, false, 0).kind; got != want {
			t.Errorf("mode %o kind = %v", mode, got)
		}
	}
	raw.mode = 0o120777
	if !posixState(raw, true, false, 0).link {
		t.Error("symlink not marked")
	}
	raw.mode = 0o040700
	if st := posixState(raw, true, true, 0); !st.extended || len(st.grants) != 0 {
		t.Errorf("deny-only ACL = %+v", st)
	}
	if st := posixState(raw, true, true, 2); !reflect.DeepEqual(st.grants, []grant{{"acl", allRights}}) {
		t.Errorf("allow ACL grants = %v", st.grants)
	}
	for _, c := range []struct {
		present bool
		allow   int
		size    uint64
	}{{true, -1, 1}, {false, 1, 1}, {false, 0, 1 << 63}} {
		raw.size = c.size
		if !posixState(raw, true, c.present, c.allow).unsupported {
			t.Errorf("inconsistent evidence %+v accepted", c)
		}
	}
}

func TestWindowsStateRequiresNormalizedPathAndDACL(t *testing.T) {
	raw := winStat{disk: true, fixed: true, attrs: 0x20, links: 1, size: 10, volume: 1, finalPath: `\\?\C:\a\k`, filesystem: "ReFS", owner: windowsUser, daclPresent: true}
	raw.file[0] = 1
	if st := windowsState(raw); st.unsupported || !st.local || st.kind != kindRegular || st.path != `C:\a\k` {
		t.Fatalf("state = %+v", st)
	}
	for name, edit := range map[string]func(*winStat){
		"no DACL":      func(w *winStat) { w.daclPresent = false },
		"UNC path":     func(w *winStat) { w.finalPath = `\\server\share\k` },
		"bad owner":    func(w *winStat) { w.owner = "Administrators" },
		"huge size":    func(w *winStat) { w.size = 1 << 63 },
		"unknown ACE":  func(w *winStat) { w.aces = []winACE{{typ: 2, sid: sidSystem}} },
		"empty prefix": func(w *winStat) { w.finalPath = `\\?\` },
	} {
		w := raw
		edit(&w)
		if !windowsState(w).unsupported {
			t.Errorf("%s accepted", name)
		}
	}
	for name, edit := range map[string]func(*winStat){
		"not disk":  func(w *winStat) { w.disk = false },
		"not fixed": func(w *winStat) { w.fixed = false },
		"FAT":       func(w *winStat) { w.filesystem = "exFAT" },
	} {
		w := raw
		edit(&w)
		if windowsState(w).local {
			t.Errorf("%s treated as local", name)
		}
	}
	raw.attrs = 0x40
	if windowsState(raw).kind != kindOther {
		t.Error("device treated as file")
	}
}

func TestAccountSIDAcceptsOnlyAccountIdentities(t *testing.T) {
	for _, sid := range []string{windowsUser, "S-1-5-21-1-2-3-500"} {
		if !accountSID(sid) {
			t.Errorf("%s refused", sid)
		}
	}
	for _, sid := range []string{"", sidSystem, "S-1-5-19", "S-1-5-20", sidAdministrators, "S-1-5-80-1-2-3-4-5", "S-1-5-21-1-2-3", "S-1-5-21-1-2-3-4-5", "S-1-5-21-01-2-3-4", "S-1-5-21-1-2-3-4294967296"} {
		if accountSID(sid) {
			t.Errorf("%s accepted", sid)
		}
	}
}

func TestCheckNodeTrustsOnlyAccountAndControlPrincipals(t *testing.T) {
	acct := account{user: "uid:501", control: []string{"uid:0"}}
	leaf := node{path: "/a/k", name: "k", role: roleLeaf}
	base := posixState(posixStat{dev: 1, ino: 2, nlink: 1, size: 100, mode: 0o100600, uid: 501}, true, false, 0)
	if !checkNode(acct, posixPaths, leaf, base) {
		t.Fatal("safe key refused")
	}
	for name, edit := range map[string]func(*state){
		"zero identity":         func(s *state) { s.id = objectID{} },
		"group owner principal": func(s *state) { s.owner = "group" },
		"grant to other":        func(s *state) { s.grants = []grant{{"other", rightRead}} },
		"unknown right bit":     func(s *state) { s.grants = []grant{{"uid:501", 1 << 7}} },
		"path on posix":         func(s *state) { s.path = "/a/k" },
	} {
		st := base
		edit(&st)
		if checkNode(acct, posixPaths, leaf, st) {
			t.Errorf("%s accepted", name)
		}
	}
	if checkNode(acct, posixPaths, node{path: "/a/k", name: "k"}, base) {
		t.Error("unknown role accepted")
	}
	if (account{user: "uid:0", control: []string{"uid:0"}}).valid() || (account{user: "uid:501"}).valid() {
		t.Error("invalid account accepted")
	}
}
