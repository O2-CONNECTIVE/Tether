//go:build !darwin

package main

import "errors"

func openBrowser(string) error { return errors.New("unsupported") }
