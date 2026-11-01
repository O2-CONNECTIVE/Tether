package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

var localUser = identity{"darwin", 501, 501, 20, 20}

type fakeStatus struct {
	lines                  chan string
	writes, closes, failAt int
	closeOK                bool
}

func (s *fakeStatus) writeFrame(p []byte) bool {
	s.writes++
	s.lines <- string(p)
	return s.writes != s.failAt
}
func (s *fakeStatus) close() bool { s.closes++; return s.closeOK }
func receive(t *testing.T, c <-chan string, want string) {
	t.Helper()
	select {
	case line := <-c:
		if line != want {
			t.Fatalf("status %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("status missing")
	}
}
func result(t *testing.T, c <-chan int, want int) {
	t.Helper()
	select {
	case code := <-c:
		if code != want {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}

type observedWait struct {
	context.Context
	waiting chan struct{}
}

func (w observedWait) Done() <-chan struct{} { close(w.waiting); return w.Context.Done() }

func TestStandbySilentModeNeverOpensOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- execute(observedWait{ctx, waiting}, []string{"--standby"}, localUser, func() (statusSink, error) { panic("output opened") })
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("silent wait not reached")
	}
	cancel()
	result(t, done, 0)
}

func TestStandbyReportsNoAuthorityAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &fakeStatus{lines: make(chan string, 4), closeOK: true}
	done := make(chan int, 1)
	go func() {
		done <- execute(ctx, []string{"--standby", "--status-stdout"}, localUser, func() (statusSink, error) { return s, nil })
	}()
	receive(t, s.lines, "state=standby execution_grant=absent connection=absent\n")
	select {
	case <-done:
		t.Fatal("stopped early")
	default:
	}
	cancel()
	result(t, done, 0)
	receive(t, s.lines, "state=stopped execution_grant=absent connection=absent\n")
	if s.closes != 1 {
		t.Fatalf("closes %d", s.closes)
	}
}

func TestStandbyRejectsUnrequestedOrElevatedStartup(t *testing.T) {
	cases := []struct {
		name string
		args []string
		who  identity
	}{
		{"no opt in", nil, localUser}, {"config", []string{"--standby", "-config", "fixture"}, localUser},
		{"execution", []string{"--execute"}, localUser}, {"unknown output", []string{"--standby", "--stdout"}, localUser},
		{"root", []string{"--standby"}, identity{"darwin", 0, 0, 0, 0}},
		{"setuid", []string{"--standby"}, identity{"darwin", 501, 0, 20, 20}},
		{"setgid", []string{"--standby"}, identity{"darwin", 501, 501, 20, 0}},
		{"invalid uid", []string{"--standby"}, identity{"darwin", -1, -1, 20, 20}},
		{"invalid gid", []string{"--standby"}, identity{"darwin", 501, 501, -1, -1}},
		{"linux", []string{"--standby"}, identity{"linux", 501, 501, 20, 20}},
		{"windows", []string{"--standby"}, identity{"windows", 501, 501, 20, 20}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if got := execute(ctx, tt.args, tt.who, func() (statusSink, error) { t.Fatal("invalid startup acquired output"); return nil, nil }); got != 2 {
				t.Fatalf("exit %d", got)
			}
		})
	}
}

func TestStandbyCancellationBeforeAndDuringAcquisition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args := []string{"--standby", "--status-stdout"}
	if execute(ctx, args, localUser, func() (statusSink, error) { t.Fatal("pre-cancel acquired output"); return nil, nil }) != 1 {
		t.Fatal("pre-cancel accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	s := &fakeStatus{lines: make(chan string, 4), closeOK: true}
	if execute(ctx, args, localUser, func() (statusSink, error) { cancel(); return s, nil }) != 1 || s.writes != 0 || s.closes != 1 {
		t.Fatal("canceled acquisition leaked or reported ready")
	}
}

func TestStandbyOutputFailureIsStickyButDoesNotBlockStop(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		ctx, cancel := context.WithCancel(context.Background())
		s := &fakeStatus{lines: make(chan string, 4), closeOK: true, failAt: failAt}
		done := make(chan int, 1)
		go func() {
			done <- execute(ctx, []string{"--standby", "--status-stdout"}, localUser, func() (statusSink, error) { return s, nil })
		}()
		receive(t, s.lines, "state=standby execution_grant=absent connection=absent\n")
		cancel()
		result(t, done, 1)
		if s.writes != failAt || s.closes != 1 {
			t.Fatalf("writes=%d closes=%d", s.writes, s.closes)
		}
	}
}

func TestStandbyAcquisitionAndCloseFailureAreNonSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	args := []string{"--standby", "--status-stdout"}
	if execute(ctx, args, localUser, func() (statusSink, error) { return nil, errors.New("fixture") }) != 1 {
		t.Fatal("acquisition failure accepted")
	}
	if execute(ctx, args, localUser, nil) != 2 {
		t.Fatal("missing opener accepted")
	}
	if execute(nil, args, localUser, nil) != 2 {
		t.Fatal("nil context accepted")
	}
	s := &fakeStatus{lines: make(chan string, 4), closeOK: false}
	got := execute(ctx, args, localUser, func() (statusSink, error) { cancel(); return s, nil })
	if got != 1 || s.closes != 1 {
		t.Fatal("close failure accepted")
	}
}

func TestStandbyFinalCloseFailureOverridesSuccessfulReporting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &fakeStatus{lines: make(chan string, 4), closeOK: false}
	done := make(chan int, 1)
	go func() {
		done <- execute(ctx, []string{"--standby", "--status-stdout"}, localUser, func() (statusSink, error) { return s, nil })
	}()
	receive(t, s.lines, "state=standby execution_grant=absent connection=absent\n")
	cancel()
	result(t, done, 1)
	receive(t, s.lines, "state=stopped execution_grant=absent connection=absent\n")
	if s.closes != 1 {
		t.Fatal("close was retried")
	}
}

func TestStandbyRetainsPartialAcquisitionAndClosesOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &fakeStatus{lines: make(chan string, 4), closeOK: false}
	code := execute(ctx, []string{"--standby", "--status-stdout"}, localUser, func() (statusSink, error) { return s, errors.New("partial acquisition") })
	if code != 1 || s.writes != 0 || s.closes != 1 {
		t.Fatal("partial acquisition discarded or reported ready")
	}
	if execute(ctx, []string{"--standby", "--status-stdout"}, localUser, func() (statusSink, error) { return nil, nil }) != 1 {
		t.Fatal("nil sink accepted")
	}
}
