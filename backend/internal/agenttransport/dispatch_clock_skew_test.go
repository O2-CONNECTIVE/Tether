package agenttransport

import (
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
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
		{name: "at allowance", extra: agentchannel.RescueClockSkewAllowance},
		{name: "above allowance", extra: agentchannel.RescueClockSkewAllowance + time.Nanosecond, wantErr: true},
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

func TestCheckRescueDispatchMinimumRemainingBoundaryUsesEncodedAnswer(t *testing.T) {
	hostTime := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	gatewayNow := hostTime.Add(30 * time.Second)
	for _, tc := range []struct {
		name    string
		extra   time.Duration
		wantErr bool
	}{
		{name: "exact minimum", extra: 0, wantErr: true},
		{name: "one nanosecond above minimum", extra: time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixtureDispatch(rescueKey(0x69), "rescue-launch", gatewayNow.Add(time.Second+tc.extra))
			frame := encodeRescueAnswer(agentgateway.RescueAnswer{
				Answer: agentgateway.RescueAllow, Dispatch: d, Signature: []byte{1},
			})
			answer, err := decodeRescueAnswer(frame)
			if err != nil {
				t.Fatalf("decode encoded answer: %v", err)
			}
			if answer.Dispatch.Binding.ConnectionGeneration == 0 {
				t.Fatal("encoded dispatch lost required connection_generation")
			}
			err = checkRescueDispatch(answer.Dispatch, gatewayNow)
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
