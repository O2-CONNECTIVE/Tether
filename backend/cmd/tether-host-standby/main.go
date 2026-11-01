// tether-host-standby is an opt-in, authority-free local waiting process.
// It is not the single-grant rescue-mac-agent and never starts or reconnects it.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

type identity struct {
	platform                             string
	uid, effectiveUID, gid, effectiveGID int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := execute(ctx, os.Args[1:], os.Stdout, identity{runtime.GOOS, os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid()})
	stop()
	os.Exit(code)
}

// execute owns only this wait, not an execution session. Cancellation is the
// stop input; stopped says nothing about cleanup of any other process. Native
// identity rejects root/elevation but does not prove an Aqua login session.
// No keys, config, grant, terminal, network, child process or persistence are
// available through this seam. Tests invoke it with finite synthetic contexts.
func execute(ctx context.Context, args []string, out io.Writer, who identity) int {
	if ctx == nil || out == nil || len(args) != 1 || args[0] != "--standby" {
		return 2
	}
	if who.platform != "darwin" || who.uid <= 0 || who.uid != who.effectiveUID || who.gid < 0 || who.gid != who.effectiveGID {
		return 2
	}
	if ctx.Err() != nil {
		return 1
	}
	if _, err := fmt.Fprintln(out, "state=standby execution_grant=absent connection=absent"); err != nil {
		return 1
	}
	<-ctx.Done()
	if _, err := fmt.Fprintln(out, "state=stopped execution_grant=absent connection=absent"); err != nil {
		return 1
	}
	return 0
}
