//go:build darwin && cgo

package residentpipe

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Source-only trusted composition, not a public policy-path launcher. Normal
// tether-host remains unavailable until sealed enrollment and image trust exist.
func runForeground(protectedPolicy string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop() // unknown retention keeps this same signal owner alive as well
	return runInput(ctx, protectedPolicy, foregroundInput)
}

// Fixed enrollment variant for a future trusted foreground composition. It is
// deliberately not registered in the ordinary CLI or selected by a flag.
func runEnrolledForeground() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	return runBootstrap(ctx, enrolledBootstrap, "", foregroundInput)
}
