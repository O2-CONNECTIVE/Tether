//go:build !darwin

package maclaunch

import (
	"context"
	"io"
	"time"
)

func localSupported() bool                                                { return false }
func openTerminal(context.Context, time.Time) (io.ReadWriteCloser, error) { return nil, ErrDenied }
