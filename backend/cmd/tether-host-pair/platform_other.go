//go:build !unix

package main

import "errors"

var errPairBusy = errors.New("pair busy")
var errPairUnsupported = errors.New("pair unsupported")

func lockPair(string) error { return errPairUnsupported }
func unlockPair()           {}
