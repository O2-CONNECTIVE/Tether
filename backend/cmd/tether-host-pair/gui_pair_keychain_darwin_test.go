//go:build darwin && cgo

package main

func guiPairUsesRealKeychain() bool { return true }
