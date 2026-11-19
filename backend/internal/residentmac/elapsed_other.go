//go:build !darwin

package residentmac

import "time"

// elapsedNow is Go's monotonic clock off macOS, where the resident does not
// run and only tests read it.
func elapsedNow() time.Time { return time.Now() }
