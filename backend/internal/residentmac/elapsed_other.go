//go:build !darwin

package residentmac

import (
	"context"
	"time"
)

// elapsedNow is Go's monotonic clock off macOS, where the resident does not
// run and only tests read it.
func elapsedNow() time.Time { return time.Now() }

func startReconnectSignals(context.Context, residentConnector, func()) {}
