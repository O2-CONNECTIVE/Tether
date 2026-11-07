package main

import (
	"reflect"
	"testing"
	"time"
)

func TestStatusSnapshotRejectsConcurrentFeedLockWithoutBlocking(t *testing.T) {
	f := &statusFeed{frame: statusFrame{Version: 1, Phase: "connecting", Cleanup: "pending"}}
	locked, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Force the valid ordering in which another feed operation owns mu. The
	// old aggregate fixture demanded a snapshot even in this unavailable window.
	go func() {
		f.mu.Lock()
		close(locked)
		<-release
		f.mu.Unlock()
		close(unlocked)
	}()
	<-locked
	type result struct {
		frame statusFrame
		ok    bool
	}
	replied := make(chan result, 1)
	go func() {
		v, ok := f.snapshot(1, 1234)
		replied <- result{v, ok}
	}()
	var got result
	blocked := false
	select {
	case got = <-replied:
	case <-time.After(time.Second):
		blocked = true
	}
	// Release and join both owned goroutines even when a blocking-lock
	// regression causes the bounded observation above to fail.
	close(release)
	<-unlocked
	if blocked {
		<-replied
		t.Fatal("snapshot blocked on another feed operation")
	}
	if got.ok || !reflect.DeepEqual(got.frame, statusFrame{}) {
		t.Fatalf("contention exposed a snapshot: %+v, %v", got.frame, got.ok)
	}
	v, ok := f.snapshot(2, 2345)
	if !ok || v.Version != 1 || v.Phase != "connecting" || v.Cleanup != "pending" || v.Seq != 2 || v.SourceContinuousMS != 2345 {
		t.Fatalf("snapshot after release: %+v, %v", v, ok)
	}
}
