package agentwire_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

var sharedProfiles = []agentexec.Profile{agentexec.ProfileMacCurrentUser, agentexec.ProfileWindowsGuest}

func TestSharedContractCancelKeepsAcceptanceSeparateFromCleanup(t *testing.T) {
	for _, profile := range sharedProfiles {
		t.Run(agentwire.ProfileName(profile), func(t *testing.T) {
			r := newContractRig(t, profile)
			p := r.prepare()
			ctx := finite(t)
			st, err := r.client.Start(ctx, "same-request", []byte("fixture command"), 60_000)
			if err != nil {
				t.Fatal(err)
			}
			contractAwait(t, p.startEntered)
			r.assertViews(st, 1, 0, 0, agentexec.CleanupPending, false)
			if st.Phase != agentexec.PhaseStarting || st.Terminal || st.Exit.Known {
				t.Fatalf("pending start became completion: %+v", st)
			}
			again, err := r.client.Start(ctx, "same-request", []byte("fixture command"), 60_000)
			if err != nil || again.CommandID != st.CommandID || r.runtime.starts.Load() != 1 {
				t.Fatalf("replay acquired another process: %+v %v", again, err)
			}
			if _, err := r.client.Start(ctx, "same-request", []byte("changed command"), 60_000); !errors.Is(err, agentexec.ErrRequestMismatch) {
				t.Fatalf("changed payload replay: %v", err)
			}
			p.allowStart()
			st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.Phase == agentexec.PhaseRunning })
			r.assertViews(st, 0, 1, 0, agentexec.CleanupPending, false)
			p.events <- event{stream: agentexec.Stdout, data: []byte("out")}
			p.events <- event{stream: agentexec.Stderr, data: []byte{0xff, 0}}
			st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.OutputEnd == 5 })
			accepted, err := r.client.Cancel(ctx, st.CommandID)
			if err != nil || accepted.Stop != agentexec.StopCanceled || accepted.Terminal || accepted.Cleanup != agentexec.CleanupPending {
				t.Fatalf("cancel acceptance pretended to confirm cleanup: %+v %v", accepted, err)
			}
			contractAwait(t, p.releaseEntered)
			st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.Stop == agentexec.StopCanceled })
			r.assertViews(st, 0, 0, 1, agentexec.CleanupPending, false)
			if _, err := r.client.Start(ctx, "replacement", []byte("next"), 60_000); !errors.Is(err, agentexec.ErrBusy) {
				t.Fatalf("replacement admitted before original release: %v", err)
			}
			p.allowRelease()
			st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.Terminal })
			r.assertViews(st, 0, 0, 0, agentexec.CleanupConfirmed, false)
			if st.Phase != agentexec.PhaseCanceled || st.Exit.Known || p.releases.Load() != 1 || !r.issuer.Snapshot().HasAuthority {
				t.Fatalf("command cancel changed grant/exit/cleanup semantics: %+v", st)
			}
			out, err := r.client.Output(ctx, st.CommandID, 0, 1024)
			if err != nil || !out.Complete || out.Next != 5 || len(out.Chunks) != 2 || out.Chunks[0].Stream != agentexec.Stdout || string(out.Chunks[0].Data) != "out" || out.Chunks[1].Stream != agentexec.Stderr || out.Chunks[1].Offset != 3 || !bytes.Equal(out.Chunks[1].Data, []byte{0xff, 0}) {
				t.Fatalf("same command output changed across cancellation: %+v %v", out, err)
			}
		})
	}
}

func TestSharedContractNaturalExitDoesNotHideUnknownCleanup(t *testing.T) {
	for _, profile := range sharedProfiles {
		for _, unknown := range []bool{false, true} {
			name := agentwire.ProfileName(profile) + "/confirmed"
			if unknown {
				name = agentwire.ProfileName(profile) + "/unknown"
			}
			t.Run(name, func(t *testing.T) {
				r := newContractRig(t, profile)
				p := r.prepare()
				p.releaseFails.Store(unknown)
				p.allowStart()
				close(p.events) // natural EOF, followed by the fixture's known exit 7
				ctx := finite(t)
				st, err := r.client.Start(ctx, "natural", []byte("fixture command"), 60_000)
				if err != nil {
					t.Fatal(err)
				}
				contractAwait(t, p.releaseEntered)
				st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.Exit.Known })
				r.assertViews(st, 0, 0, 1, agentexec.CleanupPending, false)
				if st.Terminal || st.Exit.Code != 7 || st.Stop != agentexec.StopNone {
					t.Fatalf("natural exit pretended to finish release: %+v", st)
				}
				p.allowRelease()
				st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.Terminal })
				wantCleanup := agentexec.CleanupConfirmed
				if unknown {
					wantCleanup = agentexec.CleanupUnknown
				}
				r.assertViews(st, 0, 0, 0, wantCleanup, false)
				if st.Phase != agentexec.PhaseExited || !st.Exit.Known || st.Exit.Code != 7 || st.Stop != agentexec.StopNone {
					t.Fatalf("cleanup overwrote natural result: %+v", st)
				}
				if unknown {
					if _, err := r.client.Start(ctx, "next", []byte("next"), 60_000); !errors.Is(err, agentexec.ErrCleanupUnknown) {
						t.Fatalf("unknown cleanup admitted replacement: %v", err)
					}
					for _, call := range []func() (agentwire.Status, error){
						func() (agentwire.Status, error) { return r.client.Status(ctx, st.CommandID) },
						func() (agentwire.Status, error) { return r.client.Cancel(ctx, st.CommandID) },
						func() (agentwire.Status, error) {
							return r.client.Start(ctx, "natural", []byte("fixture command"), 60_000)
						},
					} {
						got, err := call()
						if err != nil || got.Status != st.Status {
							t.Fatalf("observation/replay rewrote unknown result: %+v %v", got, err)
						}
					}
					if r.runtime.starts.Load() != 1 || p.releases.Load() != 1 {
						t.Fatal("wire observation/replay retried execution or cleanup")
					}
					// Only the existing trusted core cleanup API retries the retained
					// process. There is deliberately no remote cleanup operation.
					p.releaseFails.Store(false)
					got, err := r.session.Cleanup(ctx, agentexec.CleanupRequest{Version: agentexec.Version, Binding: r.session.Binding(), CommandID: st.CommandID})
					if err != nil || got.Cleanup != agentexec.CleanupConfirmed || got.Exit != st.Exit || p.releases.Load() != 2 {
						t.Fatalf("same-owner cleanup lost the original result: %+v %v", got, err)
					}
					st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.Cleanup == agentexec.CleanupConfirmed })
					r.assertViews(st, 0, 0, 0, agentexec.CleanupConfirmed, false)
				}
				// Confirmed command cleanup permits another command inside the same
				// still-live grant. It does not create or extend a grant.
				next := r.prepare()
				next.allowStart()
				next.allowRelease()
				close(next.events)
				before := r.session.Grant()
				fresh, err := r.client.Start(ctx, "next", []byte("next"), 60_000)
				if err != nil || fresh.CommandID == st.CommandID {
					t.Fatalf("confirmed cleanup did not permit fresh work: %+v %v", fresh, err)
				}
				r.status(fresh.CommandID, func(s agentwire.Status) bool { return s.Terminal })
				if r.runtime.starts.Load() != 2 || r.session.Grant() != before {
					t.Fatal("fresh command reused execution or changed the grant")
				}
			})
		}
	}
}

func TestSharedContractLiveTransportCannotRenewSingleUseAuthority(t *testing.T) {
	for _, profile := range sharedProfiles {
		for _, end := range []string{"revoke", "deadline"} {
			t.Run(agentwire.ProfileName(profile)+"/"+end, func(t *testing.T) {
				r := newContractRig(t, profile)
				p := r.prepare()
				p.allowStart()
				p.allowRelease()
				ctx := finite(t)
				st, err := r.client.Start(ctx, "same-request", []byte("fixture command"), 60_000)
				if err != nil {
					t.Fatal(err)
				}
				r.status(st.CommandID, func(s agentwire.Status) bool { return s.Phase == agentexec.PhaseRunning })
				before := r.session.Grant()
				wantStop := agentexec.StopRevoked
				if end == "deadline" {
					r.clock.Advance(2 * time.Hour)
					r.session.ObserveDeadline()
					wantStop = agentexec.StopHardDeadline
				} else {
					r.session.Revoke()
				}
				st = r.status(st.CommandID, func(s agentwire.Status) bool { return s.Terminal })
				r.assertViews(st, 0, 0, 0, agentexec.CleanupConfirmed, true)
				if st.Stop != wantStop || st.Phase != agentexec.PhaseCanceled || st.Exit.Known || r.issuer.Snapshot().HasAuthority {
					t.Fatalf("grant termination changed meaning: %+v", st)
				}
				again, err := r.client.Start(ctx, "same-request", []byte("fixture command"), 60_000)
				if err != nil || again.Status != st.Status {
					t.Fatalf("historical replay lost original result: %+v %v", again, err)
				}
				if _, err := r.client.Start(ctx, "new-request", []byte("new"), 60_000); !errors.Is(err, agentexec.ErrDenied) {
					t.Fatalf("live transport renewed ended authority: %v", err)
				}
				if _, err := r.client.Output(ctx, st.CommandID, 0, 1024); !errors.Is(err, agentexec.ErrDenied) {
					t.Fatalf("ended grant exposed retained output: %v", err)
				}
				if err := r.session.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := agentwire.NewServer(r.config()); !errors.Is(err, agentexec.ErrSessionClaimed) {
					t.Fatalf("close released the consumed adapter claim: %v", err)
				}
				if replacement, err := agentexec.New(agentexec.Config{Issuer: r.issuer, Authority: r.authority, Runtime: r.runtime, Clock: r.clock.Now, CleanupTimeout: time.Second}); replacement != nil || !errors.Is(err, agentexec.ErrDenied) {
					if replacement != nil {
						_ = replacement.Close(ctx)
					}
					t.Fatalf("new session revived consumed authority: %v", err)
				}
				if r.runtime.starts.Load() != 1 || r.session.Grant() != before {
					t.Fatal("replay/close changed execution count or immutable grant")
				}
				// The same lifecycle rules do not erase the OS-specific boundary.
				wantBoundary := supervisor.SeparatedNonAdminGuest
				if profile == agentexec.ProfileMacCurrentUser {
					wantBoundary = supervisor.CurrentUserNoIsolation
				}
				if before.GuestBoundary != wantBoundary {
					t.Fatalf("profile guarantee was silently unified: %v", before.GuestBoundary)
				}
			})
		}
	}
}

// A separate grant is a separate execution and observation scope, even when the
// client chooses the same idempotency key. Long-lived composition must retain
// this distinction instead of reusing an old Session or command mapping.
func TestSharedContractFreshGrantCannotAdoptPreviousExecution(t *testing.T) {
	for _, profile := range sharedProfiles {
		t.Run(agentwire.ProfileName(profile), func(t *testing.T) {
			old, fresh := newContractRig(t, profile), newContractRig(t, profile)
			ctx := finite(t)
			var ids []string
			for _, r := range []*contractRig{old, fresh} {
				p := r.prepare()
				p.allowStart()
				p.allowRelease()
				st, err := r.client.Start(ctx, "same-request", []byte("fixture command"), 60_000)
				if err != nil {
					t.Fatal(err)
				}
				r.status(st.CommandID, func(s agentwire.Status) bool { return s.Phase == agentexec.PhaseRunning })
				ids = append(ids, st.CommandID)
			}
			if old.session.Binding() == fresh.session.Binding() || ids[0] == ids[1] {
				t.Fatal("fresh grant reused execution identity")
			}
			for n, r := range []*contractRig{old, fresh} {
				foreign := ids[1-n]
				if _, err := r.client.Status(ctx, foreign); !errors.Is(err, agentexec.ErrNotFound) {
					t.Fatalf("another grant's status was visible: %v", err)
				}
				if _, err := r.client.Cancel(ctx, foreign); !errors.Is(err, agentexec.ErrNotFound) {
					t.Fatalf("another grant's command was cancelable: %v", err)
				}
				if _, err := r.client.Output(ctx, foreign, 0, 1024); !errors.Is(err, agentexec.ErrNotFound) {
					t.Fatalf("another grant's output was visible: %v", err)
				}
			}
			if err := old.session.Close(ctx); err != nil {
				t.Fatal(err)
			}
			stillRunning := fresh.status(ids[1], func(s agentwire.Status) bool { return s.Phase == agentexec.PhaseRunning })
			fresh.assertViews(stillRunning, 0, 1, 0, agentexec.CleanupPending, false)
			if !fresh.issuer.Snapshot().HasAuthority || fresh.runtime.starts.Load() != 1 || old.runtime.starts.Load() != 1 {
				t.Fatal("old grant close changed fresh grant or replayed work")
			}
			if err := fresh.session.Close(finite(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
