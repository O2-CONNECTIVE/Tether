//go:build !windows

package macagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// The gateway signs only a hash, so the person at the Mac can see what an
// agent ran only through this record. A command that cannot be recorded must
// not start: otherwise it would run without anyone at the Mac able to see it.
func TestStartShellInRecordsTheCommandBeforeItRuns(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(base, "run-history.jsonl")
	spawner := newFakeSpawner()
	cfg := Config{WorkDir: work, Home: base, RunLog: log, TermGrace: time.Millisecond, KillWait: time.Millisecond, IOWait: time.Millisecond}
	r, err := newRuntime(cfg, spawner.spawn, spawner.checks(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	grant := supervisor.Binding{Generation: 1}
	finish := func(proc agentexec.GuestProcess) {
		child := spawner.child(t)
		_ = proc.Terminate(context.Background())
		child.closeOutputs()
		_, _ = proc.Wait(context.Background())
		_ = proc.Release(context.Background())
	}
	type entry struct {
		At      string `json:"at"`
		Dir     string `json:"dir"`
		Command string `json:"command"`
	}
	read := func() []entry {
		t.Helper()
		f, err := os.Open(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		var out []entry
		s := bufio.NewScanner(f)
		for s.Scan() {
			var e entry
			if err := json.Unmarshal(s.Bytes(), &e); err != nil {
				t.Fatalf("record is not one JSON object per line: %q %v", s.Text(), err)
			}
			out = append(out, e)
		}
		return out
	}

	before := time.Now().Add(-time.Second)
	const command = "make test\necho \"done\"\n"
	proc, err := r.StartShellIn(context.Background(), grant, "src", []byte(command))
	if err != nil || proc == nil {
		t.Fatal(proc, err)
	}
	finish(proc)
	got := read()
	if len(got) != 1 || got[0].Command != command || got[0].Dir != filepath.Join(root, "src") {
		t.Fatalf("record does not show what ran and where: %+v", got)
	}
	if at, err := time.Parse(time.RFC3339, got[0].At); err != nil || at.Before(before) || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("record time %q: %v", got[0].At, err)
	}
	if info, err := os.Stat(log); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record is readable by others: %v %v", info, err)
	}

	// A second run is appended; earlier records stay.
	proc, err = r.StartShellIn(context.Background(), grant, ".", []byte("pwd\n"))
	if err != nil {
		t.Fatal(err)
	}
	finish(proc)
	if got = read(); len(got) != 2 || got[0].Command != command || got[1].Command != "pwd\n" || got[1].Dir != root {
		t.Fatalf("records were not appended: %+v", got)
	}

	// Nothing that did not run as a command run is recorded: a refused
	// directory, and the fixed operation, which shows no client text.
	if proc, err := r.StartShellIn(context.Background(), grant, "missing", []byte("id\n")); proc != nil || !errors.Is(err, agentexec.ErrNotStarted) {
		t.Fatal(proc, err)
	}
	proc, err = r.StartShell(context.Background(), grant, []byte("fixed\n"))
	if err != nil {
		t.Fatal(err)
	}
	finish(proc)
	if got = read(); len(got) != 2 {
		t.Fatalf("recorded something that was not a command run: %+v", got)
	}

	// A full record is set aside, not emptied: emptying it would let a burst of
	// harmless commands erase the one that mattered.
	if err := os.WriteFile(log, append(make([]byte, 1<<20), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	proc, err = r.StartShellIn(context.Background(), grant, ".", []byte("ls\n"))
	if err != nil {
		t.Fatal(err)
	}
	finish(proc)
	if old, err := os.Stat(log + ".1"); err != nil || old.Size() != 1<<20+1 {
		t.Fatalf("earlier records were not kept: %v %v", old, err)
	}
	if info, err := os.Stat(log); err != nil || info.Size() > 4096 {
		t.Fatalf("record was not restarted: %v %v", info, err)
	}

	// A command runs as the same user and could replace the record with a link,
	// sending later records where the screen does not look. Such a record is
	// not a record: the next command must not start.
	linked := cfg
	linked.RunLog = filepath.Join(base, "linked.jsonl")
	if err := os.Symlink(filepath.Join(base, "elsewhere.jsonl"), linked.RunLog); err != nil {
		t.Fatal(err)
	}
	r3, err := newRuntime(linked, spawner.spawn, spawner.checks(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	linkSpawns := spawner.count()
	if proc, err := r3.StartShellIn(context.Background(), grant, "src", []byte("id\n")); proc != nil || !errors.Is(err, agentexec.ErrNotStarted) || spawner.count() != linkSpawns {
		t.Fatalf("command started with its record redirected: %v %v", proc, err)
	}

	// The record cannot be written: the command must not start.
	blocked := cfg
	blocked.RunLog = filepath.Join(base, "work") // a directory, never appendable
	r2, err := newRuntime(blocked, spawner.spawn, spawner.checks(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	spawns := spawner.count()
	if proc, err := r2.StartShellIn(context.Background(), grant, "src", []byte("id\n")); proc != nil || !errors.Is(err, agentexec.ErrNotStarted) || spawner.count() != spawns {
		t.Fatalf("command started without a record: %v %v spawns=%d", proc, err, spawner.count()-spawns)
	}
}
