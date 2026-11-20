package agentchannel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOpenAllowsRescueDeadlineWithThirtySecondClockSkew(t *testing.T) {
	hostTime := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	gatewayNow := hostTime.Add(-30 * time.Second)
	cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
	cfg.Clock = func() time.Time { return gatewayNow }
	cfg.Deadline = hostTime.Add(24 * time.Hour)
	cfg.MaxLifetimeAllowance = time.Minute

	err := openAgainstClosedPeer(t, cfg)
	if err == nil || errors.Is(err, ErrConfig) {
		t.Fatalf("Open with rescue allowance = %v, want admission followed by handshake failure", err)
	}
}

func TestOpenRescueDeadlineAllowanceBoundary(t *testing.T) {
	now := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		extra   time.Duration
		wantErr bool
	}{
		{name: "at allowance", extra: time.Minute},
		{name: "above allowance", extra: time.Minute + time.Nanosecond, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
			cfg.Clock = func() time.Time { return now }
			cfg.Deadline = now.Add(MaxClientLifetime + tc.extra)
			cfg.MaxLifetimeAllowance = time.Minute
			err := openAgainstClosedPeer(t, cfg)
			if tc.wantErr && !errors.Is(err, ErrConfig) {
				t.Fatalf("Open = %v, want ErrConfig", err)
			}
			if !tc.wantErr && (err == nil || errors.Is(err, ErrConfig)) {
				t.Fatalf("Open = %v, want admission followed by handshake failure", err)
			}
		})
	}
}

func TestOpenWithoutRescueAllowanceKeepsLifetimeStrict(t *testing.T) {
	now := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
	cfg.Clock = func() time.Time { return now }
	cfg.Deadline = now.Add(MaxClientLifetime + 30*time.Second)
	if err := openAgainstClosedPeer(t, cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("strict Open = %v, want ErrConfig", err)
	}
}

func openAgainstClosedPeer(t *testing.T, cfg ClientConfig) error {
	t.Helper()
	client, peer := memPair()
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	ch, err := Open(context.Background(), cfg, client)
	if ch != nil {
		_ = ch.Close()
	}
	return err
}
