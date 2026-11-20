//go:build darwin && cgo

package residentmac

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDefaultSleepAssertionAppearsInPmsetOnlyDuringOwnedWork(t *testing.T) {
	h := newSleepHarness(t, StandbyConfig{}, nil)
	session := newSleepTestSession()
	if err := submitSleepWork(t, h, session); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("owned work did not start")
	}
	waitForPmsetAssertion(t, true)

	close(session.finish)
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.reconcileSleep(h.cycle.owner, false)
	eventuallySleep(t, func() bool {
		h.standby.sleepMu.Lock()
		defer h.standby.sleepMu.Unlock()
		return !h.standby.sleepHeld
	})
	waitForPmsetAssertion(t, false)
}

func waitForPmsetAssertion(t *testing.T, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("pmset", "-g", "assertions").CombinedOutput()
		if err != nil {
			t.Fatalf("pmset -g assertions failed: %v: %s", err, output)
		}
		found := false
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, fmt.Sprintf("pid %d(", os.Getpid())) &&
				strings.Contains(line, "PreventUserIdleSystemSleep") &&
				strings.Contains(line, "Tether resident work") {
				found = true
				break
			}
		}
		if found == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if want {
		t.Fatal("pmset did not show this PID's PreventUserIdleSystemSleep assertion")
	}
	t.Fatal("pmset still showed this PID's PreventUserIdleSystemSleep assertion after collection")
}
