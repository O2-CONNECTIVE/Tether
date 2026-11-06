package residentmac

import (
	"context"
	"path"
	"strconv"
	"strings"
	"time"
)

// One current-account location, not discovery, enrollment or a trust witness.
// Only the native provider constructs these facts outside package tests.
type enrollmentAccount struct {
	uid, euid, gid, egid int
	recordUID, home      string
}

const enrolledPolicyRelative = "Library/Application Support/com.o2connective.rescue-host.shell/resident-policy.json"

func finiteEnrollment(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Now().Before(deadline)
}

func selectEnrolledPolicy(ctx context.Context, a enrollmentAccount) (string, error) {
	if !finiteEnrollment(ctx) || a.uid <= 0 || a.uid != a.euid || a.gid < 0 || a.gid != a.egid || a.recordUID != strconv.Itoa(a.uid) {
		return "", ErrStandbyUnavailable
	}
	if !path.IsAbs(a.home) || a.home == "/" || path.Clean(a.home) != a.home || strings.ContainsAny(a.home, "\x00\r\n") {
		return "", ErrStandbyUnavailable
	}
	return a.home + "/" + enrolledPolicyRelative, nil
}

// Source-only composition for the future sealed entrypoint. No ordinary caller
// invokes this. The selected location still needs every existing protected
// read, fixed-scope, key and digest check; selection alone proves no enrollment.
func loadEnrolledStandby(ctx context.Context) (*Standby, error) {
	selected, err := currentEnrolledPolicy(ctx)
	if err != nil {
		return nil, err
	}
	return LoadStandby(ctx, selected)
}
