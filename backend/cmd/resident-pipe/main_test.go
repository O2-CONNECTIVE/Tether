package main

import "testing"

// This pure parser test never calls enrollment, even on Darwin/cgo.
func TestArgumentsAreClosed(t *testing.T) {
	for _, args := range [][]string{nil, {}} {
		if !validArguments(args) {
			t.Fatal("empty invocation rejected")
		}
	}
	for _, args := range [][]string{{""}, {"--help"}, {"--protected-policy", "/tmp/policy"}, {"--"}, {"foreground"}, {"--fd", "3"}} {
		if validArguments(args) || execute(args) != 1 {
			t.Fatal("argument accepted or promoted to policy rejection", args)
		}
	}
}
