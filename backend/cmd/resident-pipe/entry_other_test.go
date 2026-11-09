//go:build !darwin || !cgo

package main

import "testing"

func TestUnsupportedEntryCannotClaimPolicyRejection(t *testing.T) {
	if execute(nil) != 1 {
		t.Fatal("unsupported startup promoted")
	}
}
