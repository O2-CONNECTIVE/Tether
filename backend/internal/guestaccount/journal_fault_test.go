package guestaccount

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func hasJournalCreate(f *fixture) bool {
	for _, op := range f.trace {
		if op == "create" {
			return true
		}
	}
	return false
}

func TestJournalUnknownWriteOrFlushNeverPermitsAccountCreation(t *testing.T) {
	for _, kind := range []string{"short-write", "write-error", "flush-error", "cancel-after-write", "cancel-after-flush", "panic"} {
		t.Run(kind, func(t *testing.T) {
			j, k, l, f := journalFixture(t)
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			switch kind {
			case "short-write":
				k.shortWrite = true
			case "write-error":
				k.writeErr = ErrUnknown
			case "flush-error":
				k.flushErr = ErrUnknown
			case "cancel-after-write":
				k.onWrite = cancel
			case "cancel-after-flush":
				k.onFlush = cancel
			case "panic":
				k.onWrite = func() { panic("unknown native return") }
			}
			if err := l.Prepare(ctx); err == nil || !j.poisoned || hasJournalCreate(f) {
				t.Fatal("unknown commit permitted side effect", err, f.trace)
			}
			writes := k.writes
			if r, e := j.reserve(testContext(t), record{j.plan.binding, reserved}); e == nil || r.seal != nil || k.writes != writes {
				t.Fatal("poisoned journal issued receipt/retried")
			}
		})
	}
}

func TestJournalRecoveryReadsActualUnknownAttemptNotPriorReserved(t *testing.T) {
	j, k, l, f := journalFixture(t)
	k.onFlush = func() {
		if k.writes == 2 {
			k.flushErr = ErrUnknown
		}
	}
	if err := l.Prepare(testContext(t)); err == nil || hasJournalCreate(f) {
		t.Fatal("unknown attempt executed account", err)
	}
	if j.record.stage != reserved {
		t.Fatal("failed commit changed durable in-memory record")
	}
	k.onFlush = nil
	k.flushErr = nil
	if err := j.close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	next, e := reopenJournal(t, j, k, j.plan)
	if e != nil {
		t.Fatal(e)
	}
	f.s.journal = next
	f.s.journalSeal = next.seal
	f.s.previous = f
	f.oldBinding = j.plan.binding
	f.trace = nil
	owner, e := recoverLifecycle(testContext(t), j.plan, f.s)
	if e != nil {
		t.Fatal(e)
	}
	if owner.record.stage != createAttempt || owner.state != Quarantined {
		t.Fatal("recovery silently rolled back full attempt", owner.record)
	}
	if e = owner.Prepare(testContext(t)); e == nil {
		t.Fatal("recovery recreated account")
	}
	if e = owner.Close(testContext(t)); !errors.Is(e, ErrManual) || hasJournalCreate(f) {
		t.Fatal("SID-less attempt escaped quarantine", e, f.trace)
	}
}

func TestJournalEveryTornTailAndCorruptionRejectWithoutTruncate(t *testing.T) {
	j, k, l, _ := journalFixture(t)
	if e := l.Prepare(testContext(t)); e != nil {
		t.Fatal(e)
	}
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	full := append([]byte(nil), k.data...)
	start := len(full) - journalFrameBytes
	for cut := 1; cut < journalFrameBytes; cut++ {
		k.data = append([]byte(nil), full[:start+cut]...)
		before := append([]byte(nil), k.data...)
		partial, e := reopenJournal(t, j, k, j.plan)
		if e == nil {
			t.Fatalf("torn byte %d accepted", cut)
		}
		if !bytes.Equal(before, k.data) {
			t.Fatal("torn tail was truncated/repaired")
		}
		if partial != nil {
			if e = partial.close(testContext(t)); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, kind := range []string{"missing-header", "empty", "header", "checksum", "sequence"} {
		k.data = append([]byte(nil), full...)
		switch kind {
		case "missing-header":
			k.data = k.data[:journalHeaderBytes-1]
		case "empty":
			k.data = nil
		case "header":
			k.data[16] ^= 1
		case "checksum":
			k.data[len(k.data)-1] ^= 1
		case "sequence":
			k.data[start] = 1
			sum := sha256.Sum256(k.data[start : len(k.data)-32])
			copy(k.data[len(k.data)-32:], sum[:])
		}
		partial, e := reopenJournal(t, j, k, j.plan)
		if e == nil {
			t.Fatalf("%s accepted", kind)
		}
		if partial != nil {
			if e = partial.close(testContext(t)); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestJournalConditionalSaveRejectsBindingAndStageMutation(t *testing.T) {
	j, k, l, _ := journalFixture(t)
	if e := l.Prepare(testContext(t)); e != nil {
		t.Fatal(e)
	}
	current := j.record
	writes := k.writes
	for _, field := range []string{"epoch", "intent", "policy", "generation", "machine", "operator", "service", "name", "sid", "regression", "skip"} {
		r := current
		switch field {
		case "epoch":
			r.binding.epoch[0]++
		case "intent":
			r.binding.intent[0]++
		case "policy":
			r.binding.policy[0]++
		case "generation":
			r.binding.generation++
		case "machine":
			r.binding.machine += "-1"
		case "operator":
			r.binding.operator += "1"
		case "service":
			r.binding.service = "S-1-5-19"
		case "name":
			r.binding.name += "a"
		case "sid":
			r.binding.sid += "1"
		case "regression":
			r.stage = ownedSID
		case "skip":
			r.stage = deleteComplete
		}
		if receipt, e := j.save(testContext(t), r); e == nil || receipt.seal != nil {
			t.Fatalf("%s mutation issued receipt", field)
		}
	}
	if k.writes != writes {
		t.Fatal("rejected mutations appended data")
	}
}

func journalTerminalHistory(t *testing.T, j *journalBackend, count int) []byte {
	t.Helper()
	data := journalHeader(j.root.installation)
	previous := sha256.Sum256(data)
	seq := uint64(0)
	for i := 1; i <= count; i++ {
		p := journalPlan(t, uint64(i+10), uint64(i))
		for _, s := range []stage{reserved, abortComplete} {
			seq++
			b, e := journalFrame(seq, previous, record{p.binding, s})
			if e != nil {
				t.Fatal(e)
			}
			data = append(data, b...)
			previous = sha256.Sum256(b)
		}
	}
	return data
}

func TestJournalReserveKeepsAllSixCleanupFramesAtCapacityBoundary(t *testing.T) {
	for _, history := range []int{509, 510} {
		t.Run(string(rune('A'+history-509)), func(t *testing.T) {
			// Capacity is a ledger invariant, not a wall-clock replay benchmark.
			synctest.Test(t, func(t *testing.T) {
				j, k, _, f := journalFixture(t)
				if e := j.close(testContext(t)); e != nil {
					t.Fatal(e)
				}
				k.data = journalTerminalHistory(t, j, history)
				p := journalPlan(t, 10000, uint64(history+1))
				next, e := reopenJournal(t, j, k, p)
				if e != nil {
					t.Fatal(e)
				}
				f.s.journal = next
				f.s.journalSeal = next.seal
				owner, e := newLifecycle(p, f.s)
				if e != nil {
					t.Fatal(e)
				}
				e = owner.Prepare(testContext(t))
				if history == 510 {
					if e == nil || hasJournalCreate(f) {
						t.Fatal("insufficient cleanup capacity admitted create")
					}
					return
				}
				if e != nil {
					t.Fatal(e)
				}
				if e = owner.Close(testContext(t)); e != nil {
					t.Fatal("reserved cleanup budget exhausted", e)
				}
				if next.count != journalMaxFrames || len(k.data) != journalHeaderBytes+journalMaxFrames*journalFrameBytes {
					t.Fatal("cleanup did not fit exact bound", next.count, len(k.data))
				}
				writes := k.writes
				if _, e = next.save(testContext(t), next.record); e != nil || k.writes != writes {
					t.Fatal("full ledger terminal idempotence appended")
				}
			})
		})
	}
}

func TestJournalRetainsRealPartialOpenAndBorrowedRootAlias(t *testing.T) {
	for _, kind := range []string{"partial", "root-alias", "pseudo", "missing", "short-read", "file-id", "links", "reparse", "security", "inherit"} {
		j, k, _, _ := journalFixture(t)
		if e := j.close(testContext(t)); e != nil {
			t.Fatal(e)
		}
		switch kind {
		case "partial":
			k.openErr = ErrUnknown
		case "root-alias":
			k.openHandle = k.root.root
		case "pseudo":
			k.openHandle = ^uintptr(0)
		case "missing":
			k.openHandle = 0
			k.openErr = ErrUnknown
		case "short-read":
			k.shortRead = true
		default:
			k.onInspect = func(i *journalFileInfo) {
				switch kind {
				case "file-id":
					i.identity.fileID[0]++
				case "links":
					i.links = 2
				case "reparse":
					i.reparse = true
				case "security":
					i.identity.security = "different"
				case "inherit":
					i.inherit = true
				}
			}
		}
		next, e := reopenJournal(t, j, k, j.plan)
		if e == nil || next == nil {
			t.Fatalf("%s did not retain partial owner", kind)
		}
		if next.file != k.openHandle {
			t.Fatal("real returned handle lost")
		}
		closes := k.closes
		e = next.close(testContext(t))
		if kind == "root-alias" || kind == "pseudo" {
			if e == nil || k.closes != closes || next.file == 0 {
				t.Fatal("borrowed/pseudo return was closed or dropped")
			}
		}
	}
}

func TestJournalCloseUnknownAndInFlightFlushRetainExclusiveOwner(t *testing.T) {
	j, k, _, _ := journalFixture(t)
	k.closeErr = ErrUnknown
	if e := j.close(testContext(t)); e == nil || j.file == 0 {
		t.Fatal("failed Close dropped owned handle")
	}
	if other, e := reopenJournal(t, j, k, j.plan); e == nil {
		t.Fatal("unknown close allowed duplicate writer", other)
	}
	if r, e := j.reserve(testContext(t), record{j.plan.binding, reserved}); e == nil || r.seal != nil {
		t.Fatal("unknown close reopened local journal admission")
	}
	k.closeErr = nil
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	j, k, _, _ = journalFixture(t)
	k.closeErr = nil
	entered, release := make(chan struct{}), make(chan struct{})
	k.onFlush = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, e := j.reserve(testContext(t), record{j.plan.binding, reserved}); done <- e }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if e := j.close(ctx); e == nil || j.file == 0 {
		close(release)
		<-done
		t.Fatal("in-flight flush acknowledged close")
	}
	if other, e := reopenJournal(t, j, k, j.plan); e == nil {
		close(release)
		<-done
		t.Fatal("in-flight flush allowed duplicate writer", other)
	}
	close(release)
	if e := <-done; !errors.Is(e, ErrUnknown) {
		t.Fatal("closing in-flight commit returned receipt", e)
	}
	k.onFlush = nil
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestJournalSecurityRejectsUnsafeExpectedAllowPolicy(t *testing.T) {
	j, _, _, _ := journalFixture(t)
	root := j.root
	for _, a := range []journalACE{{0, 0, 2, "S-1-1-0"}, {0, 0, 0x40000000, "S-1-1-0"}, {0, 0, 0x10000000, "S-1-1-0"}, {2, 0, 1, root.service}, {0, 0x40, 1, root.service}, {0, 0, 0x2000000, root.service}} {
		if journalSecurityAllowed(root, root.service, []journalACE{a}) {
			t.Fatal("unsafe ACE accepted", a)
		}
	}
	if journalSecurityAllowed(root, "S-1-1-0", []journalACE{}) || journalSecurityAllowed(root, root.service, nil) {
		t.Fatal("untrusted owner/null DACL accepted")
	}
	if !journalSecurityAllowed(root, root.service, []journalACE{{0, 0, 0x1f01ff, root.serviceSID}, {1, 0, 2, "S-1-1-0"}, {0, 0, 0x120089, "S-1-1-0"}}) {
		t.Fatal("safe bounded policy denied")
	}
}

func TestJournalPendingOpenWithoutReturnedHandleCannotAcknowledgeClose(t *testing.T) {
	j, k, _, _ := journalFixture(t)
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	k.openHandle = 0
	k.openErr = journalPending{}
	next, e := reopenJournal(t, j, k, j.plan)
	if e == nil || next == nil {
		t.Fatal("pending open did not retain owner")
	}
	closes := k.closes
	if e = next.close(testContext(t)); !errors.Is(e, ErrUnknown) || k.closes != closes {
		t.Fatal("pending handle acquisition was acknowledged", e)
	}
}

func TestJournalClosePanicNeverReissuesUnknownNativeClose(t *testing.T) {
	j, k, _, _ := journalFixture(t)
	k.onClose = func() { panic("close native outcome unknown") }
	if e := j.close(testContext(t)); !errors.Is(e, ErrUnknown) {
		t.Fatal(e)
	}
	closes := k.closes
	k.onClose = nil
	if e := j.close(testContext(t)); !errors.Is(e, ErrUnknown) || k.closes != closes || j.file == 0 {
		t.Fatal("unknown close was reissued or ownership dropped", e, k.closes, j.file)
	}
}

func TestJournalReplayRejectsReusedIdentityAndIllegalTerminalEdges(t *testing.T) {
	for _, kind := range []string{"generation", "intent", "terminal-regression"} {
		j, k, _, _ := journalFixture(t)
		if e := j.close(testContext(t)); e != nil {
			t.Fatal(e)
		}
		old := j.plan
		records := []record{{old.binding, reserved}, {old.binding, abortComplete}}
		p := journalPlan(t, 3, 1)
		if kind == "intent" {
			p = old
			p.binding.epoch[0]++
			p.binding.generation = 2
		}
		if kind == "terminal-regression" {
			records = append(records, record{old.binding, createAttempt})
		} else {
			records = append(records, record{p.binding, reserved})
		}
		data := journalHeader(j.root.installation)
		previous := sha256.Sum256(data)
		for i, r := range records {
			b, e := journalFrame(uint64(i+1), previous, r)
			if e != nil {
				t.Fatal(e)
			}
			data = append(data, b...)
			previous = sha256.Sum256(b)
		}
		k.data = data
		partial, e := reopenJournal(t, j, k, p)
		if e == nil || partial == nil {
			t.Fatalf("%s valid-checksum history was accepted", kind)
		}
		if e = partial.close(testContext(t)); e != nil {
			t.Fatal(e)
		}
	}
}

func TestJournalSubjectPartialTokenWithoutFileCannotAcknowledgeClose(t *testing.T) {
	j, k, _, _ := journalFixture(t)
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	k.subjectErr = ErrUnknown
	k.releaseErr = ErrUnknown
	partial, e := reopenJournal(t, j, k, j.plan)
	if e == nil || partial == nil || partial.file != 0 {
		t.Fatal("partial subject owner not retained")
	}
	if e = partial.close(testContext(t)); !errors.Is(e, ErrUnknown) {
		t.Fatal("kernel-held token without journal file was acknowledged", e)
	}
}

func TestJournalNewSessionRetainsHistoryAndRejectsReusedOwnedSID(t *testing.T) {
	j, k, l, _ := journalFixture(t)
	if e := l.Prepare(testContext(t)); e != nil {
		t.Fatal(e)
	}
	if e := l.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	oldSID := j.record.binding.sid
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	p := journalPlan(t, 3, 2)
	next, e := reopenJournal(t, j, k, p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = next.reserve(testContext(t), record{p.binding, reserved}); e != nil {
		t.Fatal(e)
	}
	if _, e = next.save(testContext(t), record{p.binding, createAttempt}); e != nil {
		t.Fatal(e)
	}
	b := p.binding
	b.sid = oldSID
	writes := k.writes
	if _, e = next.save(testContext(t), record{b, ownedSID}); e == nil || k.writes != writes {
		t.Fatal("previous session SID was adopted")
	}
	if !next.history[j.plan.binding.intent] || !next.sids[oldSID] || next.count != 8 {
		t.Fatal("renewal discarded history")
	}
}

func TestJournalPreparedRecoveryCleansExactOldSIDWithoutRecreate(t *testing.T) {
	j, k, l, f := journalFixture(t)
	if e := l.Prepare(testContext(t)); e != nil {
		t.Fatal(e)
	}
	old := j.record.binding
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	next, e := reopenJournal(t, j, k, j.plan)
	if e != nil {
		t.Fatal(e)
	}
	f.s.journal = next
	f.s.journalSeal = next.seal
	f.s.previous = f
	f.oldBinding = old
	f.trace = nil
	recovered, e := recoverLifecycle(testContext(t), j.plan, f.s)
	if e != nil {
		t.Fatal(e)
	}
	if e = recovered.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	if next.record.binding != old || next.record.stage != deleteComplete || hasJournalCreate(f) {
		t.Fatal("recovery lost exact binding or recreated account", next.record, f.trace)
	}
}

func TestJournalCloseCancellationAfterActualReturnNeedsFreshAck(t *testing.T) {
	j, k, _, _ := journalFixture(t)
	ctx, cancel := context.WithCancel(testContext(t))
	k.onClose = cancel
	if e := j.close(ctx); !errors.Is(e, ErrUnknown) || j.file != 0 {
		t.Fatal("late Close success acknowledged expired context or lost real close", e, j.file)
	}
	k.onClose = nil
	if e := j.close(testContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestJournalLostSubjectNeverRestartsNativeCleanup(t *testing.T) {
	j, k, l, _ := journalFixture(t)
	k.subjectErr = ErrUnknown
	if e := l.Prepare(testContext(t)); e == nil {
		t.Fatal("lost subject prepared")
	}
	k.subjectErr = nil
	closes := k.closes
	if e := j.close(testContext(t)); !errors.Is(e, ErrUnknown) || k.closes != closes || j.file == 0 {
		t.Fatal("lost subject resumed native cleanup", e, k.closes, j.file)
	}
}
