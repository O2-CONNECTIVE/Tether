package agentkeys

import (
	"strconv"
	"strings"
)

// Pure mappings from raw native protection evidence to grants. They live in a
// common file so that fake-kernel tests exercise the same code as native
// builds; the native files only collect the raw evidence from open handles.

const (
	sidSystem         = "S-1-5-18"
	sidAdministrators = "S-1-5-32-544"
	sidOwnerRights    = "S-1-3-4"
	maxACEs           = 128

	// NT SERVICE\TrustedInstaller. It is never a trusted principal; it is
	// only an acceptable owner of the proven OS volume root (see checkNode).
	sidTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

// winACE is one DACL entry exactly as read from the handle's security
// descriptor.
type winACE struct {
	typ, flags uint8
	mask       uint32
	sid        string
}

// windowsGrants classifies an owner's DACL. Only ACCESS_ALLOWED (0) and
// ACCESS_DENIED (1) entries with the five standard inheritance flags are
// understood; object, callback, conditional or unknown entries, unknown mask
// bits, ACCESS_SYSTEM_SECURITY and MAXIMUM_ALLOWED make the evidence
// unsupported. Deny entries and inherit-only entries (not effective on this
// object) produce no grant; a deny never offsets an allow.
func windowsGrants(owner string, aces []winACE) ([]grant, bool) {
	if len(aces) > maxACEs {
		return nil, false
	}
	grants := make([]grant, 0, len(aces))
	for _, ace := range aces {
		if ace.typ > 1 || ace.flags&^0x1f != 0 || ace.mask&^0xf01f01ff != 0 || !validSID(ace.sid) {
			return nil, false
		}
		if ace.typ == 1 || ace.flags&0x08 != 0 {
			continue
		}
		who := ace.sid
		if who == sidOwnerRights { // OWNER RIGHTS is exactly the (checked) owner
			who = owner
		}
		if r := windowsRights(ace.mask); r != 0 {
			grants = append(grants, grant{who: who, rights: r})
		}
	}
	return grants, true
}

func windowsRights(mask uint32) rights {
	m := mask & 0x001f01ff
	for _, pair := range [...]struct{ generic, specific uint32 }{
		{0x80000000, 0x00120089}, // GENERIC_READ
		{0x40000000, 0x00120116}, // GENERIC_WRITE
		{0x20000000, 0x001200a0}, // GENERIC_EXECUTE
		{0x10000000, 0x001f01ff}, // GENERIC_ALL
	} {
		if mask&pair.generic != 0 {
			m |= pair.specific
		}
	}
	var r rights
	if m&(0x0001|0x0020) != 0 { // READ_DATA/LIST_DIRECTORY, EXECUTE/TRAVERSE
		r |= rightRead
	}
	if m&(0x0002|0x0004) != 0 { // WRITE_DATA/ADD_FILE, APPEND_DATA/ADD_SUBDIRECTORY
		r |= rightCreate
	}
	if m&(0x0010|0x0100) != 0 { // WRITE_EA, WRITE_ATTRIBUTES
		r |= rightModify
	}
	if m&(0x0040|0x10000) != 0 { // DELETE_CHILD, DELETE
		r |= rightReplace
	}
	if m&(0x40000|0x80000) != 0 { // WRITE_DAC, WRITE_OWNER
		r |= rightControl
	}
	// READ_EA 0x8, READ_ATTRIBUTES 0x80, READ_CONTROL 0x20000 and SYNCHRONIZE
	// 0x100000 neither read key data nor change the object.
	return r
}

// posixGrants classifies group and other permission bits. The owner is
// checked separately. No group is trusted, including wheel/admin/staff.
// Setuid, setgid or sticky bits on a file are unsupported for a key.
func posixGrants(mode uint32, directory bool) ([]grant, bool) {
	special := !directory && mode&0o7000 != 0
	var grants []grant
	for _, class := range [...]struct {
		who  string
		bits uint32
	}{{"group", mode >> 3 & 7}, {"other", mode & 7}} {
		var r rights
		if directory {
			if class.bits&2 != 0 { // add, rename and remove entries
				r |= rightCreate | rightReplace
			}
		} else {
			if class.bits&5 != 0 {
				r |= rightRead
			}
			if class.bits&2 != 0 {
				r |= rightCreate | rightModify
			}
		}
		if r != 0 {
			grants = append(grants, grant{who: class.who, rights: r})
		}
	}
	return grants, special
}

// winStat is the raw evidence of one open Windows handle.
type winStat struct {
	disk        bool // GetFileType == FILE_TYPE_DISK
	fixed       bool // the configured root is a DRIVE_FIXED volume
	attrs       uint32
	links       uint32
	size        uint64
	volume      uint64
	file        [16]byte
	finalPath   string // GetFinalPathNameByHandle, normalized DOS form
	filesystem  string
	stamp       [4]int64 // creation and last-write FILETIME halves
	owner       string
	aces        []winACE
	daclPresent bool // false for a NULL or missing DACL

	// Collected only for a held drive root owned by TrustedInstaller:
	volumePath string         // GetFinalPathNameByHandle(VOLUME_NAME_GUID) of this handle
	system     *winSystemRoot // nil when the OS directory evidence is unavailable
}

// winSystemRoot is the native evidence of where the running OS lives. api is
// GetSystemWindowsDirectory (an OS query, not %SystemRoot%, the caller or any
// configuration); the rest is read from a separate no-follow, attribute-only
// handle to that directory.
type winSystemRoot struct {
	api        string
	dosPath    string // GetFinalPathNameByHandle(VOLUME_NAME_DOS), `\\?\X:\...`
	volumePath string // GetFinalPathNameByHandle(VOLUME_NAME_GUID)
	volume     uint64 // FileIdInfo volume serial
	attrs      uint32
}

// osVolumeRoot reports whether raw is a held drive root proven to be the root
// of the volume that holds the running OS. The OS directory must be a real
// directory (not a reparse point) whose final DOS path is this handle's "X:\"
// plus a relative name matching api, whose volume GUID path is this handle's
// bare volume GUID root plus the same name, and whose volume serial equals
// this handle's. A path that merely ends in `\`, another volume, or missing
// evidence fails closed.
func osVolumeRoot(raw winStat) bool {
	sys := raw.system
	root, ok := strings.CutPrefix(raw.finalPath, `\\?\`)
	if sys == nil || !ok || len(root) != 3 || root[0] < 'A' || root[0] > 'Z' || root[1:] != `:\` || !volumeGUIDRoot(raw.volumePath) || raw.volume == 0 {
		return false
	}
	dir, ok := strings.CutPrefix(sys.dosPath, raw.finalPath)
	if !ok || dir == "" || strings.HasPrefix(dir, `\`) || strings.HasSuffix(dir, `\`) {
		return false
	}
	return sys.volumePath == raw.volumePath+dir && sys.volume == raw.volume &&
		sys.attrs&0x10 != 0 && sys.attrs&0x400 == 0 && strings.EqualFold(sys.api, root+dir)
}

// volumeGUIDRoot accepts exactly `\\?\Volume{xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx}\`.
func volumeGUIDRoot(s string) bool {
	guid, ok := strings.CutPrefix(s, `\\?\Volume{`)
	if !ok {
		return false
	}
	if guid, ok = strings.CutSuffix(guid, `}\`); !ok || len(guid) != 36 {
		return false
	}
	for i, c := range guid {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case '0' <= c && c <= '9', 'a' <= c && c <= 'f', 'A' <= c && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// windowsState maps Windows evidence. Only NTFS/ReFS on a fixed drive is a
// supported local volume; a NULL DACL (everyone, full control) is unsupported.
func windowsState(raw winStat) state {
	s := state{
		id:    objectID{volume: raw.volume, file: raw.file},
		local: raw.disk && raw.fixed && (raw.filesystem == "NTFS" || raw.filesystem == "ReFS"),
		links: uint64(raw.links), attrs: raw.attrs, stamp: raw.stamp, owner: raw.owner,
	}
	path, ok := strings.CutPrefix(raw.finalPath, `\\?\`)
	if !ok || path == "" || raw.size > 1<<63-1 || !raw.daclPresent || !validSID(raw.owner) {
		s.unsupported = true
		return s
	}
	s.path, s.size, s.osRoot = path, int64(raw.size), osVolumeRoot(raw)
	switch {
	case raw.attrs&0x400 != 0: // FILE_ATTRIBUTE_REPARSE_POINT
		s.link = true
	case raw.attrs&0x40 != 0: // FILE_ATTRIBUTE_DEVICE
	case raw.attrs&0x10 != 0: // FILE_ATTRIBUTE_DIRECTORY
		s.kind = kindDirectory
	default:
		s.kind = kindRegular
	}
	grants, ok := windowsGrants(raw.owner, raw.aces)
	s.grants, s.unsupported = grants, !ok
	return s
}

// posixStat is the raw fstat(2) evidence of one open descriptor.
type posixStat struct {
	dev, ino, nlink, size uint64
	mode, uid, flags      uint32
	stamp                 [4]int64 // mtime sec/nsec, ctime sec/nsec
}

// posixState maps Darwin evidence: fstat, whether the volume is local with
// enforced ownership, whether an extended ACL exists, and how many of its
// entries are not ACL_EXTENDED_DENY. Allow entries are not decoded further;
// each makes an untrusted all-rights grant, so only deny-only ACLs can pass on
// a directory and no ACL at all can pass on the key.
func posixState(raw posixStat, local, aclPresent bool, aclAllow int) state {
	s := state{local: local, links: raw.nlink, attrs: raw.flags, stamp: raw.stamp, owner: uidPrincipal(raw.uid), extended: aclPresent}
	s.id.volume = raw.dev
	for i := range 8 {
		s.id.file[i] = byte(raw.ino >> (8 * i))
	}
	if raw.size > 1<<63-1 || aclAllow < 0 || !aclPresent && aclAllow != 0 {
		s.unsupported = true
		return s
	}
	s.size = int64(raw.size)
	switch raw.mode & 0o170000 { // S_IFMT
	case 0o040000:
		s.kind = kindDirectory
	case 0o100000:
		s.kind = kindRegular
	case 0o120000:
		s.link = true
	}
	s.grants, s.special = posixGrants(raw.mode, s.kind == kindDirectory)
	if aclAllow > 0 {
		s.grants = append(s.grants, grant{who: "acl", rights: allRights})
	}
	return s
}

func uidPrincipal(uid uint32) string { return "uid:" + strconv.FormatUint(uint64(uid), 10) }

// accountSID accepts a local or domain account SID (S-1-5-21-a-b-c-rid), the
// only identity form for this per-user loader. Service, LocalSystem and
// well-known SIDs are refused.
func accountSID(s string) bool {
	if !strings.HasPrefix(s, "S-1-5-21-") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(s, "S-1-5-21-"), "-")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}

func validSID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 3 || len(parts) > 18 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for i, part := range parts[2:] {
		bits := 32
		if i == 0 {
			bits = 48
		}
		n, err := strconv.ParseUint(part, 10, bits)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}
