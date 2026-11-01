//go:build !darwin || !cgo

package main

// No wall-clock fallback: unsupported display clocks end only the status stream.
func statusContinuousMillis() (uint64, bool) { return 0, false }
