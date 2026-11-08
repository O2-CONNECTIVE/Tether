package fileread

import (
	"crypto/sha256"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// Atime is deliberately excluded: this reader can itself update it. These
// metadata observations detect many changes but do not authenticate bytes or
// create a stable snapshot. ACLs, same-filesystem aliases and change/restore
// races are not ruled out by UID/mode/device/FSID comparisons.
type nativeState struct {
	device                      int32
	inode                       uint64
	mode, links                 uint16
	uid, gid, flags, generation uint32
	size                        int64
	mtime, ctime, birthtime     unix.Timespec
	fsid                        unix.Fsid
}

func nativeStat(st unix.Stat_t, fsid unix.Fsid) nativeState {
	return nativeState{device: st.Dev, inode: st.Ino, mode: st.Mode, links: st.Nlink,
		uid: st.Uid, gid: st.Gid, flags: st.Flags, generation: st.Gen, size: st.Size,
		mtime: st.Mtim, ctime: st.Ctim, birthtime: st.Btim, fsid: fsid}
}

func inspectNative(fd int) (nativeState, error) {
	var st unix.Stat_t
	var fs unix.Statfs_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nativeState{}, err
	}
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return nativeState{}, err
	}
	return nativeStat(st, fs.Fsid), nil
}

func inspectName(parent int, name string) (nativeState, error) {
	var st unix.Stat_t
	var fs unix.Statfs_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nativeState{}, err
	}
	if err := unix.Fstatfs(parent, &fs); err != nil {
		return nativeState{}, err
	}
	// A name has no fd yet. Parent FSID is provisional; after opening, compare
	// the actual child's Fstatfs too. A crossed mount with another FSID fails.
	return nativeStat(st, fs.Fsid), nil
}

func (s nativeState) directory() bool {
	return s.mode&unix.S_IFMT == unix.S_IFDIR && s.mode&0022 == 0 && s.links > 0
}

func (s nativeState) safeChild(root nativeState, leaf bool) bool {
	if s.uid != root.uid || s.device != root.device || s.fsid != root.fsid {
		return false
	}
	if !leaf {
		return s.directory()
	}
	return s.mode&unix.S_IFMT == unix.S_IFREG && s.links == 1 && s.size >= 0
}

func (s nativeState) identity() [32]byte {
	return hashWords(uint64(s.device), s.inode, uint64(s.generation),
		uint64(s.birthtime.Sec), uint64(s.birthtime.Nsec), uint64(s.fsid.Val[0]), uint64(s.fsid.Val[1]))
}

func (s nativeState) observation() observation {
	kind := uint8(0)
	if s.mode&unix.S_IFMT == unix.S_IFREG {
		kind = regularFile
	}
	return observation{identity: s.identity(), size: s.size, kind: kind,
		version: hashWords(uint64(s.size), uint64(s.mode), uint64(s.links), uint64(s.uid), uint64(s.gid),
			uint64(s.flags), uint64(s.mtime.Sec), uint64(s.mtime.Nsec), uint64(s.ctime.Sec), uint64(s.ctime.Nsec))}
}

func hashWords(words ...uint64) [32]byte {
	h := sha256.New()
	var word [8]byte
	for _, value := range words {
		binary.LittleEndian.PutUint64(word[:], value)
		_, _ = h.Write(word[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}
