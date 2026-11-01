package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"strings"
	"testing"
)

type sealedOwner struct {
	*fakeOwner
	sealErr error
}

func (o *sealedOwner) RevokeTerminalUnknown() error { o.r.log("terminal-revoke"); return o.sealErr }

type stoppedSession struct {
	*fakeSession
	stopErr error
}

func (s *stoppedSession) StopTerminalUnknown(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		s.r.t.Error("unbounded transport seal")
	}
	s.r.log("terminal-stop")
	return s.stopErr
}
func TestTerminalUnknownAllCallerPaths(t *testing.T) {
	for _, path := range []string{"finish", "partial-approval", "client-failure"} {
		t.Run(path, func(t *testing.T) {
			r := newRecorder(t, encode(t, macDoc(t)))
			owner := &sealedOwner{fakeOwner: r.owner}
			client := &stoppedSession{fakeSession: r.session}
			r.owner.closeErr = errors.New("private owner error")
			r.session.closeErr = errors.New("private cleanup error")
			r.approval.owner = owner
			d := r.deps()
			d.newClient = func(agenttransport.ClientConfig, agenttransport.Owner) (session, error) {
				r.log("client")
				if path == "client-failure" {
					return nil, agenttransport.ErrConfig
				}
				return client, nil
			}
			if path == "partial-approval" {
				r.approval.err = errors.New("partial")
			}
			var out bytes.Buffer
			code := execute(context.Background(), []string{"-config", configPath}, &out, d)
			if code != 1 || strings.Count(out.String(), "TERMINAL_CLEANUP_UNKNOWN") != 1 || strings.Contains(out.String(), "private") {
				t.Fatal(code, out.String())
			}
			if r.prepared.Runtime.TerminalDisposition == nil || !r.prepared.Runtime.TerminalDisposition.IsTerminal() {
				t.Fatal("no terminal disposition")
			}
			trace := r.trace()
			if !strings.Contains(trace, "terminal-revoke") {
				t.Fatal(trace)
			}
			if path == "finish" && !strings.HasSuffix(trace, "client-close terminal-revoke terminal-stop") {
				t.Fatal(trace)
			}
			if path != "finish" && !strings.HasSuffix(trace, "owner-close owner-close terminal-revoke") {
				t.Fatal(trace)
			}
		})
	}
}
func TestTerminalUnknownSealFailuresStillNonzeroAndNoSuccess(t *testing.T) {
	for _, fail := range []string{"revoke", "transport"} {
		r := newRecorder(t, encode(t, macDoc(t)))
		r.session.closeErr = errors.New("unknown")
		o := &sealedOwner{fakeOwner: r.owner}
		c := &stoppedSession{fakeSession: r.session}
		if fail == "revoke" {
			o.sealErr = errors.New("refused")
		} else {
			c.stopErr = errors.New("refused")
		}
		r.approval.owner = o
		d := r.deps()
		d.newClient = func(agenttransport.ClientConfig, agenttransport.Owner) (session, error) { return c, nil }
		var out bytes.Buffer
		code := execute(context.Background(), []string{"-config", configPath}, &out, d)
		if code != 1 || !r.prepared.Runtime.TerminalDisposition.IsTerminal() || !strings.Contains(out.String(), "TERMINAL_CLEANUP_UNKNOWN") || strings.Contains(out.String(), "接続を終了しました") {
			t.Fatal(code, out.String())
		}
	}
}
func TestCleanCallerDoesNotRelinquishUnknown(t *testing.T) {
	r := newRecorder(t, encode(t, macDoc(t)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, out := run(t, ctx, r)
	if code != 0 || strings.Contains(out, "TERMINAL_CLEANUP_UNKNOWN") || r.prepared.Runtime.TerminalDisposition.IsTerminal() {
		t.Fatal(code, out)
	}
}
