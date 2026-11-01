package agentkeys

const maxKeyBytes = 16 * 1024

// objectID is the platform's stable object identity: (st_dev, st_ino) on
// Darwin, (volume serial, 128-bit file ID) on Windows.
type objectID struct {
	volume uint64
	file   [16]byte
}

type kind uint8

const (
	kindOther kind = iota
	kindDirectory
	kindRegular
)

// rights are normalized from POSIX mode bits, Darwin ACL entries or Windows
// DACL masks. Anything a platform mapping cannot classify sets
// state.unsupported instead of being dropped.
type rights uint8

const (
	rightRead    rights = 1 << iota // file data read or execute/map
	rightCreate                     // add entries to a directory; append/write data to a file
	rightModify                     // write data, attributes or extended attributes
	rightReplace                    // delete or rename this object or entries in it
	rightControl                    // change owner or access control
	allRights    = rightRead | rightCreate | rightModify | rightReplace | rightControl
)

// grant is one effective allow that the object's protection gives a principal.
// Deny entries are never represented: this policy never lets a deny offset an
// unsafe allow.
type grant struct {
	who    string
	rights rights
}

// state is read from one open handle only, never from a path lookup.
type state struct {
	id          objectID
	path        string // final path reported by the handle (drivePaths only)
	kind        kind
	link        bool // symbolic link or reparse point
	local       bool // a local filesystem whose ownership and ACLs are enforced
	special     bool // setuid/setgid/sticky or other unsupported mode bits on a file
	links       uint64
	size        int64
	attrs       uint32   // platform flags/attributes, compared for stability
	stamp       [4]int64 // platform modification/change stamps, compared for stability
	owner       string
	grants      []grant
	extended    bool // an extended ACL exists (Darwin), even a deny-only one
	unsupported bool // protection evidence that could not be classified
	osRoot      bool // Windows: this handle is natively proven to be the running OS volume root
}

// account is the current process identity and the fixed OS control principals
// (root, or SYSTEM and BUILTIN\Administrators) that this platform trusts.
type account struct {
	user    string
	control []string
}

func (a account) valid() bool {
	if a.user == "" || len(a.control) == 0 {
		return false
	}
	for _, c := range a.control {
		if c == "" || c == a.user {
			return false
		}
	}
	return true
}

func (a account) trusts(who string) bool {
	if who == "" {
		return false
	}
	if who == a.user {
		return true
	}
	for _, c := range a.control {
		if who == c {
			return true
		}
	}
	return false
}

// checkNode applies the fixed protection policy to one held handle:
//   - every node is owned by a trusted principal (or, on the proven OS volume
//     root only, by TrustedInstaller: see osRootOwner) and is not a
//     link/reparse point on a supported local filesystem;
//   - the key is a regular single-link file of 1..16 KiB with no extended ACL
//     and no untrusted right of any kind (including read);
//   - its parent grants no untrusted principal any right to add, modify,
//     replace or re-protect entries;
//   - higher ancestors may let untrusted principals add new entries (e.g. the
//     default Windows drive root), but never rename, delete, modify or
//     re-protect existing ones.
func checkNode(a account, s pathStyle, n node, st state) bool {
	if st.unsupported || st.link || !st.local || st.id == (objectID{}) || !a.trusts(st.owner) && !osRootOwner(s, n, st) {
		return false
	}
	if s.finalPath() != (st.path != "") || s.finalPath() && st.path != n.path {
		return false
	}
	var forbidden rights
	switch n.role {
	case roleLeaf:
		if st.kind != kindRegular || st.special || st.extended || st.links != 1 || st.size <= 0 || st.size > maxKeyBytes {
			return false
		}
		forbidden = allRights
	case roleParent:
		forbidden = rightCreate | rightModify | rightReplace | rightControl
	case roleAncestor:
		forbidden = rightModify | rightReplace | rightControl
	default:
		return false
	}
	if n.role != roleLeaf && st.kind != kindDirectory {
		return false
	}
	for _, g := range st.grants {
		if g.rights&^allRights != 0 || !a.trusts(g.who) && g.rights&forbidden != 0 {
			return false
		}
	}
	return true
}

// osRootOwner is the only owner accepted outside the account's trusted set:
// exactly NT SERVICE\TrustedInstaller, on the drive root node only, when its
// held handle was natively proven to be the running OS volume root. Windows
// installs OS-protected resources under that owner. Its implicit owner rights
// (READ_CONTROL, WRITE_DAC) on that one root are accepted because it already
// owns and can replace the OS files that SYSTEM runs, so this adds no power
// beyond the trusted SYSTEM; a later DACL or owner change is still refused by
// revalidation.
// It is an ownership allowance only: TrustedInstaller ACEs (and OWNER RIGHTS
// mapped to it) stay untrusted grants, and no node below the root, other
// volume, service SID or LocalService gains anything from it.
func osRootOwner(s pathStyle, n node, st state) bool {
	return s == drivePaths && n.role == roleAncestor && n.name == "" && st.osRoot && st.owner == sidTrustedInstaller
}
