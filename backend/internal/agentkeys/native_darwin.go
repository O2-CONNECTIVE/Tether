//go:build cgo

package agentkeys

/*
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <sys/acl.h>
#include <sys/param.h>
#include <sys/mount.h>
#include <sys/stat.h>

struct rh_stat {
	unsigned long long dev, ino, nlink, size;
	unsigned int mode, uid, flags;
	long long stamp[4];
};

static void rh_copy(const struct stat *st, struct rh_stat *out) {
	out->dev = (unsigned long long)(unsigned int)st->st_dev;
	out->ino = (unsigned long long)st->st_ino;
	out->nlink = (unsigned long long)st->st_nlink;
	out->size = st->st_size < 0 ? ~0ULL : (unsigned long long)st->st_size;
	out->mode = (unsigned int)st->st_mode;
	out->uid = (unsigned int)st->st_uid;
	out->flags = (unsigned int)st->st_flags;
	out->stamp[0] = (long long)st->st_mtimespec.tv_sec;
	out->stamp[1] = (long long)st->st_mtimespec.tv_nsec;
	out->stamp[2] = (long long)st->st_ctimespec.tv_sec;
	out->stamp[3] = (long long)st->st_ctimespec.tv_nsec;
}

static int rh_fstat(int fd, struct rh_stat *out) {
	struct stat st;
	if (fstat(fd, &st) != 0) return -1;
	rh_copy(&st, out);
	return 0;
}

static int rh_lookup(int dirfd, const char *name, struct rh_stat *out) {
	struct stat st;
	if (fstatat(dirfd, name, &st, AT_SYMLINK_NOFOLLOW) != 0) return -1;
	rh_copy(&st, out);
	return 0;
}

// 1: local APFS/HFS volume that enforces ownership; 0: anything else; -1: error.
static int rh_volume(int fd) {
	struct statfs fs;
	if (fstatfs(fd, &fs) != 0) return -1;
	if ((fs.f_flags & MNT_LOCAL) == 0 || (fs.f_flags & MNT_IGNORE_OWNERSHIP) != 0) return 0;
	if (strncmp(fs.f_fstypename, "apfs", MFSTYPENAMELEN) != 0 && strncmp(fs.f_fstypename, "hfs", MFSTYPENAMELEN) != 0) return 0;
	return 1;
}

// 0: no extended ACL; 1: an extended ACL exists and *allow counts its entries
// that are not ACL_EXTENDED_DENY; -1: the evidence could not be read.
static int rh_acl(int fd, int *allow) {
	*allow = 0;
	errno = 0;
	acl_t acl = acl_get_fd_np(fd, ACL_TYPE_EXTENDED);
	if (acl == NULL) return errno == ENOENT ? 0 : -1;
	int result = 1, count = 0;
	acl_entry_t entry = NULL;
	for (int id = ACL_FIRST_ENTRY;; id = ACL_NEXT_ENTRY) {
		errno = 0;
		if (acl_get_entry(acl, id, &entry) != 0) {
			if (errno != EINVAL) result = -1; // EINVAL marks the end of the list
			break;
		}
		acl_tag_t tag;
		if (entry == NULL || ++count > 128 || acl_get_tag_type(entry, &tag) != 0) {
			result = -1;
			break;
		}
		if (tag != ACL_EXTENDED_DENY) (*allow)++;
	}
	acl_free(acl);
	return result;
}
*/
import "C"

import (
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This file is compiled for darwin with cgo only. The agentkeys unit suite uses
// the fake kernel; the resident input bridge additionally reads only its own
// synthetic file fixture with these unchanged native protection checks.
// It only opens read-only, queries and closes. Unverified platform facts are
// listed in docs/agent-key-loader.md.

const nativeStyle = posixPaths

func nativeKernel() (kernel, error) { return darwinKernel{}, nil }

type darwinKernel struct{}

func (darwinKernel) enter() (account, error) { return darwinAccount() }
func (darwinKernel) exit() (account, error)  { return darwinAccount() }

// darwinAccount reads the calling thread's credentials. Root and set-id
// processes are refused: this loader is for the current non-root user only.
func darwinAccount() (account, error) {
	uid, euid, gid, egid := os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid()
	if uid <= 0 || uid != euid || gid < 0 || gid != egid {
		return account{}, ErrDenied
	}
	return account{user: uidPrincipal(uint32(uid)), control: []string{uidPrincipal(0)}}, nil
}

func (darwinKernel) open(parent handle, n node) (handle, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if n.role == roleLeaf {
		flags |= unix.O_NONBLOCK // a FIFO must not block; non-regular files are refused later
	} else {
		flags |= unix.O_DIRECTORY
	}
	var p *darwinFile
	if parent == nil {
		if n.path != "/" || n.name != "" {
			return nil, ErrDenied
		}
	} else {
		var ok bool
		if p, ok = parent.(*darwinFile); !ok || p.closed || n.name == "" {
			return nil, ErrDenied
		}
	}
	for range 8 {
		var fd int
		var err error
		if p == nil {
			fd, err = unix.Open("/", flags, 0)
		} else {
			fd, err = unix.Openat(p.fd, n.name, flags, 0)
		}
		if err == unix.EINTR {
			continue
		}
		if err != nil || fd < 0 {
			return nil, ErrDenied
		}
		return &darwinFile{fd: fd}, nil
	}
	return nil, ErrDenied
}

type darwinFile struct {
	fd     int
	closed bool
}

func (f *darwinFile) inspect() (state, error) {
	if f.closed {
		return state{}, ErrDenied
	}
	var st C.struct_rh_stat
	if C.rh_fstat(C.int(f.fd), &st) != 0 {
		return state{}, ErrDenied
	}
	volume := C.rh_volume(C.int(f.fd))
	var allow C.int
	acl := C.rh_acl(C.int(f.fd), &allow)
	if volume < 0 || acl < 0 {
		return state{}, ErrDenied
	}
	return posixState(rawStat(&st), volume == 1, acl == 1, int(allow)), nil
}

func (f *darwinFile) resolve(name string) (objectID, error) {
	if f.closed || name == "" {
		return objectID{}, ErrDenied
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var st C.struct_rh_stat
	if C.rh_lookup(C.int(f.fd), cname, &st) != 0 {
		return objectID{}, ErrDenied
	}
	return posixState(rawStat(&st), false, false, 0).id, nil
}

func rawStat(st *C.struct_rh_stat) posixStat {
	return posixStat{
		dev: uint64(st.dev), ino: uint64(st.ino), nlink: uint64(st.nlink), size: uint64(st.size),
		mode: uint32(st.mode), uid: uint32(st.uid), flags: uint32(st.flags),
		stamp: [4]int64{int64(st.stamp[0]), int64(st.stamp[1]), int64(st.stamp[2]), int64(st.stamp[3])},
	}
}

func (f *darwinFile) Read(b []byte) (int, error) {
	if f.closed {
		return 0, ErrDenied
	}
	for range 8 {
		n, err := unix.Read(f.fd, b)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n < 0 || n > len(b) {
			return 0, ErrDenied
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
	return 0, ErrDenied
}

// close is attempted once. A failed close(2) is not retried, because the
// descriptor number may already have been released and reused.
func (f *darwinFile) close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if unix.Close(f.fd) != nil {
		return ErrCleanup
	}
	return nil
}
