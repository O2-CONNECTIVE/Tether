//go:build tether_development && (!darwin || !cgo)

package main

func foreground() int { return 1 }
