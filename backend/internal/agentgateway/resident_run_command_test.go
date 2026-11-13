package agentgateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

func runCommandRig(t *testing.T) (*Gateway, *residentSession, *residentControlFixture) {
	t.Helper()
	g, s, _, control := residentEvidenceRig(t)
	t.Cleanup(func() {
		select {
		case <-control.begin:
		default:
			close(control.begin)
		}
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = g.Close(ctx)
	})
	return g, s, control
}

// The Mac runs only what the gateway signs. The signed scope must therefore be
// exactly the command, directory and timeout this caller asked for, and one
// request id must never come to mean a second, different command.
func TestRunCommandSignsExactlyTheRequestedScopeOncePerRequestID(t *testing.T) {
	g, s, control := runCommandRig(t)
	const command, cwd, timeout = "make test\n", "src/app", uint32(90000)
	id := strings.Repeat("3", 32)
	before := time.Now()
	v := s.RunCommand(context.Background(), id, command, cwd, timeout)
	after := time.Now()
	if v["error"] != "" || v["phase"] != "pending" || v["request_id"] != id {
		t.Fatal("run command was not admitted", v)
	}
	var call residentRunCall
	select {
	case call = <-control.runs:
	case <-time.After(time.Second):
		t.Fatal("no command-run intent was requested")
	}
	// SHA-256 of "make test\n", computed outside this code.
	want := ResidentRunScope{CommandSHA256: "cfa5c4526386239681bc6cdb4563c86551da2ef1325ecc9c20b48ae2cf97df9a", Cwd: cwd, TimeoutMillis: timeout}
	if call.scope != want || call.id != id || call.generation != s.generation {
		t.Fatalf("signed scope %+v for %s; want %+v", call.scope, call.id, want)
	}
	// The whole work, not only the command, ends within the requested timeout:
	// the Mac refuses an intent whose deadline lies further away than that.
	limit := time.Duration(timeout) * time.Millisecond
	if call.deadline.Before(before.Add(limit).Truncate(time.Millisecond)) || call.deadline.After(after.Add(limit)) || call.deadline.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatal("work deadline is not the requested timeout from now", call.deadline.Sub(before))
	}
	g.mu.Lock()
	b := s.entry.works[id].binding
	g.mu.Unlock()
	// The Mac's registration and collection proofs are checked against this record.
	if b.Run != want || b.DeadlineMillis != call.deadline.UnixMilli() {
		t.Fatalf("work record %+v does not carry the signed scope", b)
	}
	if again := s.RunCommand(context.Background(), id, command, cwd, timeout); again["error"] != "" || again["request_id"] != id {
		t.Fatal("the same request did not observe the same work", again)
	}
	for name, v := range map[string]map[string]any{
		"another command":   s.RunCommand(context.Background(), id, "make test \n", cwd, timeout),
		"another directory": s.RunCommand(context.Background(), id, command, "src", timeout),
		"another timeout":   s.RunCommand(context.Background(), id, command, cwd, timeout+1),
	} {
		if v["error"] != "request_scope_mismatch" {
			t.Fatalf("%s reused the request id: %v", name, v)
		}
	}
	// One work per session at a time, as for the fixed routine.
	if v := s.RunCommand(context.Background(), strings.Repeat("4", 32), command, cwd, timeout); v["error"] != "busy_or_refused" {
		t.Fatal("a second command ran beside the first", v)
	}
	select {
	case extra := <-control.runs:
		t.Fatal("a repeated or refused request signed another intent", extra)
	default:
	}
	// The existing tools observe and stop a command run by its request id.
	if v := s.FixedStatus(context.Background(), id); v["error"] != "" || v["request_id"] != id {
		t.Fatal("status cannot observe the command run", v)
	}
	if v := s.FixedCancel(context.Background(), id); v["error"] != "" || v["request_id"] != id {
		t.Fatal("cancel cannot reach the command run", v)
	}
	g.mu.Lock()
	cancelled := s.entry.works[id].cancelled
	g.mu.Unlock()
	if !cancelled {
		t.Fatal("cancel did not mark the command run")
	}
}

// Releasing a result leaves only a replay record. That record must still say
// which command it was, or a later request for another command under the same
// id would be answered with the old command's finished result.
func TestRunCommandKeepsItsScopeAfterTheResultIsReleased(t *testing.T) {
	g, s, control := runCommandRig(t)
	id := strings.Repeat("6", 32)
	if v := s.RunCommand(context.Background(), id, "make test\n", ".", 60000); v["error"] != "" {
		t.Fatal(v)
	}
	g.mu.Lock()
	w := s.entry.works[id]
	g.mu.Unlock()
	close(control.begin) // the Mac refuses without taking the work
	select {
	case <-w.completed:
	case <-time.After(5 * time.Second):
		t.Fatal("refused work never finished")
	}
	if v := s.FixedRelease(context.Background(), id); v["result_released"] != true {
		t.Fatal("finished work was not released", v)
	}
	for name, v := range map[string]map[string]any{
		"another command":   s.RunCommand(context.Background(), id, "make deploy\n", ".", 60000),
		"another directory": s.RunCommand(context.Background(), id, "make test\n", "src", 60000),
		"another timeout":   s.RunCommand(context.Background(), id, "make test\n", ".", 60001),
	} {
		if v["error"] != "request_scope_mismatch" {
			t.Fatalf("%s was answered with the released result: %v", name, v)
		}
	}
	if v := s.RunCommand(context.Background(), id, "make test\n", ".", 60000); v["result_released"] != true || v["request_id"] != id {
		t.Fatal("the same command no longer observes its released record", v)
	}
	// The refused work kept no command text.
	g.mu.Lock()
	kept := w.command
	g.mu.Unlock()
	if kept != nil {
		t.Fatal("finished work still holds the command text")
	}
}

// A scope the Mac would refuse must be refused before any work exists.
// Otherwise one bad argument would leave the session busy with work that can
// never be collected.
func TestRunCommandRefusesMalformedScopeWithoutSpendingTheSession(t *testing.T) {
	g, s, control := runCommandRig(t)
	id := strings.Repeat("5", 32)
	type args struct {
		id, command, cwd string
		timeout          uint32
	}
	good := args{id, "git status\n", ".", 60000}
	for name, edit := range map[string]func(*args){
		"empty command":               func(a *args) { a.command = "" },
		"oversized command":           func(a *args) { a.command = strings.Repeat("a", agentwire.MaxCommandBytes+1) },
		"NUL in command":              func(a *args) { a.command = "git\x00status" },
		"malformed request id":        func(a *args) { a.id = "5" },
		"empty directory":             func(a *args) { a.cwd = "" },
		"absolute directory":          func(a *args) { a.cwd = "/etc" },
		"parent directory":            func(a *args) { a.cwd = "../other" },
		"unclean directory":           func(a *args) { a.cwd = "src//app" },
		"control in directory":        func(a *args) { a.cwd = "src\napp" },
		"oversized directory":         func(a *args) { a.cwd = strings.Repeat("a", 1025) },
		"line separator in directory": func(a *args) { a.cwd = "src\u2028app" },
		"zero timeout":                func(a *args) { a.timeout = 0 },
		// Shorter than the fixed routine's whole budget, the work would expire
		// before the Mac could answer and leave the session busy for good.
		"timeout below the floor": func(a *args) { a.timeout = 14999 },
		"timeout above the limit": func(a *args) { a.timeout = 240001 },
	} {
		a := good
		edit(&a)
		if v := s.RunCommand(context.Background(), a.id, a.command, a.cwd, a.timeout); v["error"] != "invalid_run_scope" {
			t.Fatalf("%s: %v", name, v)
		}
	}
	g.mu.Lock()
	works, current := len(s.entry.works), s.entry.current
	g.mu.Unlock()
	if works != 0 || current != nil {
		t.Fatal("a refused scope left work behind", works)
	}
	select {
	case call := <-control.runs:
		t.Fatal("a refused scope was sent for signing", call)
	default:
	}
	if v := s.RunCommand(context.Background(), good.id, good.command, good.cwd, 15000); v["error"] != "" || v["phase"] != "pending" {
		t.Fatal("the session no longer admits a valid command", v)
	}
}
