//go:build !unix

package main

import "errors"

var errPairBusy = errors.New("pair busy")

func lockPair(string) error { return errors.New("unsupported") }
func unlockPair()           {}
