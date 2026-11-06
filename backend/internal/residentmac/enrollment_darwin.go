//go:build cgo && !osusergo

package residentmac

import (
	"context"
	"os"
	"os/user"
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
	// LookupId uses the OS account database, not HOME/UserConfigDir, and does
	// not use user.Current's first-call cache. This synchronous OS lookup is
	// not forcibly interrupted; recheck the deadline and identity afterwards.
	u, err := user.LookupId(strconv.Itoa(a.uid))
	if err != nil || u == nil || !finiteEnrollment(ctx) || a.uid != os.Getuid() || a.euid != os.Geteuid() || a.gid != os.Getgid() || a.egid != os.Getegid() {
		return "", ErrStandbyUnavailable
	}
	a.recordUID, a.home = u.Uid, u.HomeDir
	return selectEnrolledPolicy(ctx, a)
}
