package main

import (
	"encoding/json"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"sync"
	"time"
)

// statusSink operations must be bounded, nonblocking, and own only their pipe.
type statusSink interface {
	writeFrame([]byte) bool
	close()
}
type localSummaryOwner interface {
	LocalSummary() (agentexec.LocalSummary, bool, bool)
}
type statusFrame struct {
	SourceContinuousMS uint64 `json:"source_continuous_ms"`
	Version            int    `json:"version"`
	Seq                uint64 `json:"seq"`
	Phase              string `json:"phase"`
	Reason             string `json:"reason"`
	Remaining          *int64 `json:"grant_remaining_ms"`
	Starting           int    `json:"starting_workers"`
	Running            int    `json:"running_workers"`
	Stopping           int    `json:"stopping_workers"`
	Cleanup            string `json:"cleanup"`
	Terminal           bool   `json:"terminal"`
	Unknown            bool   `json:"terminal_cleanup_unknown"`
}

type statusFeed struct {
	clock func() (uint64, bool)
	mu    sync.Mutex
	frame statusFrame
	owner localSummaryOwner
	sink  statusSink
	wake  chan struct{}
	stop  chan struct{}
	done  chan struct{}
}

func newStatusFeed(sink statusSink) *statusFeed {
	return newStatusFeedClock(sink, statusContinuousMillis)
}
func newStatusFeedClock(sink statusSink, clock func() (uint64, bool)) *statusFeed {
	if sink == nil || clock == nil {
		return nil
	}
	f := &statusFeed{clock: clock, sink: sink, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), frame: statusFrame{Version: 1, Phase: "preparing", Reason: "none", Cleanup: "not_started"}}
	go f.run()
	return f
}
func (f *statusFeed) update(phase, cleanup string, terminal bool) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if !f.frame.Unknown {
		f.frame.Phase = phase
		f.frame.Cleanup = cleanup
		f.frame.Terminal = terminal
	}
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}
func (f *statusFeed) attach(owner any) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.owner, _ = owner.(localSummaryOwner)
	f.frame.Phase = "connecting"
	f.frame.Cleanup = "pending"
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}
func (f *statusFeed) unknown() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.frame.Unknown = true
	f.frame.Terminal = true
	f.frame.Phase = "failed"
	f.frame.Reason = "terminal_cleanup_unknown"
	f.frame.Cleanup = "unknown"
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}
func (f *statusFeed) end() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if !f.frame.Terminal {
		f.frame.Terminal = true
		f.frame.Phase = "failed"
	}
	f.mu.Unlock()
	close(f.stop)
	<-f.done
}
func (f *statusFeed) snapshot(seq, stamp uint64) (statusFrame, bool) {
	if !f.mu.TryLock() {
		return statusFrame{}, false
	}
	v, owner := f.frame, f.owner
	f.mu.Unlock()
	v.Seq = seq
	v.SourceContinuousMS = stamp
	if owner != nil {
		summary, connected, ok := owner.LocalSummary()
		if !ok {
			return statusFrame{}, false
		}
		ms := displayRemaining(summary.Deadline, time.Now())
		if summary.Revoked || v.Terminal {
			ms = 0
		}
		if ms < 0 {
			ms = 0
		}
		if ms > 86400000 {
			ms = 86400000
		}
		v.Remaining = &ms
		v.Starting, v.Running, v.Stopping = summary.Starting, summary.Running, summary.Stopping
		if !v.Unknown {
			v.Reason = localStopReason(summary.Reason)
		}
		if !v.Terminal {
			if summary.Revoked {
				v.Phase = "stopping"
			} else if v.Phase != "stopping" && connected {
				v.Phase = "connected"
			}
			switch summary.Cleanup {
			case agentexec.CleanupUnknown:
				v.Cleanup = "unknown"
			case agentexec.CleanupPending:
				v.Cleanup = "pending"
			default:
				v.Cleanup = "pending"
			}
			v.Reason = localStopReason(summary.Reason)
		}
	}
	return v, true
}
func localStopReason(r agentexec.StopReason) string {
	switch r {
	case agentexec.StopCanceled:
		return "canceled"
	case agentexec.StopCommandTimeout:
		return "command_timeout"
	case agentexec.StopRevoked:
		return "revoked"
	case agentexec.StopHardDeadline:
		return "deadline"
	case agentexec.StopTransportLost:
		return "transport_lost"
	case agentexec.StopRuntimeFault:
		return "runtime_fault"
	default:
		return "none"
	}
}
func (f *statusFeed) run() {
	defer close(f.done)
	defer f.sink.close()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var seq uint64
	emit := func() bool {
		if seq >= 9007199254740991 {
			return false
		}
		stamp, valid := f.clock()
		if !valid || stamp > 9007199254740991 {
			return false
		}
		v, ok := f.snapshot(seq+1, stamp)
		if !ok {
			return true
		}
		b, err := json.Marshal(v)
		if err != nil || len(b)+1 > 2048 {
			return false
		}
		seq++
		if !f.sink.writeFrame(append(b, '\n')) {
			return false
		}
		// A terminal frame ends this stream exactly once; end() only joins.
		return !v.Terminal
	}
	if !emit() {
		return
	}
	for {
		select {
		case <-f.stop:
			emit()
			return
		case <-f.wake:
			if !emit() {
				return
			}
		case <-tick.C:
			if !emit() {
				return
			}
		}
	}
}

// Display only: wall jumps/sleep may shorten the monotonic estimate, never extend it.
func displayRemaining(deadline, now time.Time) int64 {
	mono := deadline.Sub(now)
	wall := deadline.Round(0).Sub(now.Round(0))
	if wall < mono {
		mono = wall
	}
	if mono < 0 {
		return 0
	}
	return mono.Milliseconds()
}
