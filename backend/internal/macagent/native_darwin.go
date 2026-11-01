//go:build darwin

package macagent

import (
	"errors"
	"os"
	ossignal "os/signal"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// This file is compiled for darwin only and has never been executed by the
// repository tests; every behavioural test uses the fake osProcess seam.

const nativeSupported = true

var (
	errEnvironment    = errors.New("macagent: refusing an inherited environment")
	errUnexpectedReap = errors.New("macagent: wait returned another process")
	errInheritableFD  = errors.New("macagent: a descriptor >= 3 is not close-on-exec")
	errFDTable        = errors.New("macagent: the descriptor table could not be read")
	errSIGCHLD        = errors.New("macagent: SIGCHLD is ignored, so the kernel may reap the leader")
	errNoObserver     = errors.New("macagent: no exit observer")
)

var nativeChecks = hostChecks{inheritableFDs: nativeInheritableFDs, childReaping: nativeChildReaping}

// nativeChildReaping is read-only: it never changes the SIGCHLD disposition.
// It cannot see SA_NOCLDWAIT installed by foreign code; that stays a trusted
// composition invariant.
func nativeChildReaping() error {
	if ossignal.Ignored(syscall.SIGCHLD) {
		return errSIGCHLD
	}
	return nil
}

// nativeInheritableFDs lists this process's descriptors through /dev/fd and
// refuses if any descriptor >= 3 lacks FD_CLOEXEC. It only reads flags; it does
// not close or change any host descriptor, and it is not atomic with the fork.
func nativeInheritableFDs() error {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return errors.Join(errFDTable, err)
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd < 0 {
			return errFDTable
		}
		if fd < 3 {
			continue // replaced by the child's own pipes
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if errors.Is(err, unix.EBADF) {
			continue // closed after the listing, e.g. the listing's own descriptor
		}
		if err != nil {
			return errors.Join(errFDTable, err)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			return &startFault{kind: StartFailureInheritedDescriptor, cause: errInheritableFD}
		}
	}
	return nil
}

// nativeSpawn starts the fixed shell as the current user in a new session, so
// the child has no controlling terminal and is the leader of its own process
// group (pgid == pid). Only the three pipes are mapped to 0..2; os.Pipe makes
// them close-on-exec, and the preflight has refused any other inheritable
// descriptor. os.Process is released immediately: its Wait/Signal are never
// used, so every reap and group signal goes through darwinProcess under the
// process mutex.
//
// If the exit observer cannot be created after the child exists, the same
// process is returned with an error: the caller owns it as a partial start and
// stops and reaps it like any other.
func nativeSpawn(spec spawnSpec) (spawned, error) {
	if spec.env == nil { // a nil Env would make os.StartProcess inherit os.Environ
		return spawned{}, errEnvironment
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return spawned{}, &startFault{kind: StartFailureStdinPipe, cause: err}
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		closeFiles(inR, inW)
		return spawned{}, &startFault{kind: StartFailureStdoutPipe, cause: err}
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeFiles(inR, inW, outR, outW)
		return spawned{}, &startFault{kind: StartFailureStderrPipe, cause: err}
	}
	proc, err := os.StartProcess(spec.path, spec.argv, &os.ProcAttr{
		Dir:   spec.dir,
		Env:   spec.env,
		Files: []*os.File{inR, outW, errW},
		Sys:   &syscall.SysProcAttr{Setsid: true}, // setsid also makes pgid == pid
	})
	closeFiles(inR, outW, errW) // the child holds its own copies
	if err != nil {
		// StartProcess reaps a child whose exec failed, so no process remains.
		closeFiles(inW, outR, errR)
		return spawned{}, execStartFault(err)
	}
	d := &darwinProcess{pid: proc.Pid, kq: -1}
	_ = proc.Release()
	sp := spawned{proc: d, stdin: inW, stdout: outR, stderr: errR}
	if err := d.watch(); err != nil {
		d.watchErr = errors.Join(errObserver, err)
		return sp, d.watchErr
	}
	return sp, nil
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// darwinProcess is the unreaped leader of the group with ID == pid. StartProcess
// returns only after the child ran setsid and exec, so the group exists. All
// methods run under process.mu.
type darwinProcess struct {
	pid      int
	done     bool // reaped, or reap state lost: never signal again
	kq       int  // exit observer; -1 when absent or released
	kqClosed bool
	kqErr    error // permanent result of the single close attempt
	watchErr error
	exitSeen bool
	exitInfo exitInfo
	exitKnow bool
}

// watch registers a one-shot EVFILT_PROC NOTE_EXIT|NOTE_EXITSTATUS filter. The
// event reports the exit and the wait status but leaves the zombie unreaped.
// The kqueue is created under ForkLock and marked close-on-exec before any
// concurrent fork can inherit it.
func (d *darwinProcess) watch() error {
	syscall.ForkLock.RLock()
	kq, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(kq)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return err
	}
	d.kq = kq
	change := unix.Kevent_t{
		Ident:  uint64(d.pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS,
	}
	for {
		_, err = unix.Kevent(kq, []unix.Kevent_t{change}, nil, &unix.Timespec{})
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	// Registration errors, including ESRCH, never prove a natural exit.
	// The caller retains this partial process and sweeps it before reaping.
	return err
}

func (d *darwinProcess) pollExit() (bool, exitInfo, bool, error) {
	if d.exitSeen {
		return true, d.exitInfo, d.exitKnow, nil
	}
	if d.watchErr != nil {
		return false, exitInfo{}, false, d.watchErr
	}
	if d.kq < 0 {
		return false, exitInfo{}, false, errNoObserver
	}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(d.kq, nil, events, &unix.Timespec{})
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, exitInfo{}, false, err
		}
		if n == 0 {
			return false, exitInfo{}, false, nil
		}
		info, known, err := decodeExitEvent(events[0])
		if err != nil {
			return false, exitInfo{}, false, err
		}
		d.exitSeen, d.exitInfo, d.exitKnow = true, info, known
		return true, d.exitInfo, d.exitKnow, nil
	}
}

// decodeExitEvent is pure: consuming an unexpected one-shot event is an
// observation failure, not an indefinitely pending or invented natural exit.
func decodeExitEvent(ev unix.Kevent_t) (exitInfo, bool, error) {
	if ev.Flags&unix.EV_ERROR != 0 {
		return exitInfo{}, false, errors.Join(errObserver, syscall.Errno(ev.Data))
	}
	if ev.Fflags&unix.NOTE_EXIT == 0 {
		return exitInfo{}, false, errObserver
	}
	if ev.Fflags&unix.NOTE_EXITSTATUS == 0 {
		return exitInfo{}, false, nil
	}
	return statusInfo(syscall.WaitStatus(uint32(ev.Data))), true, nil
}

func statusInfo(ws syscall.WaitStatus) exitInfo {
	info := exitInfo{code: -1}
	switch {
	case ws.Exited():
		info.code = int32(ws.ExitStatus())
	case ws.Signaled():
		info.code = 128 + int32(ws.Signal())
	}
	return info
}

func (d *darwinProcess) releaseObserver() error {
	if d.kq < 0 || d.kqClosed {
		return d.kqErr
	}
	kq := d.kq
	d.kq, d.kqClosed = -1, true
	if err := unix.Close(kq); err != nil {
		d.kqErr = errors.Join(errCloseUncertain, err)
	}
	return d.kqErr
}

func (d *darwinProcess) signalGroup(s signal) error {
	if d.done || d.pid <= 1 {
		return errUnconfirmed
	}
	sig := syscall.SIGTERM
	if s == sigKill {
		sig = syscall.SIGKILL
	}
	// The leader is not reaped yet, so -pid still names only its own group.
	// Success means delivery to the group, not that every member exited.
	err := syscall.Kill(-d.pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (d *darwinProcess) tryReap() (bool, exitInfo, error) {
	if d.done {
		return false, exitInfo{}, errUnconfirmed
	}
	for {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(d.pid, &ws, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			d.done = true // e.g. ECHILD: someone else reaped it; ownership is lost
			return false, exitInfo{}, err
		}
		if wpid == 0 {
			return false, exitInfo{}, nil
		}
		d.done = true
		if wpid != d.pid {
			return false, exitInfo{}, errUnexpectedReap
		}
		return true, statusInfo(ws), nil
	}
}

// reapExitedOnce is used only by the terminal local disposition. It does not
// re-observe, signal, wait for a running child, or retry an interrupted wait.
func (d *darwinProcess) reapExitedOnce() (bool, exitInfo, error) {
	if d.done || d.pid <= 1 || !d.exitSeen || !d.exitKnow {
		return false, exitInfo{}, errUnconfirmed
	}
	var ws syscall.WaitStatus
	pid, err := syscall.Wait4(d.pid, &ws, syscall.WNOHANG, nil)
	if err != nil {
		d.done = true
		return false, exitInfo{}, err
	}
	if pid == 0 {
		return false, exitInfo{}, nil
	}
	d.done = true
	if pid != d.pid {
		return false, exitInfo{}, errUnexpectedReap
	}
	return true, statusInfo(ws), nil
}
