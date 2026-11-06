package workerowner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRegistrationSourceHostStopsDuringSourceCleanup(t *testing.T) {
	for _, end := range []string{"disconnect", "eof"} {
		for _, outcome := range []string{"confirmed", "unknown"} {
			t.Run(end+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					o := New(time.Second)
					host, stopHost := context.WithCancel(context.Background())
					defer stopHost()
					source, disconnect := context.WithCancel(context.Background())
					defer disconnect()
					requests, done := make(chan *Request, 1), make(chan error, 1)
					entered, release := make(chan struct{}), make(chan struct{})
					var released sync.Once
					releaseClose := func() { released.Do(func() { close(release) }) }
					defer releaseClose()
					var closes atomic.Int32
					started := make(chan struct{})
					go func() { done <- o.WaitSource(host, source, requests) }()
					requests <- sourceRequest(t, started, &closes, func(context.Context) error {
						close(entered)
						<-release
						return nil
					})
					sourceStarted(t, started)
					if end == "disconnect" {
						disconnect()
					} else {
						close(requests)
					}
					<-entered
					// WaitSource has entered its source cleanup wait before host cancellation.
					// All other goroutines are durably blocked; no wall-clock race or sleep.
					synctest.Wait()
					before := time.Now()
					stopHost()
					synctest.Wait()
					if outcome == "confirmed" {
						releaseClose()
					}
					err := <-done
					if outcome == "confirmed" {
						if err != nil {
							t.Fatal(err)
						}
					} else {
						if !errors.Is(err, ErrUnknown) {
							t.Fatal("unknown lost", err)
						}
						if elapsed := time.Since(before); elapsed != time.Second {
							t.Fatalf("original cleanup budget renewed: %v", elapsed)
						}
						if s := o.Snapshot(); !s.Closed || s.Owned != 1 || !s.Unknown {
							t.Fatalf("unknown owner not retained and sealed: %+v", s)
						}
					}
					if !o.Snapshot().Closed || closes.Load() != 1 {
						t.Fatalf("host stop during source cleanup left admission open: snapshot=%+v closes=%d", o.Snapshot(), closes.Load())
					}
					if err := o.Request(context.Background(), freshRequest(nil)); !errors.Is(err, ErrRejected) {
						t.Fatalf("host stop reopened admission: %v", err)
					}
					releaseClose()
					synctest.Wait()
					if s := o.Snapshot(); !s.Closed || s.Owned != 0 || s.Unknown || closes.Load() != 1 {
						t.Fatalf("original cleanup not joined exactly once: %+v closes=%d", s, closes.Load())
					}
				})
			})
		}
	}
}
