//go:build windows

package operatorauth

import (
	"context"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func closeNativeHelperEntryHandle(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}

// Source only: SCM delivery, the supplier registration and its process/token
// security are unverified on a real host. Registration is not performed here.
type nativeSupplierDispatcher struct{}
type supplierEntryHandler struct {
	ctx      context.Context
	lifetime time.Duration
}

func nativeSupplierEntry() supplierEntryKernel { return nativeSupplierDispatcher{} }

func (nativeSupplierDispatcher) run(ctx context.Context, name string, lifetime time.Duration) error {
	if err := svc.Run(name, supplierEntryHandler{ctx: ctx, lifetime: lifetime}); err != nil {
		return ErrDenied
	}
	return nil
}

// Execute accepts only stop/shutdown. args, CurrentStatus and event data never
// select behaviour or extend the compiled lifetime.
func (h supplierEntryHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	statuses <- svc.Status{State: svc.StartPending}
	serveSupplierEntry(h.ctx, h.lifetime, requests, func() { statuses <- running }, func(r svc.ChangeRequest) bool {
		switch r.Cmd {
		case svc.Stop, svc.Shutdown:
			return true
		case svc.Interrogate:
			statuses <- running
		}
		return false
	})
	statuses <- svc.Status{State: svc.StopPending}
	return false, 0
}
