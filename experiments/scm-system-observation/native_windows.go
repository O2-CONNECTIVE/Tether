//go:build windows

package main

import (
	"context"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// This watchdog exits only this probe. Hard exit is a capture failure and does
// not claim checked cleanup. OS freezes/scheduling can delay even this timer.
func runNative() int {
	watchdog := time.AfterFunc(hardLimit, func() { os.Exit(124) })
	defer watchdog.Stop()
	h := &probeService{}
	if err := svc.Run(serviceName, h); err != nil {
		return 10
	}
	return h.exit
}

type probeService struct{ exit int }

func (h *probeService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	entry := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), cooperativeLimit)
	defer cancel()
	states, acknowledged := make(chan string), make(chan struct{})
	done := make(chan Report, 1)
	workerExited := make(chan struct{})
	go func() {
		defer close(workerExited)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		c := &nativeCollector{entry: entry}
		done <- Run(ctx, c, nativeReportWriter{serviceSID: &c.serviceSID}, func(s string) {
			states <- s
			<-acknowledged
			if s == "running" {
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-ctx.Done():
				case <-timer.C:
				}
			}
		})
	}()
	current := svc.Status{State: svc.StartPending, WaitHint: 15000}
	for {
		select {
		case state := <-states:
			switch state {
			case "start_pending":
				current = svc.Status{State: svc.StartPending, CheckPoint: 1, WaitHint: 15000}
			case "running":
				current = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			case "stop_pending":
				current = svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: 5000}
			}
			changes <- current
			acknowledged <- struct{}{}
		case request, ok := <-requests:
			if !ok {
				cancel()
				requests = nil
				continue
			}
			switch request.Cmd {
			case svc.Stop, svc.Shutdown:
				cancel()
			case svc.Interrogate:
				changes <- current
			}
		case report := <-done:
			<-workerExited
			if report.CaptureStatus != "complete" {
				h.exit = 10
				return true, 10
			}
			return false, 0
		}
	}
}

type nativeCollector struct {
	entry           time.Time
	process, thread windows.Handle
	token           windows.Token
	serviceSID      string
}

func (c *nativeCollector) Capture(ctx context.Context, phase string) (s Snapshot, failures []Failure) {
	s.Phase = phase
	s.StartedAfterEntryNS = time.Since(c.entry).Nanoseconds()
	defer func() { s.FinishedAfterEntryNS = time.Since(c.entry).Nanoseconds() }()
	if ctx.Err() != nil {
		return s, contextFailures(ctx)
	}
	if c.serviceSID == "" {
		var f []Failure
		c.serviceSID, f = resolveFixedServiceSID()
		if len(f) != 0 {
			return s, f
		}
	}
	s.ProcessID, s.WorkerThreadID = windows.GetCurrentProcessId(), windows.GetCurrentThreadId()
	if c.process == 0 {
		var err error
		c.process, err = windows.OpenProcess(windows.READ_CONTROL|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, s.ProcessID)
		if err != nil {
			return s, failure("open_self_process", err)
		}
		c.thread, err = windows.OpenThread(windows.READ_CONTROL|windows.THREAD_QUERY_LIMITED_INFORMATION, false, s.WorkerThreadID)
		if err != nil {
			return s, failure("open_worker_thread", err)
		}
	}
	if err := windows.OpenProcessToken(c.process, windows.TOKEN_QUERY|windows.READ_CONTROL, &c.token); err != nil {
		return s, failure("open_primary_token", err)
	}
	defer func() {
		if c.token != 0 {
			failures = append(failures, failure("close_primary_token", c.token.Close())...)
			c.token = 0
		}
	}()
	var f []Failure
	s.Process, f = readSecurity(c.process, windows.SE_KERNEL_OBJECT, "process_security")
	failures = append(failures, f...)
	s.WorkerThread, f = readSecurity(c.thread, windows.SE_KERNEL_OBJECT, "worker_security")
	failures = append(failures, f...)
	s.PrimaryTokenObject, f = readSecurity(windows.Handle(c.token), windows.SE_KERNEL_OBJECT, "token_object_security")
	failures = append(failures, f...)
	s.PrimaryToken, f = readToken(ctx, c.token, c.serviceSID)
	failures = append(failures, f...)
	var threadToken windows.Token
	err := windows.OpenThreadToken(c.thread, windows.TOKEN_QUERY, true, &threadToken)
	if err == nil {
		s.WorkerHasImpersonationToken = true
		failures = append(failures, Failure{Stage: "worker_unexpected_impersonation"})
		failures = append(failures, failure("close_worker_token", threadToken.Close())...)
	} else if err != windows.ERROR_NO_TOKEN {
		failures = append(failures, failure("query_worker_token", err)...)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(c.process, &created, &exited, &kernel, &user); err != nil {
		failures = append(failures, failure("process_creation_time", err)...)
	} else {
		s.ProcessCreationFiletime = uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	}
	r, _, err := getThreadTimes.Call(uintptr(c.thread), uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		failures = append(failures, failure("worker_creation_time", err)...)
	} else {
		s.WorkerCreationFiletime = uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	}
	return s, failures
}

func (c *nativeCollector) Close() (failures []Failure) {
	if c.token != 0 {
		failures = append(failures, failure("close_remaining_token", c.token.Close())...)
		c.token = 0
	}
	if c.thread != 0 {
		failures = append(failures, failure("close_worker_thread", windows.CloseHandle(c.thread))...)
		c.thread = 0
	}
	if c.process != 0 {
		failures = append(failures, failure("close_self_process", windows.CloseHandle(c.process))...)
		c.process = 0
	}
	return failures
}

var getThreadTimes = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetThreadTimes")
