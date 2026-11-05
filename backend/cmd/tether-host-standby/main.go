// tether-host-standby is an opt-in, authority-free local waiting process.
// It is not the single-grant rescue-mac-agent and never starts or reconnects it.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

type identity struct {
	platform                             string
	uid, effectiveUID, gid, effectiveGID int
}

// Native status operations are single nonblocking attempts on an owned pipe.
// They must not use a generic potentially blocking io.Writer or worker goroutine.
type statusSink interface {
	writeFrame([]byte) bool
	close() bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := execute(ctx, os.Args[1:], identity{runtime.GOOS, os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid()}, openStatusSink)
	stop()
	os.Exit(code)
}

// execute owns only this wait. Stopped never asserts cleanup of other work.
// The default is silent; the optional status stream is not control authority.
// A failed status attempt disables reporting, not cancellation; exit is nonzero.
// Native identity rejects root/elevation, but does not prove an Aqua login.
func execute(ctx context.Context, args []string, who identity, open func() (statusSink, error)) (code int) {
	if ctx == nil || (len(args) != 1 && len(args) != 2) || args[0] != "--standby" || (len(args) == 2 && args[1] != "--status-stdout") {
		return 2
	}
	if who.platform != "darwin" || who.uid <= 0 || who.uid != who.effectiveUID || who.gid < 0 || who.gid != who.effectiveGID {
		return 2
	}
	reporting := len(args) == 2
	if reporting && open == nil {
		return 2
	}
	if ctx.Err() != nil {
		return 1
	}
	var sink statusSink
	if reporting {
		var err error
		sink, err = open()
		if sink != nil {
			defer func() {
				if !sink.close() {
					code = 1
				}
			}()
		}
		if err != nil || sink == nil {
			return 1
		}
		if ctx.Err() != nil {
			return 1
		}
	}
	delivered := true
	if sink != nil {
		delivered = sink.writeFrame([]byte("state=standby execution_grant=absent connection=absent\n"))
	}
	<-ctx.Done()
	if sink != nil && delivered {
		delivered = sink.writeFrame([]byte("state=stopped execution_grant=absent connection=absent\n"))
	}
	if !delivered {
		return 1
	}
	return 0
}
