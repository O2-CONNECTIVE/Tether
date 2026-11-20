package residentmac

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

type fakeSleepAssertion struct {
	mu       sync.Mutex
	takes    int
	releases int
	held     int
	peak     int
}

func (f *fakeSleepAssertion) Take() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes++
	f.held++
	if f.held > f.peak {
		f.peak = f.held
	}
	return nil
}
func (f *fakeSleepAssertion) Release() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	if f.held > 0 {
		f.held--
	}
	return nil
}
func (f *fakeSleepAssertion) counts() (takes, releases, held, peak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.takes, f.releases, f.held, f.peak
}

type sleepTestSession struct{}

func (sleepTestSession) Run(context.Context) error   { return nil }
func (sleepTestSession) Close(context.Context) error { return nil }

func TestStandbySleepPreventionFollowsOwnedWork(t *testing.T) {
	s := newStandbyWithConfig(StandbyConfig{})
	fake := &fakeSleepAssertion{}
	s.sleep = fake
	request := &workerowner.Request{Approve: func(context.Context) (*workerowner.Worker, error) {
		return &workerowner.Worker{Session: sleepTestSession{}}, nil
	}}
	s.prepareOwnedRequest(request)
	s.prepareOwnedRequest(request)
	if _, err := request.Approve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 0 || held != 1 || peak != 1 {
		t.Fatalf("owned work did not take one assertion: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}

	s.collect(nil, agentgateway.ResidentWorkBinding{}, nil)
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 1 || held != 0 || peak != 1 {
		t.Fatalf("collection did not release one assertion: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
	s.collect(nil, agentgateway.ResidentWorkBinding{}, nil)
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 1 || held != 0 || peak != 1 {
		t.Fatalf("duplicate collection changed assertion counts: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
}

func TestStandbySleepPreventionSkipsIdleAndAllowedWork(t *testing.T) {
	s, _, _ := idleStandby(t)
	fake := &fakeSleepAssertion{}
	s.sleep = fake
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	eventually(t, func() bool { return s.Snapshot().Standby == "on" })
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("idle standby took an assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}

	off := newStandbyWithConfig(StandbyConfig{AllowSleepDuringWork: true})
	off.sleep = fake
	request := &workerowner.Request{Approve: func(context.Context) (*workerowner.Worker, error) {
		return &workerowner.Worker{Session: sleepTestSession{}}, nil
	}}
	off.prepareOwnedRequest(request)
	if _, err := request.Approve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("allowed-sleep work took an assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
}

// IOPM releases process-owned assertions at process teardown. This test checks
// explicit standby stops; abrupt process death is covered by the OS contract.
func TestStandbySleepPreventionReleasesOnOffAndClose(t *testing.T) {
	for _, stop := range []string{"off", "close"} {
		t.Run(stop, func(t *testing.T) {
			s, c, _ := idleStandby(t)
			fake := &fakeSleepAssertion{}
			s.sleep = fake
			if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
				t.Fatal(r)
			}
			if err := s.acquireOwnedWork(); err != nil {
				t.Fatal(err)
			}
			if stop == "off" {
				if r := s.RequestOff(2, s.Snapshot().Revision); r.Disposition != "accepted" {
					t.Fatal(r)
				}
			} else {
				close(c.gate)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 1 || held != 0 || peak != 1 {
				t.Fatalf("%s left an assertion held: takes=%d releases=%d held=%d peak=%d", stop, takes, releases, held, peak)
			}
		})
	}
}

func TestStandbySleepPreventionReleasesWhenCycleFails(t *testing.T) {
	s := newStandby()
	fake := &fakeSleepAssertion{}
	s.sleep = fake
	s.makeConnector = func(context.Context) (residentConnector, error) {
		return nil, errors.New("synthetic connector failure")
	}
	if err := s.acquireOwnedWork(); err != nil {
		t.Fatal(err)
	}
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	eventually(t, func() bool {
		o := s.Snapshot()
		return o.Standby == "off" && o.Cleanup == "confirmed"
	})
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 1 || held != 0 || peak != 1 {
		t.Fatalf("failed cycle left an assertion held: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
}

func TestStandbySleepPreventionReleasesAfterFailedOrUnknownCollection(t *testing.T) {
	for _, result := range []struct {
		name string
		err  error
	}{{name: "work failure", err: context.Canceled}, {name: "cleanup budget", err: workerowner.ErrUnknown}} {
		t.Run(result.name, func(t *testing.T) {
			s := newStandbyWithConfig(StandbyConfig{})
			fake := &fakeSleepAssertion{}
			s.sleep = fake
			if err := s.acquireOwnedWork(); err != nil {
				t.Fatal(err)
			}
			_ = s.collect(nil, agentgateway.ResidentWorkBinding{}, result.err)
			if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 1 || held != 0 || peak != 1 {
				t.Fatalf("%s collection left an assertion held: takes=%d releases=%d held=%d peak=%d", result.name, takes, releases, held, peak)
			}
		})
	}
}
