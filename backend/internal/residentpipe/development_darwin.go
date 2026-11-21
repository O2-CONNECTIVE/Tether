//go:build tether_development && darwin && cgo

package residentpipe

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostlifecycle"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

// Explicit development CLI only. No Swift launch gate, sealed producer or
// production enrollment is changed or presented as trusted by this composition.
// It has no lifetime of its own: the end of standby (#399), kept by the owner,
// is the time limit, and close, input EOF or a signal end the process.
func RunDevelopmentForeground() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop() // unknown cleanup retains this original signal owner
	return runDevelopmentInput(ctx)
}

func runDevelopmentInput(lifetime context.Context) error {
	pipes, err := capturePipes()
	if err != nil {
		return err
	}
	defer pipes.close()
	coord := hostlifecycle.New(CloseBudget)
	loadCtx, cancel := context.WithTimeout(lifetime, LoadBudget)
	var owner *residentmac.Standby
	err = coord.Start(loadCtx, hostlifecycle.Step{
		Name: "standby",
		Acquire: func(ctx context.Context) (hostlifecycle.Resource, error) {
			loaded, err := residentmac.LoadDevelopmentStandby(ctx)
			owner = loaded
			if loaded == nil {
				return nil, err
			}
			return loaded, err
		},
	})
	cancel()
	if err != nil {
		return err
	} // no invented startup-rejection receipt
	return runOwnedInput(lifetime, coord, owner, pipes, foregroundInput)
}
