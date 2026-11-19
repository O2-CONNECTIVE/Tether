//go:build darwin

package residentmac

import (
	"time"

	"golang.org/x/sys/unix"
)

// elapsedNow reads the clock standby's length is counted on: the one that
// keeps counting while the Mac sleeps (CLOCK_MONOTONIC_RAW, the clock of
// mach_continuous_time). Go's own monotonic reading stops during sleep on
// macOS, so a wall clock set back before a sleep would stretch standby (#399).
// A clock that cannot be read reads as the far future: standby ends rather
// than outlives its length.
func elapsedNow() time.Time {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts) != nil {
		return time.Unix(1<<62, 0)
	}
	return time.Unix(ts.Unix())
}
