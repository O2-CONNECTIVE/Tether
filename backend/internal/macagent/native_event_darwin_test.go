//go:build darwin

package macagent

import (
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"golang.org/x/sys/unix"
)

// Pure event decoding only: no kqueue, process, descriptor or signal operation.
func TestDarwinExitEventDecoding(t *testing.T) {
	cases := []struct {
		name  string
		event unix.Kevent_t
		known bool
		code  int32
		bad   bool
	}{
		{"exit5", unix.Kevent_t{Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS, Data: 5 << 8}, true, 5, false},
		{"signal9", unix.Kevent_t{Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS, Data: 9}, true, 137, false},
		{"missing-status", unix.Kevent_t{Fflags: unix.NOTE_EXIT}, false, 0, false},
		{"unexpected-one-shot", unix.Kevent_t{}, false, 0, true},
		{"kernel-error", unix.Kevent_t{Flags: unix.EV_ERROR, Data: int64(unix.ESRCH)}, false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, known, err := decodeExitEvent(tc.event)
			if (err != nil) != tc.bad || known != tc.known || (!tc.bad && known && info.code != tc.code) {
				t.Fatalf("info=%+v known=%v err=%v", info, known, err)
			}
		})
	}
}

func TestDarwinRegistrationESRCHKeepsPartialStartFailed(t *testing.T) {
	h := newHarness(t, time.Minute, agentexec.Limits{})
	h.spawner.configure = func(f *fakeOS) { f.observerErr = unix.ESRCH }
	h.spawner.result = func(sp spawned) (spawned, error) { return sp, errors.Join(errObserver, unix.ESRCH) }
	st := h.start("observer-esrch", "exit 0\n")
	c := h.spawner.child(t)
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseFailed || final.Exit.Known || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final=%+v", final)
	}
	if got := c.os.entries(); !before(got, "KILL", "reap") {
		t.Fatalf("order=%v", got)
	}
	noViolation(t, c.os)
}
