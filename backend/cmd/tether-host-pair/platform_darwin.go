//go:build darwin

package main

import "os/exec"

func openBrowser(url string) error { return exec.Command("/usr/bin/open", url).Start() }
