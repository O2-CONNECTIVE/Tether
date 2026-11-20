package agenttransport

import (
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
)

func TestCheckRescueDispatchAllowsThirtySecondSkewAndOneMinuteBoundary(t *testing.T) {
	hostTime := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	gatewayNow := hostTime.Add(-30 * time.Second)

	d := fixtureDispatch(rescueKey(0x67), "rescue-launch", hostTime.Add(24*time.Hour))
	if err := checkRescueDispatch(d, gatewayNow); err != nil {
		t.Fatalf("rescue dispatch with 30s clock skew = %v, want accepted", err)
	}
	if !d.Deadline.Equal(hostTime.Add(24 * time.Hour)) {
		t.Fatalf("rescue check changed issued deadline to %v", d.Deadline)
	}

	for _, tc := range []struct {
		name    string
		extra   time.Duration
		wantErr bool
	}{
		{name: "at allowance", extra: time.Minute},
		{name: "above allowance", extra: time.Minute + time.Nanosecond, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixtureDispatch(rescueKey(0x67), "rescue-launch", gatewayNow.Add(agentchannel.MaxClientLifetime+tc.extra))
			err := checkRescueDispatch(d, gatewayNow)
			if tc.wantErr && !errors.Is(err, ErrRefused) {
				t.Fatalf("checkRescueDispatch = %v, want ErrRefused", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkRescueDispatch = %v, want accepted", err)
			}
		})
	}
}

func TestCheckDispatchKeepsRegistrationLifetimeStrict(t *testing.T) {
	now := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	d := fixtureDispatch(rescueKey(0x68), "registration-launch", now.Add(agentchannel.MaxClientLifetime+30*time.Second))
	if err := checkDispatch(d, now); !errors.Is(err, ErrRefused) {
		t.Fatalf("checkDispatch = %v, want strict ErrRefused", err)
	}
}
