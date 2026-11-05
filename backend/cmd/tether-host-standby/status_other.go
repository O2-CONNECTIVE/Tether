//go:build !darwin

package main

import "errors"

func openStatusSink() (statusSink, error) { return nil, errors.New("standby: unsupported platform") }
