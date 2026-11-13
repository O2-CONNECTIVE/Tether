package agenttransport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const residentRunWireHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func residentRunWireDeadline() time.Time {
	return time.Now().Add(2 * time.Second).Truncate(time.Millisecond)
}

// The Mac can only compare the command and directory it is asked to run against
// what the gateway signed if that scope survives the wire. Fixed and run
// intents also share one sequence and one replay set, so neither kind can be
// used to replay or reorder the other.
func TestResidentRunIntentReachesHandedOffSourceWithSignedScope(t *testing.T) {
	r := connectorRig(t)
	done := residentRun(r.c)
	s := takeSource(t, r)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	result := make(chan error, 1)
	runID, fixedID := fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2)
	go func() {
		result <- r.h.ProposeResidentRun(ctx, hostLabel, member1, runID, residentRunWireDeadline(), residentRunWireHash, "src/app", 60000)
	}()
	i, e := s.Next()
	if e != nil {
		t.Fatal(e)
	}
	if i.Operation() != ResidentRunOperation || i.CommandSHA256() != residentRunWireHash || i.Cwd() != "src/app" || i.TimeoutMillis() != 60000 || i.RequestID() != runID {
		t.Fatalf("run intent lost its signed scope: %q %q %q %d %q", i.Operation(), i.CommandSHA256(), i.Cwd(), i.TimeoutMillis(), i.RequestID())
	}
	if i.Receiver() != s.Receiver() || i.OriginalContext() == nil || i.OriginalContext().Err() != nil {
		t.Fatal("run intent lost the observed original binding")
	}
	if e = s.Refuse(i); e != nil {
		t.Fatal(e)
	}
	if e = await(t, result, 3*time.Second); !errors.Is(e, ErrWorkUnconfigured) {
		t.Fatal("refused run intent did not fail closed", e)
	}
	// The same link carries the next sequence as a fixed intent.
	go func() {
		result <- r.h.ProposeResidentFixed(ctx, hostLabel, member1, fixedID, residentRunWireDeadline())
	}()
	if i, e = s.Next(); e != nil {
		t.Fatal("fixed intent after a run intent ended the link", e)
	}
	if i.Operation() != ResidentFixedOperation || i.TimeoutMillis() != 15000 || i.CommandSHA256() != "" || i.Cwd() != "" || i.RequestID() != fixedID {
		t.Fatal("fixed intent after a run intent carries a run scope")
	}
	if e = s.Refuse(i); e != nil {
		t.Fatal(e)
	}
	if e = await(t, result, 3*time.Second); !errors.Is(e, ErrWorkUnconfigured) {
		t.Fatal(e)
	}
	// A request id spent by either kind is spent for both.
	if e = r.h.ProposeResidentRun(ctx, hostLabel, member1, fixedID, residentRunWireDeadline(), residentRunWireHash, ".", 60000); !errors.Is(e, ErrRefused) {
		t.Fatal("run intent reused a fixed request id", e)
	}
	if e = r.h.ProposeResidentFixed(ctx, hostLabel, member1, runID, residentRunWireDeadline()); !errors.Is(e, ErrRefused) {
		t.Fatal("fixed intent reused a run request id", e)
	}
	r.c.Stop()
	if e = finishSource(t, s, nil); e != nil {
		t.Fatal(e)
	}
	if e = await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

// A Mac that only keeps the connection has nothing that could run a command.
// It must answer a run intent exactly like a fixed one: signed refusal, link kept.
func TestResidentRunIntentIsRefusedByConnectionOnlyMac(t *testing.T) {
	r := newResidentRig(t, nil)
	residentRun(r.c)
	residentReady(t, r)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e := r.h.ProposeResidentRun(ctx, hostLabel, member1, fmt.Sprintf("%032x", 1), residentRunWireDeadline(), residentRunWireHash, ".", 60000); !errors.Is(e, ErrWorkUnconfigured) {
		t.Fatal("run intent did not fail closed", e)
	}
	if e := r.h.ProposeResidentFixed(ctx, hostLabel, member1, fmt.Sprintf("%032x", 2), residentRunWireDeadline()); !errors.Is(e, ErrWorkUnconfigured) {
		t.Fatal("run refusal ended the link", e)
	}
	if !r.c.Connected() || !r.h.ResidentConnected(hostLabel, member1) || r.rec.count.Load() != 0 {
		t.Fatal("run refusal changed authority or ended idle control")
	}
}

// A caller mistake must not cost the link or a sequence number, and must never
// fall through to the fixed operation: the gateway refuses before it signs.
func TestResidentRunProposalOutsideScopeIsNotSentAndKeepsTheLink(t *testing.T) {
	r := newResidentRig(t, nil)
	residentRun(r.c)
	residentReady(t, r)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	id := fmt.Sprintf("%032x", 1)
	for name, c := range map[string]struct {
		hash, cwd string
		timeout   uint32
		deadline  time.Time
	}{
		"cwd leaves the work root":   {residentRunWireHash, "../x", 60000, residentRunWireDeadline()},
		"absolute cwd":               {residentRunWireHash, "/etc", 60000, residentRunWireDeadline()},
		"malformed hash":             {strings.ToUpper(residentRunWireHash), ".", 60000, residentRunWireDeadline()},
		"timeout over the bound":     {residentRunWireHash, ".", uint32(ResidentRunMaxTimeout/time.Millisecond) + 1, residentRunWireDeadline()},
		"deadline after the timeout": {residentRunWireHash, ".", 1000, residentRunWireDeadline()},
		"empty scope":                {"", "", 0, residentRunWireDeadline()},
	} {
		t.Run(name, func(t *testing.T) {
			if e := r.h.ProposeResidentRun(ctx, hostLabel, member1, id, c.deadline, c.hash, c.cwd, c.timeout); !errors.Is(e, ErrRefused) {
				t.Fatal(e)
			}
		})
	}
	// Nothing was signed or sent: the same request id and the link are intact.
	if e := r.h.ProposeResidentRun(ctx, hostLabel, member1, id, residentRunWireDeadline(), residentRunWireHash, ".", 60000); !errors.Is(e, ErrWorkUnconfigured) {
		t.Fatal("refused proposals spent the request id or the link", e)
	}
	if !r.c.Connected() || !r.h.ResidentConnected(hostLabel, member1) {
		t.Fatal("refused proposals ended the link")
	}
}

// The gateway refuses a reused request id before it signs, so the Mac's own
// replay set is only reachable with the gateway key itself. A holder of that
// key must not replay a request id by switching the intent kind.
func TestResidentReceiverSharesReplaySetAcrossIntentKinds(t *testing.T) {
	id := fmt.Sprintf("%032x", 1)
	for name, c := range map[string]struct {
		first  func(*residentRig, context.Context) error
		replay func(residentBinding, ResidentFixedIntent, ssh.Signer) ([]byte, error)
		scope  residentRunScope
	}{
		"fixed id replayed as run": {
			func(r *residentRig, ctx context.Context) error {
				return r.h.ProposeResidentFixed(ctx, hostLabel, member1, id, residentRunWireDeadline())
			},
			residentEncodeRun, residentRunScope{commandSHA256: residentRunWireHash, cwd: ".", timeoutMillis: 60000},
		},
		"run id replayed as fixed": {
			func(r *residentRig, ctx context.Context) error {
				return r.h.ProposeResidentRun(ctx, hostLabel, member1, id, residentRunWireDeadline(), residentRunWireHash, ".", 60000)
			},
			residentEncodeIntent, residentRunScope{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newResidentRig(t, nil)
			done := residentRun(r.c)
			residentReady(t, r)
			ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			if e := c.first(r, ctx); !errors.Is(e, ErrWorkUnconfigured) {
				t.Fatal(e)
			}
			r.h.mu.Lock()
			p := r.h.residents[residentMapKey(hostLabel, member1)]
			r.h.mu.Unlock()
			msg, e := c.replay(p.binding, ResidentFixedIntent{generation: p.binding.generation, sequence: 2, requestID: id, deadline: residentRunWireDeadline(), run: c.scope}, p.signer)
			if e != nil {
				t.Fatal(e)
			}
			if e = residentWrite(ctx, p.ws, msg); e != nil {
				t.Fatal(e)
			}
			if e = await(t, done, 3*time.Second); !errors.Is(e, ErrRefused) || r.m.trips.Load() != 1 || r.rec.count.Load() != 0 {
				t.Fatal("request id replayed across intent kinds", e)
			}
		})
	}
}
