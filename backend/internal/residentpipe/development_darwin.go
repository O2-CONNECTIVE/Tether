//go:build tether_development && darwin && cgo

package residentpipe

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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
	place := developmentRecordPlace(lifetime)
	coord, owner, record, err := startStandby(lifetime, place, func(ctx context.Context) (*residentmac.Standby, error) {
		return residentmac.LoadDevelopmentStandby(ctx)
	})
	if err != nil {
		return err
	} // no invented startup-rejection receipt
	return runOwnedInput(lifetime, coord, owner, record, pipes, foregroundInput)
}

func developmentRecordPlace(lifetime context.Context) recordPlace {
	place := recordPlace{bootSession: currentBootSession}
	ctx, cancel := context.WithTimeout(lifetime, LoadBudget)
	defer cancel()
	_, policyPath, err := residentmac.DevelopmentEnrolledLocation(ctx)
	if err == nil && policyPath != "" {
		place.dir = filepath.Dir(policyPath)
	}
	return place
}
