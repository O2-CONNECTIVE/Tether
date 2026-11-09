//go:build darwin && cgo

package main

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentpipe"

func enrolled() int { return residentpipe.ExitCode(residentpipe.RunEnrolled()) }
