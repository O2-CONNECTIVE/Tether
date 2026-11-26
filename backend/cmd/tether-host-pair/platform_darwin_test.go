//go:build darwin

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunAppleScriptPassesOptionLikeValuesAsArguments(t *testing.T) {
	// Why: account labels beginning with a dash must reach AppleScript argv instead of becoming osascript options.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := `on run argv
set AppleScript's text item delimiters to "|"
return argv as text
end run`
	got, err := runAppleScript(ctx, script, "-e", "-Abc")
	if err != nil || strings.TrimSpace(got) != "-e|-Abc" {
		t.Fatalf("osascript argv = %q, err=%v; want -e|-Abc", got, err)
	}
}
