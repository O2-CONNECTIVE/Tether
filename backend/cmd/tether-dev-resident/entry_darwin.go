//go:build tether_development && darwin && cgo

package main

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentpipe"

func foreground() int { return residentpipe.ExitCode(residentpipe.RunDevelopmentForeground()) }
