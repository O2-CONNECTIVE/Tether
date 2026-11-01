//go:build darwin

package maclaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const terminalPoll = 100 * time.Millisecond
const terminalRetryLimit = 8

// Readiness uses select(2). On the tested Mac/Go host, os.File deadlines
// on /dev/tty returned os.ErrNoDeadline. poll(2) was not evaluated. The
// descriptor must fit fd_set; otherwise the prompt is refused.
const selectLimit = 8 * int(unsafe.Sizeof(unix.FdSet{}))

// No elevated identity, stdin fallback, configured terminal path or persistent
// terminal setting is accepted. O_NONBLOCK applies only to this newly owned
// open file description; no inherited descriptor or terminal mode is changed.
func localSupported() bool {
	return os.Getuid() != 0 && os.Geteuid() == os.Getuid() && os.Getegid() == os.Getgid()
}
func openTerminal(ctx context.Context, deadline time.Time) (io.ReadWriteCloser, error) {
	if !localSupported() || ctx == nil || ctx.Err() != nil {
		return nil, ErrDenied
	}
	fd, err := openTTY()
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || uint32(st.Mode)&unix.S_IFMT != unix.S_IFCHR || fd >= selectLimit {
		_ = unix.Close(fd)
		return nil, ErrDenied
	}
	return newLocalTerminal(ctx, deadline, fd, nativeTTY), nil
}

func openTTY() (int, error) {
	for range 8 {
		fd, err := unix.Open("/dev/tty", unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == unix.EINTR {
			continue
		}
		if err != nil || fd < 0 {
			return -1, errors.Join(ErrDenied, err)
		}
		return fd, nil
	}
	return -1, ErrDenied
}

// ttySys is the private syscall seam. Wait is bounded; read/write use the
// nonblocking fd. close(2) is not covered by the wait bound. No goroutine starts.
type ttySys struct {
	wait  func(fd int, write bool, timeout time.Duration) (bool, error)
	read  func(fd int, p []byte) (int, error)
	write func(fd int, p []byte) (int, error)
	close func(fd int) error
}

var nativeTTY = ttySys{wait: selectTTY, read: unix.Read, write: unix.Write, close: unix.Close}

func selectTTY(fd int, write bool, timeout time.Duration) (bool, error) {
	if fd < 0 || fd >= selectLimit || timeout < 0 {
		return false, ErrDenied
	}
	var set unix.FdSet
	set.Set(fd)
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	r, w := &set, (*unix.FdSet)(nil)
	if write {
		r, w = nil, &set
	}
	n, err := unix.Select(fd+1, r, w, nil, &tv)
	if err != nil {
		return false, err
	}
	return n > 0 && set.IsSet(fd), nil
}

type localTerminal struct {
	fd       int
	sys      ttySys
	ctx      context.Context
	deadline time.Time
	now      func() time.Time
	// Every I/O loop holds use for reading. Close sets closing, which loops
	// observe within one slice, then takes use exclusively, so close(2) never
	// races a syscall that could reach a reused descriptor number.
	closing  atomic.Bool
	use      sync.RWMutex
	once     sync.Once
	closeErr error
}

func newLocalTerminal(ctx context.Context, deadline time.Time, fd int, sys ttySys) *localTerminal {
	return &localTerminal{fd: fd, sys: sys, ctx: ctx, deadline: deadline.Round(0), now: time.Now}
}

// check returns the next finite wait slice, capped by the absolute deadline.
func (t *localTerminal) check() (time.Duration, error) {
	if t.closing.Load() {
		return 0, os.ErrClosed
	}
	if err := t.ctx.Err(); err != nil {
		return 0, err
	}
	left := t.deadline.Sub(t.now().Round(0))
	if left <= 0 {
		return 0, ErrDenied
	}
	return min(left, terminalPoll), nil
}

// wait returns nil only after readiness and a second check, so cancellation,
// expiry or Close observed after the wait refuses I/O. Cancellation may race
// the next syscall; confirm checks again before creating any grant.
func (t *localTerminal) wait(write bool) error {
	retries := 0
	for {
		slice, err := t.check()
		if err != nil {
			return err
		}
		ready, err := t.sys.wait(t.fd, write, slice)
		if err != nil && !retryable(err) {
			return err
		}
		if err != nil {
			retries++
			if retries >= terminalRetryLimit {
				return ErrDenied
			}
			continue
		}
		retries = 0
		if !ready {
			continue
		}
		_, err = t.check()
		return err
	}
}

func retryable(err error) bool { return errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) }

func (t *localTerminal) acquire() bool {
	t.use.RLock()
	if t.closing.Load() {
		t.use.RUnlock()
		return false
	}
	return true
}

func (t *localTerminal) Read(p []byte) (int, error) {
	if !t.acquire() {
		return 0, os.ErrClosed
	}
	defer t.use.RUnlock()
	if len(p) == 0 {
		return 0, nil
	}
	retries := 0
	for {
		if err := t.wait(false); err != nil {
			return 0, err
		}
		n, err := t.sys.read(t.fd, p)
		switch {
		case retryable(err):
			// Bound repeated spurious readiness even if the clock does not advance.
			retries++
			if retries >= terminalRetryLimit {
				return 0, ErrDenied
			}
		case err != nil:
			return 0, err
		case n == 0:
			return 0, io.EOF
		case n < 0 || n > len(p):
			return 0, ErrDenied
		default:
			return n, nil
		}
	}
}

// Write returns the bytes already accepted with any error, as io.Writer
// requires; a refused or interrupted prompt is never treated as displayed.
func (t *localTerminal) Write(p []byte) (int, error) {
	if !t.acquire() {
		return 0, os.ErrClosed
	}
	defer t.use.RUnlock()
	done, retries := 0, 0
	for done < len(p) {
		if err := t.wait(true); err != nil {
			return done, err
		}
		n, err := t.sys.write(t.fd, p[done:])
		switch {
		case retryable(err):
			retries++
			if retries >= terminalRetryLimit {
				return done, ErrDenied
			}
		case err != nil:
			return done, err
		case n <= 0 || n > len(p)-done:
			return done, io.ErrShortWrite
		default:
			done += n
			retries = 0
		}
	}
	return done, nil
}

// Close is idempotent and safe during I/O. In-flight readiness waits end
// within one slice plus scheduling delay; the OS close itself has no timeout.
// A failed close(2) is not retried, because the number may already be reused.
// It must not be called from inside Read or Write.
func (t *localTerminal) Close() error {
	t.closing.Store(true)
	t.once.Do(func() {
		t.use.Lock()
		defer t.use.Unlock()
		t.closeErr = t.sys.close(t.fd)
	})
	return t.closeErr
}
