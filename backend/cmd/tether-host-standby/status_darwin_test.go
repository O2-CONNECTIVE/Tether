//go:build darwin

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"testing"
)

func fixturePipe(t *testing.T) (pipeOps, *[]string) {
	t.Helper()
	calls := []string{}
	check := func(fd int) {
		if fd != 9 {
			t.Fatalf("used non-owned fd %d", fd)
		}
	}
	return pipeOps{
		dup: func() (int, error) { calls = append(calls, "dup"); return 9, nil },
		stat: func(fd int, s *unix.Stat_t) error {
			check(fd)
			calls = append(calls, "stat")
			s.Mode = unix.S_IFIFO
			return nil
		},
		flags: func(fd int) (int, error) {
			check(fd)
			calls = append(calls, "flags")
			return unix.O_WRONLY | unix.O_NONBLOCK, nil
		},
		write: func(fd int, p []byte) (int, error) { check(fd); calls = append(calls, "write"); return len(p), nil },
		close: func(fd int) error { check(fd); calls = append(calls, "close"); return nil },
	}, &calls
}
func countCalls(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

func TestStatusPipeAcquiresOnlyCheckedDuplicate(t *testing.T) {
	o, calls := fixturePipe(t)
	s, err := acquireStatusPipe(o)
	if err != nil || !s.writeFrame([]byte("fixed\n")) || !s.close() || !s.close() {
		t.Fatal("valid pipe rejected")
	}
	if countCalls(*calls, "dup") != 1 || countCalls(*calls, "close") != 1 || countCalls(*calls, "write") != 1 {
		t.Fatalf("calls %v", *calls)
	}
	if s.writeFrame([]byte("closed\n")) {
		t.Fatal("wrote closed fd")
	}
}
func TestStatusPipeRejectsAcquisitionFailuresAndOtherOutputs(t *testing.T) {
	bad := errors.New("fixture")
	cases := []string{"dup", "stat", "regular", "terminal", "socket", "flags", "readonly", "readwrite", "blocking", "close"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			o, calls := fixturePipe(t)
			switch name {
			case "dup":
				o.dup = func() (int, error) { return -1, bad }
			case "stat":
				o.stat = func(int, *unix.Stat_t) error { return bad }
			case "regular", "terminal", "socket":
				mode := uint16(unix.S_IFREG)
				if name == "terminal" {
					mode = unix.S_IFCHR
				}
				if name == "socket" {
					mode = unix.S_IFSOCK
				}
				o.stat = func(_ int, s *unix.Stat_t) error { s.Mode = mode; return nil }
			case "flags":
				o.flags = func(int) (int, error) { return 0, bad }
			case "readonly":
				o.flags = func(int) (int, error) { return unix.O_RDONLY | unix.O_NONBLOCK, nil }
			case "readwrite":
				o.flags = func(int) (int, error) { return unix.O_RDWR | unix.O_NONBLOCK, nil }
			case "blocking":
				o.flags = func(int) (int, error) { return unix.O_WRONLY, nil }
			case "close":
				o.stat = func(int, *unix.Stat_t) error { return bad }
				base := o.close
				o.close = func(fd int) error { _ = base(fd); return bad }
			}
			s, err := acquireStatusPipe(o)
			if err == nil || (name == "dup" && s != nil) || (name != "dup" && s == nil) {
				t.Fatal("invalid pipe accepted")
			}
			if s != nil {
				_ = s.close()
			} // runner retains the record; no second syscall
			want := 1
			if name == "dup" {
				want = 0
			}
			if countCalls(*calls, "close") != want || countCalls(*calls, "write") != 0 {
				t.Fatalf("calls %v", *calls)
			}
		})
	}
}
func TestStatusPipeWriteNeverRetries(t *testing.T) {
	for _, name := range []string{"full", "interrupt", "reader lost", "short"} {
		t.Run(name, func(t *testing.T) {
			o, _ := fixturePipe(t)
			writes := 0
			o.write = func(int, []byte) (int, error) {
				writes++
				switch name {
				case "full":
					return 0, unix.EAGAIN
				case "interrupt":
					return 0, unix.EINTR
				case "reader lost":
					return 0, unix.EPIPE
				}
				return 1, nil
			}
			s, err := acquireStatusPipe(o)
			if err != nil || s.writeFrame([]byte("fixed\n")) || writes != 1 {
				t.Fatal("write retried or falsely delivered")
			}
			if !s.close() {
				t.Fatal("close failed")
			}
		})
	}
}
func TestStatusPipeUncertainCloseIsSticky(t *testing.T) {
	o, calls := fixturePipe(t)
	base := o.close
	o.close = func(fd int) error { _ = base(fd); return unix.EINTR }
	s, err := acquireStatusPipe(o)
	if err != nil || s.close() || s.close() || countCalls(*calls, "close") != 1 {
		t.Fatal("uncertain close retried or reported success")
	}
}
