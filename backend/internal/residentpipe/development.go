//go:build tether_development

package residentpipe

import (
	"context"
	"time"
)

const developmentLifetime = 15 * time.Minute

// A new deadline belongs to the original invocation, not to an ON cycle or
// reconnect. The callback must retain and close its original owner on expiry.
func runDevelopmentLifetime(parent context.Context, run func(context.Context) error) error {
	if parent == nil || parent.Err() != nil || run == nil {
		return ErrProtocol
	}
	ctx, cancel := context.WithTimeout(parent, developmentLifetime)
	defer cancel()
	return run(ctx)
}
