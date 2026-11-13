//go:build darwin && cgo

package residentpipe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Real pipes and the shared idle owner only. No ON, native command, network,
// operational policy or producer child is started by these component tests.
func TestForegroundIdleControlAndClose(t *testing.T) {
	foregroundIdleControlAndClose(t, false)
}

func TestForegroundContextCancellationClosesSameOwner(t *testing.T) {
	foregroundIdleControlAndClose(t, true)
}

func foregroundIdleControlAndClose(t *testing.T, canceled bool) {
	t.Helper()
	owner := idleProtectedOwner(t)
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()
	p := &ownedPipes{int(inR.Fd()), int(outW.Fd())}
	if unix.SetNonblock(p.in, true) != nil || unix.SetNonblock(p.out, true) != nil {
		t.Fatal("test-owned pipe configuration failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// A nil heartbeat channel cannot publish. The second frame must therefore
	// come from the real status dispatch on the same original idle owner.
	go func() { done <- serveLoop(ctx, owner, p, foregroundInput, nil) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("original control loop not joined")
			}
		}
	})
	reader := bufio.NewReader(outR)
	read := func(v any) {
		t.Helper()
		if err := outR.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		wanted := "status"
		switch v.(type) {
		case *receiptEnvelope:
			wanted = "receipt"
		case *closedEnvelope:
			wanted = "closed"
		}
		for frames := 0; frames < 16; frames++ {
			line, err := reader.ReadBytes('\n')
			var envelope struct {
				Type string `json:"type"`
			}
			if err != nil || json.Unmarshal(line, &envelope) != nil {
				t.Fatal("invalid original control response", err)
			}
			if envelope.Type == "status" && wanted != "status" {
				continue // a heartbeat may precede the requested receipt
			}
			if envelope.Type != wanted || json.Unmarshal(line, v) != nil {
				t.Fatal("unexpected original control response")
			}
			return
		}
		t.Fatal("expected bounded response missing")
	}
	write := func(line string) {
		t.Helper()
		if _, err := io.WriteString(inW, line); err != nil {
			t.Fatal(err)
		}
	}
	var status statusEnvelope
	read(&status)
	first := status.Snapshot.Revision
	write("status\n")
	read(&status)
	if status.Type != "status" || status.Snapshot.Revision <= first || status.Snapshot.Standby != "off" {
		t.Fatal("status did not use original mapper", status)
	}
	// Stale revision, then duplicate ID, are rejected by the same mapper.
	for index, line := range []string{"off 1 0\n", fmt.Sprintf("off 1 %d\n", status.Snapshot.Revision)} {
		write(line)
		if index == 0 {
			previous := status.Snapshot.Revision
			read(&status) // strict order: refusal evidence must precede stale receipt
			tr := status.Snapshot.Transition
			if status.Snapshot.Revision <= previous || tr == nil || tr.RequestID != 1 || tr.Intent != "off" || tr.Outcome != "rejected" {
				t.Fatal("missing pre-owner refusal observation", status)
			}
		}
		var receipt receiptEnvelope
		read(&receipt)
		if receipt.Type != "receipt" || receipt.Receipt.RequestID != 1 || receipt.Receipt.Disposition != "stale" {
			t.Fatal("stale/duplicate input accepted", receipt)
		}
		if index != 0 {
			read(&status)
			if status.Snapshot.Transition != nil {
				t.Fatal("duplicate manufactured evidence", status)
			}
		}
	}
	// The allowance reaches the same owner through the same mapper. Standby is
	// OFF here, so the owner refuses to open it; closing it is always accepted.
	// Neither is a standby transition, so neither may leave one or block ON.
	for index, tc := range []struct{ word, intent, disposition string }{
		{"allow", "allow-run", "rejected"}, {"deny", "deny-run", "accepted"},
	} {
		id := uint64(index + 2)
		write(fmt.Sprintf("%s %d %d\n", tc.word, id, status.Snapshot.Revision))
		var receipt receiptEnvelope
		read(&receipt)
		if receipt.Receipt.RequestID != id || receipt.Receipt.Intent != tc.intent || receipt.Receipt.Disposition != tc.disposition {
			t.Fatal("allowance did not reach the original owner", receipt)
		}
		read(&status)
		if status.Snapshot.Transition != nil || !status.Snapshot.CanRequestOn || status.Snapshot.Standby != "off" {
			t.Fatal("allowance changed standby evidence", status)
		}
	}
	if owner.RunAllowed() {
		t.Fatal("allowance opened while standby is off")
	}
	before := owner.Snapshot()
	if canceled {
		cancel() // the same lifetime consumed by foreground's signal context
	} else {
		// A would-be ON after close in the same OS read is never decoded/applied.
		write(fmt.Sprintf("close\non 2 %d\ninvalid\n", status.Snapshot.Revision))
	}
	select {
	case err := <-done:
		joined = true
		if (canceled && !errors.Is(err, context.Canceled)) || (!canceled && err != nil) {
			t.Fatal("unexpected original loop termination", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit close did not join while input is still open")
	}
	// Later-chunk input has no remaining reader and cannot start work either.
	write(fmt.Sprintf("on 3 %d\n", status.Snapshot.Revision))
	if after := owner.Snapshot(); after.Revision != before.Revision || after.Standby != "off" || after.Owned != 0 {
		t.Fatal("input after close affected original owner", after)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	result := closeOnce(owner, closeCtx)
	if result != nil || writeClosed(p, result) != nil {
		t.Fatal("same idle owner close failed", result)
	}
	var closed closedEnvelope
	read(&closed)
	if closed.Outcome != "confirmed" || owner.Snapshot().CanRequestOn {
		t.Fatal("closed owner can restart", closed)
	}
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatal("output EOF not observed", err)
	}
}

func TestForegroundPipeMalformedAndPartialEOF(t *testing.T) {
	for _, text := range []string{"status", "on 01 1\n", "status\r\n"} {
		t.Run(fmt.Sprintf("input-%x", text), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			p := &ownedPipes{int(r.Fd()), -1}
			if err := unix.SetNonblock(p.in, true); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(w, text); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			var buffer []byte
			got, err := p.readCommands(&buffer, foregroundInput)
			if err == nil {
				got, err = p.readCommands(&buffer, foregroundInput)
			}
			if len(got) != 0 || !errors.Is(err, ErrProtocol) {
				t.Fatal("malformed or partial EOF accepted", got, err)
			}
		})
	}
}
