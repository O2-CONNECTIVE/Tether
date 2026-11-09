package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpStatesForegroundBoundary(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if execute([]string{"--help"}, &out, &diagnostic) != 0 || diagnostic.Len() != 0 {
		t.Fatal("help refused")
	}
	for _, text := range []string{"foreground", "status", "on <request_id> <expected_revision>", "off <request_id> <expected_revision>", "close", "pipe", "unavailable", "CI", "MCP"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing boundary", text)
		}
	}
}

func TestOrdinaryForegroundCannotAcquireSource(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if execute([]string{"foreground"}, &out, &diagnostic) != 2 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "RESIDENT_SOURCE_UNAVAILABLE") {
		t.Fatal("ordinary source gate changed")
	}
}

func TestForegroundRejectsArgumentsBeforeSource(t *testing.T) {
	for _, args := range [][]string{nil, {"on"}, {"--help", "foreground"}, {"foreground", "--policy", "/tmp/policy"}, {"foreground", "--ca", "ca"}, {"foreground", "--approve"}} {
		var out, diagnostic bytes.Buffer
		if execute(args, &out, &diagnostic) != 2 || out.Len() != 0 || strings.Contains(diagnostic.String(), "RESIDENT_SOURCE_UNAVAILABLE") {
			t.Fatal("invalid args reached source", args)
		}
	}
}
