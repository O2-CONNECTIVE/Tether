//go:build darwin && cgo && ci_actual_producer

package main

import (
	"io"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentpipe"
)

// The exact unmodified CLI main already validated the foreground command.
// Only this fixture composition can read synthetic inherited bootstrap.
func foreground(_ io.Writer) int { return residentpipe.CIForegroundMain() }
