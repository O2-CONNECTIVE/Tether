package maclaunch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// dirRuntime records the directory a start was asked for and never spawns.
type dirRuntime struct {
	neverRuntime
	mu   sync.Mutex
	dirs []string
}

func (r *dirRuntime) StartShellIn(_ context.Context, _ supervisor.Binding, dir string, _ []byte) (agentexec.GuestProcess, error) {
	r.mu.Lock()
	r.dirs = append(r.dirs, dir)
	r.mu.Unlock()
	r.starts.Add(1)
	return nil, agentexec.ErrNotStarted
}

// The resident path has no person approving each command: the gateway's
// signature over the command hash, directory and timeout is the only thing
// that says which command may run. The native core must hold a worker to
// exactly those three values.
func TestResidentNativeCoreHoldsCommandRunToItsSignedScope(t *testing.T) {
	f, _ := fixedNativeFixture(t)
	rt := &dirRuntime{}
	f.deps.runtime = func(macagent.Config) (agentexec.ProfiledRuntime, error) { return rt, nil }
	s, e := f.openSource(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	const command = "make test\n"
	sum := sha256.Sum256([]byte(command))
	fixed := agentexec.FixedStartConstraint{RequestID: agenttransport.ResidentRunOperation, CommandSHA256: hex.EncodeToString(sum[:]), Dir: "src/app", TimeoutMillis: 60_000}
	outer := "0123456789abcdef0123456789abcdef"
	entry := &residentEntry{}
	s.entries[outer] = entry
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	owned, e := s.approveWithID(ctx, ctx, entry, outer, fixed, time.Now().Add(8*time.Second))
	if e != nil || owned == nil {
		t.Fatal(e)
	}
	defer closeResidentTest(t, owned)
	w := owned.Session.(*ResidentWorker)
	req := agentexec.StartRequest{Version: agentexec.Version, Binding: w.approved.session.Binding(), Mode: agentexec.ModeFull, RequestID: agenttransport.ResidentRunOperation, Command: []byte(command), TimeoutMillis: 60_000}
	for kind, edit := range map[string]func(*agentexec.StartRequest){
		"outer correlation as id": func(r *agentexec.StartRequest) { r.RequestID = outer },
		"fixed-work id":           func(r *agentexec.StartRequest) { r.RequestID = ResidentFixedRequestID },
		"another command":         func(r *agentexec.StartRequest) { r.Command = []byte("make test") },
		"another timeout":         func(r *agentexec.StartRequest) { r.TimeoutMillis++ },
	} {
		changed := req
		edit(&changed)
		if _, e = w.approved.session.Start(changed); !errors.Is(e, agentexec.ErrPolicy) {
			t.Fatal(kind, e)
		}
	}
	if rt.starts.Load() != 0 {
		t.Fatal("a request outside the signed scope reached spawn")
	}
	if _, e = w.approved.session.Start(req); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for rt.starts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	rt.mu.Lock()
	dirs := append([]string(nil), rt.dirs...)
	rt.mu.Unlock()
	if len(dirs) != 1 || dirs[0] != "src/app" {
		t.Fatalf("signed directory did not reach the runtime: %q", dirs)
	}
}
