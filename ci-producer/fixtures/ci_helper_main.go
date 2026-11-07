//go:build darwin && cgo && ci_actual_producer

package main

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentpipe"
	"os"
)

func main() { os.Exit(residentpipe.CIProducerMain()) }
