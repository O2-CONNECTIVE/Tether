package macagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func TestStartShellInResolvesAndConstrainsWorkingDirectory(t *testing.T) {
	// Refusing unresolved or escaping cwd values prevents an approved command from starting outside its reviewed work root.
	base := t.TempDir()
	work := filepath.Join(base, "work")
	outside := filepath.Join(t.TempDir(), "outside")
	workEvil := filepath.Join(base, "work-evil")
	for _, dir := range []string{filepath.Join(work, "src", "app"), filepath.Join(work, "real"), outside, workEvil} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "file.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"link-in": filepath.Join(work, "real"), "link-out": outside,
		filepath.Join("src", "link-out"): outside, "link-sibling": workEvil,
	} {
		if err := os.Symlink(target, filepath.Join(work, name)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	spawner := newFakeSpawner()
	diagnostics := &StartDiagnostics{}
	cfg := Config{WorkDir: work, Home: base, StartDiagnostics: diagnostics, TermGrace: time.Millisecond, KillWait: time.Millisecond, IOWait: time.Millisecond}
	r, err := newRuntime(cfg, spawner.spawn, spawner.checks(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	grant := supervisor.Binding{Generation: 1}

	accepted := map[string]string{".": root, "src/app": filepath.Join(root, "src", "app"), "link-in": filepath.Join(root, "real")}
	for dir, want := range accepted {
		proc, err := r.StartShellIn(context.Background(), grant, dir, nil)
		if err != nil || proc == nil {
			t.Fatalf("StartShellIn(%q): process=%v err=%v", dir, proc, err)
		}
		spawner.mu.Lock()
		got := spawner.specs[len(spawner.specs)-1].dir
		spawner.mu.Unlock()
		if got != want {
			t.Fatalf("StartShellIn(%q) spawn dir=%q want=%q", dir, got, want)
		}
		child := spawner.child(t)
		_ = proc.Terminate(context.Background())
		child.closeOutputs()
		_, _ = proc.Wait(context.Background())
		_ = proc.Release(context.Background())
	}

	refused := []string{"link-out", "src/link-out", "link-sibling", "missing", "file.txt", "", "/etc", "../work-evil", "../work", "src/../..", "src//app", "src/./app", "src/app/"}
	for _, dir := range refused {
		before := spawner.count()
		proc, err := r.StartShellIn(context.Background(), grant, dir, nil)
		if proc != nil || !errors.Is(err, agentexec.ErrNotStarted) || spawner.count() != before {
			t.Errorf("StartShellIn(%q): process=%v err=%v spawns=%d want=%d", dir, proc, err, spawner.count(), before)
		}
	}
	// A refused directory uses up the one approved run, so the person at the
	// Mac must be able to see why nothing started.
	if got := diagnostics.Failure(); got != StartFailureWorkDir {
		t.Fatalf("refused directory left diagnostic %v", got)
	}

	before := spawner.count()
	proc, err := r.StartShell(context.Background(), grant, nil)
	if err != nil || proc == nil {
		t.Fatalf("StartShell: process=%v err=%v", proc, err)
	}
	spawner.mu.Lock()
	got := spawner.specs[len(spawner.specs)-1].dir
	spawner.mu.Unlock()
	if got != cfg.WorkDir || spawner.count() != before+1 {
		t.Fatalf("StartShell dir=%q want original Config.WorkDir %q", got, cfg.WorkDir)
	}
	child := spawner.child(t)
	_ = proc.Terminate(context.Background())
	child.closeOutputs()
	_, _ = proc.Wait(context.Background())
	_ = proc.Release(context.Background())
}
