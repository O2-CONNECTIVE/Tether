//go:build cgo && !osusergo

package residentmac

/*
#include <errno.h>
#include <pwd.h>
#include <stdint.h>
#include <string.h>

// ACCOUNT_LOOKUP_CALLSITE
static int rh_enrollment_home(uid_t uid, char *out, size_t capacity) {
	char storage[65536];
	struct passwd record, *found = NULL;
	memset(&record, 0, sizeof(record));
	out[0] = '\0';
	int result = -1;
	if (getpwuid_r(uid, &record, storage, sizeof(storage), &found) != 0 ||
		found != &record || record.pw_uid != uid || record.pw_dir == NULL) goto done;
	uintptr_t start = (uintptr_t)storage, end = start + sizeof(storage);
	uintptr_t address = (uintptr_t)record.pw_dir;
	if (address < start || address >= end) goto done;
	const char *terminator = memchr(record.pw_dir, '\0', end - address);
	if (terminator == NULL) goto done;
	size_t length = (size_t)(terminator - record.pw_dir);
	if (length == 0 || length >= capacity) goto done;
	memcpy(out, record.pw_dir, length + 1);
	result = 0;
done:
	memset(storage, 0, sizeof(storage));
	return result;
}
*/
import "C"

import (
	"context"
	"os"
	"strconv"
)

func currentEnrolledPolicy(ctx context.Context) (string, error) {
	if !finiteEnrollment(ctx) {
		return "", ErrStandbyUnavailable
	}
	a := enrollmentAccount{uid: os.Getuid(), euid: os.Geteuid(), gid: os.Getgid(), egid: os.Getegid()}
	if a.uid <= 0 || a.uid != a.euid || a.gid < 0 || a.gid != a.egid {
		return "", ErrStandbyUnavailable
	}
	// Direct native lookup avoids os/user.LookupId's same-UID Current cache.
	// The OS directory service may have its own caching. Synchronous lookup is
	// not forcibly interrupted; recheck the deadline and identity afterwards.
	var home [1024]C.char
	if C.rh_enrollment_home(C.uid_t(a.uid), &home[0], C.size_t(len(home))) != 0 || !finiteEnrollment(ctx) || a.uid != os.Getuid() || a.euid != os.Geteuid() || a.gid != os.Getgid() || a.egid != os.Getegid() {
		return "", ErrStandbyUnavailable
	}
	a.recordUID, a.home = strconv.Itoa(a.uid), C.GoString(&home[0])
	return selectEnrolledPolicy(ctx, a)
}
