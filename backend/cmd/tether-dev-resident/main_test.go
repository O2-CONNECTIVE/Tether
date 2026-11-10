//go:build tether_development

package main

import (
	"bytes"
	"io"
	"testing"
)

func TestArgumentScope(t *testing.T) {
	for _, args := range [][]string{nil, {"on"}, {"foreground", "--policy", "x"}, {"foreground", "--duration", "3600"}, {"foreground", "--trust"}, {"--help", "extra"}} {
		called := false
		if got := execute(args, io.Discard, io.Discard, func() int { called = true; return 0 }); got != 2 || called {
			t.Fatalf("invalid arguments admitted: %v", args)
		}
	}
}
func TestHelpNeverStarts(t *testing.T) {
	var out bytes.Buffer
	if execute([]string{"--help"}, &out, io.Discard, func() int { t.Fatal("started"); return 1 }) != 0 || !bytes.Contains(out.Bytes(), []byte("no Developer ID")) {
		t.Fatal("help")
	}
}
func TestForegroundExplicitlyWarnsAndPreservesOutcome(t *testing.T) {
	var out, diag bytes.Buffer
	calls := 0
	if execute([]string{"foreground"}, &out, &diag, func() int { calls++; return 1 }) != 1 || calls != 1 || out.Len() != 0 || !bytes.Contains(diag.Bytes(), []byte("DEVELOPMENT ONLY")) {
		t.Fatal("entry contract")
	}
}
