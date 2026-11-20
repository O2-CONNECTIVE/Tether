package agentchannel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOpenAllowsRescueDeadlineWithThirtySecondClockSkew(t *testing.T) {
	env := newEnv(t, 24*time.Hour)
	cfg := env.clientConfig(quickBudgets())
	cfg.Clock = func() time.Time { return env.deadline.Add(-MaxClientLifetime - 30*time.Second) }
	cfg.AllowRescueClockSkew = true

	err := openAgainstClosedPeer(t, cfg)
	if err == nil || errors.Is(err, ErrConfig) {
		t.Fatalf("Open with rescue clock skew = %v, want admission followed by handshake failure", err)
	}
}

func TestOpenRescueDeadlineAllowanceBoundary(t *testing.T) {
	now := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		extra   time.Duration
		wantErr bool
	}{
		{name: "at allowance", extra: RescueClockSkewAllowance},
		{name: "above allowance", extra: RescueClockSkewAllowance + time.Nanosecond, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
			cfg.Clock = func() time.Time { return now }
			cfg.Deadline = now.Add(MaxClientLifetime + tc.extra)
			cfg.AllowRescueClockSkew = true
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

func TestOpenRescueDeadlineAtNowIsRejected(t *testing.T) {
	now := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
	cfg.Clock = func() time.Time { return now }
	cfg.Deadline = now
	cfg.AllowRescueClockSkew = true
	if err := openAgainstClosedPeer(t, cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("Open at deadline = %v, want ErrConfig", err)
	}
}

func TestOpenRescueAllowanceDoesNotExtendChannelDeadline(t *testing.T) {
	env := newEnv(t, 24*time.Hour)
	server := env.newServer(t, quickBudgets())
	sConn, cConn := memPair()
	accepted := acceptAsync(context.Background(), server, sConn)
	cfg := env.clientConfig(quickBudgets())
	cfg.Clock = func() time.Time { return env.deadline.Add(-MaxClientLifetime - 30*time.Second) }
	cfg.AllowRescueClockSkew = true
	client, err := Open(context.Background(), cfg, cConn)
	if err != nil {
		t.Fatalf("Open with rescue allowance = %v", err)
	}
	result := await(t, accepted, 5*time.Second)
	if result.err != nil {
		t.Fatalf("Accept = %v", result.err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = result.ch.Close()
	})

	issuedDeadline := server.Dispatch().Deadline
	if !client.deadline.Equal(issuedDeadline) || !result.ch.deadline.Equal(issuedDeadline) {
		t.Fatalf("client deadline %v and server channel deadline %v; want issued deadline %v", client.deadline, result.ch.deadline, issuedDeadline)
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
