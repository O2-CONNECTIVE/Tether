//go:build !darwin || !cgo

package main

// Unsupported builds cannot emit an authoritative policy-rejection frame.
func enrolled() int { return 1 }
