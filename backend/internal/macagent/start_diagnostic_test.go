package macagent

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestStartDiagnosticsClassifyOnlyNoChildRefusals(t *testing.T) {
	cases := []struct {
		name  string
		kind  StartFailure
		fault error
		which int
	}{
		{"sigchld", StartFailureSIGCHLD, errors.New("private-value"), 1},
		{"fd-table", StartFailureDescriptorTable, errors.New("private-value"), 2},
		{"inheritable", StartFailureInheritedDescriptor, &startFault{StartFailureInheritedDescriptor, errors.New("private-value")}, 2},
		{"stdin", StartFailureStdinPipe, &startFault{StartFailureStdinPipe, errors.New("private-value")}, 3},
		{"stdout", StartFailureStdoutPipe, &startFault{StartFailureStdoutPipe, errors.New("private-value")}, 3},
		{"stderr", StartFailureStderrPipe, &startFault{StartFailureStderrPipe, errors.New("private-value")}, 3},
		{"exec-permission", StartFailureExecPermission, execStartFault(&os.PathError{Op: "fork/exec", Path: "private-path", Err: syscall.EACCES}), 3},
		{"exec-missing", StartFailureExecMissing, execStartFault(syscall.ENOENT), 3},
		{"exec-resource", StartFailureExecResource, execStartFault(syscall.EMFILE), 3},
		{"exec-other", StartFailureExecOther, execStartFault(errors.New("private-value")), 3},
		{"spawn-other", StartFailureSpawnOther, errors.New("private-value"), 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diag := &StartDiagnostics{}
			calls := 0
			r := &Runtime{diagnostics: diag, checks: hostChecks{childReaping: func() error { return nil }, inheritableFDs: func() error { return nil }}, spawn: func(spawnSpec) (spawned, error) { calls++; return spawned{}, tc.fault }}
			if tc.which == 1 {
				r.checks.childReaping = func() error { return tc.fault }
			}
			if tc.which == 2 {
				r.checks.inheritableFDs = func() error { return tc.fault }
			}
			p, err := r.StartShell(context.Background(), supervisor.Binding{Generation: 1}, []byte("synthetic"))
			if p != nil || !errors.Is(err, agentexec.ErrNotStarted) || !errors.Is(err, tc.fault) || diag.Failure() != tc.kind {
				t.Fatalf("diagnostic=%s child=%t", diag.Failure(), p != nil)
			}
			if (tc.which < 3 && calls != 0) || (tc.which == 3 && calls != 1) {
				t.Fatal("unexpected fake spawn count")
			}
		})
	}
}

func TestStartDiagnosticsConfigurationContextAndBinding(t *testing.T) {
	for _, name := range []string{"config", "nil-context", "canceled", "binding"} {
		t.Run(name, func(t *testing.T) {
			d := &StartDiagnostics{}
			r := &Runtime{diagnostics: d, spawn: func(spawnSpec) (spawned, error) { t.Fatal("spawn reached"); return spawned{}, nil }}
			ctx := context.Background()
			g := supervisor.Binding{Generation: 1}
			want := StartFailureConfiguration
			switch name {
			case "config":
				r.spawn = nil
			case "nil-context":
				ctx = nil
				want = StartFailureContext
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
				want = StartFailureContext
			case "binding":
				g.Generation = 0
				want = StartFailureBinding
			}
			p, err := r.StartShell(ctx, g, nil)
			if p != nil || !errors.Is(err, agentexec.ErrNotStarted) || d.Failure() != want {
				t.Fatal("wrong refusal")
			}
		})
	}
}

func TestStartDiagnosticsPreservePartialProcessOwnership(t *testing.T) {
	s := newFakeSpawner()
	fault := execStartFault(syscall.EPERM)
	s.result = func(sp spawned) (spawned, error) { return sp, fault }
	cfg := testTiming
	d := &StartDiagnostics{}
	cfg.StartDiagnostics = d
	r, err := newRuntime(cfg, s.spawn, s.checks(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.StartShell(context.Background(), supervisor.Binding{Generation: 1}, nil)
	if p == nil || !errors.Is(err, fault) || errors.Is(err, agentexec.ErrNotStarted) || d.Failure() != StartFailureNone {
		t.Fatal("partial spawn relabeled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Terminate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStartDiagnosticsFixedAndFirstOnly(t *testing.T) {
	var nilRecord *StartDiagnostics
	nilRecord.record(StartFailureExecOther)
	if nilRecord.Failure() != StartFailureNone {
		t.Fatal("nil record")
	}
	d := &StartDiagnostics{}
	d.record(StartFailureNone)
	d.record(StartFailure(999))
	d.record(StartFailureInheritedDescriptor)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.record(StartFailureExecPermission); _ = d.Failure() }()
	}
	wg.Wait()
	if d.Failure() != StartFailureInheritedDescriptor {
		t.Fatal("first overwritten")
	}
	want := []string{"none", "configuration", "context", "binding", "sigchld", "descriptor_table", "inherited_descriptor", "stdin_pipe", "stdout_pipe", "stderr_pipe", "exec_permission", "exec_missing", "exec_resource", "exec_other", "spawn_other", "work_dir"}
	for i, s := range want {
		if StartFailure(i).String() != s {
			t.Fatal("nonfixed value")
		}
	}
	if StartFailure(999).String() != "unknown" {
		t.Fatal("unknown value")
	}
}
