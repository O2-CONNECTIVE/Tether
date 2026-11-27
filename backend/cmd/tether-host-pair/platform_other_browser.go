//go:build !darwin

package main

import (
	"context"
	"errors"
)

func openBrowser(string) error { return errors.New("unsupported") }

func runAppleScript(context.Context, string, ...string) (string, error) {
	return "", errors.New("unsupported")
}
