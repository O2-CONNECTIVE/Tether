package guestaccount

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalExpiredFlushNeverPermitsAccountCreation(t *testing.T) {
	for _, at := range []struct {
		name   string
		writes int
	}{
		{"reserve", 1},
		{"create-attempt", 2},
	} {
		t.Run(at.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				j, k, owner, f := journalFixture(t)
				ctx := testContext(t)
				start := time.Now()
				k.onFlush = func() {
					if k.writes == at.writes {
						// Complete the native flush exactly at the unchanged deadline.
						time.Sleep(time.Second)
					}
				}
				if err := owner.Prepare(ctx); !errors.Is(err, ErrUnknown) {
					t.Fatal("expired flush was acknowledged", err)
				}
				synctest.Wait()
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(start) != time.Second {
					t.Fatal("original one-second deadline was not exercised", ctx.Err(), time.Since(start))
				}
				if !j.poisoned || hasJournalCreate(f) || k.writes != at.writes {
					t.Fatal("expired commit permitted side effects", j.poisoned, k.writes, f.trace)
				}
				k.onFlush = nil
				if receipt, err := j.reserve(testContext(t), record{j.plan.binding, reserved}); !errors.Is(err, ErrUnknown) || receipt.seal != nil || k.writes != at.writes {
					t.Fatal("poisoned journal issued receipt or retried", err, k.writes)
				}
				if err := owner.Prepare(testContext(t)); err == nil || hasJournalCreate(f) || k.writes != at.writes {
					t.Fatal("fresh context reopened expired preparation", err, f.trace)
				}
			})
		})
	}
}
