//go:build tether_development && darwin && cgo

package residentpipe

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

// Explicit development CLI only. No Swift launch gate, sealed producer or
// production enrollment is changed or presented as trusted by this composition.
func RunDevelopmentForeground() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop() // unknown cleanup retains this original signal owner
	return runDevelopmentLifetime(ctx, runDevelopmentInput)
}

func runDevelopmentInput(lifetime context.Context) error {
	pipes, err := capturePipes()
	if err != nil {
		return err
	}
	defer pipes.close()
	loadCtx, cancel := context.WithTimeout(lifetime, LoadBudget)
	owner, err := residentmac.LoadDevelopmentStandby(loadCtx)
	cancel()
	if err != nil {
		return err
	} // no invented startup-rejection receipt
	return runOwnedInput(lifetime, owner, pipes, foregroundInput)
}
