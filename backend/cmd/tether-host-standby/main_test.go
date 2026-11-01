package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type statusLines chan string

func (s statusLines) Write(p []byte) (int, error) { s <- string(p); return len(p), nil }

func TestStandbyReportsNoAuthorityAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(statusLines, 4)
	done := make(chan int, 1)
	go func() { done <- execute(ctx, []string{"--standby"}, lines, identity{"darwin", 501, 501, 20, 20}) }()
	select {
	case line := <-lines:
		if line != "state=standby execution_grant=absent connection=absent\n" {
			t.Fatalf("unexpected status: %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("standby status missing")
	}
	select {
	case <-done:
		t.Fatal("standby ended before stop")
	default:
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not complete")
	}
	select {
	case line := <-lines:
		if line != "state=stopped execution_grant=absent connection=absent\n" {
			t.Fatalf("unexpected stop: %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("stopped status missing")
	}
}

func TestStandbyRejectsUnrequestedOrElevatedStartup(t *testing.T) {
	good := identity{"darwin", 501, 501, 20, 20}
	cases := []struct {
		name string
		args []string
		who  identity
	}{
		{"no opt in", nil, good},
		{"grant config", []string{"--standby", "-config", "fixture"}, good},
		{"execution flag", []string{"--execute"}, good},
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
			var out bytes.Buffer
			if got := execute(ctx, tt.args, &out, tt.who); got != 2 {
				t.Fatalf("exit = %d", got)
			}
			if out.Len() != 0 {
				t.Fatal("rejected startup reported standby")
			}
		})
	}
}

func TestStandbyCanceledBeforeStartDoesNotReportReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if execute(ctx, []string{"--standby"}, &out, identity{"darwin", 501, 501, 20, 20}) != 1 || out.Len() != 0 {
		t.Fatal("canceled startup reported ready")
	}
}

type failedStatus struct {
	writes int
	failAt int
	cancel context.CancelFunc
}

func (w *failedStatus) Write(p []byte) (int, error) {
	w.writes++
	if w.cancel != nil {
		w.cancel()
	}
	if w.writes == w.failAt {
		return 0, errors.New("synthetic output failure")
	}
	return len(p), nil
}

func TestStandbyOutputFailureNeverReportsSuccess(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		ctx, cancel := context.WithCancel(context.Background())
		w := &failedStatus{failAt: failAt, cancel: cancel}
		got := execute(ctx, []string{"--standby"}, w, identity{"darwin", 501, 501, 20, 20})
		cancel()
		if got != 1 || w.writes != failAt {
			t.Fatalf("failure %d: exit=%d writes=%d", failAt, got, w.writes)
		}
	}
}

func TestStandbyRejectsMissingDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	who := identity{"darwin", 501, 501, 20, 20}
	if execute(nil, []string{"--standby"}, io.Discard, who) != 2 || execute(ctx, []string{"--standby"}, nil, who) != 2 {
		t.Fatal("missing dependency accepted")
	}
}
