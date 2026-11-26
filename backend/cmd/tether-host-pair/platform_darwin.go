//go:build darwin

package main

import (
	"context"
	"os/exec"
	"time"
)

func openBrowser(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/open", url).Run()
}

func runAppleScript(ctx context.Context, script string, args ...string) (string, error) {
	commandArgs := append([]string{"-e", script}, args...)
	output, err := exec.CommandContext(ctx, "/usr/bin/osascript", commandArgs...).Output()
	return string(output), err
}
