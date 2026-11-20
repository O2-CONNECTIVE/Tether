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
	now := env.deadline.Add(-MaxClientLifetime - 30*time.Second)

	err := openRescueAgainstClosedPeer(t, cfg, func() time.Time { return now })
	if err == nil || errors.Is(err, ErrConfig) {
		t.Fatalf("OpenRescue with 30s clock skew = %v, want admission followed by handshake failure", err)
	}
}

func TestOpenRescueDeadlineAllowanceBoundary(t *testing.T) {
	now := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	const rescueDeadlineLimit = 24*time.Hour + time.Minute
	for _, tc := range []struct {
		name     string
		lifetime time.Duration
		wantErr  bool
	}{
		{name: "at allowance", lifetime: rescueDeadlineLimit},
		{name: "above allowance", lifetime: rescueDeadlineLimit + time.Nanosecond, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
			cfg.Deadline = now.Add(tc.lifetime)
			err := openRescueAgainstClosedPeer(t, cfg, func() time.Time { return now })
			if tc.wantErr && !errors.Is(err, ErrConfig) {
				t.Fatalf("OpenRescue = %v, want ErrConfig", err)
			}
			if !tc.wantErr && (err == nil || errors.Is(err, ErrConfig)) {
				t.Fatalf("OpenRescue = %v, want admission followed by handshake failure", err)
			}
		})
	}
}

func TestOpenRescueDeadlineAtNowIsRejected(t *testing.T) {
	now := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
	cfg.Deadline = now
	if err := openRescueAgainstClosedPeer(t, cfg, func() time.Time { return now }); !errors.Is(err, ErrConfig) {
		t.Fatalf("OpenRescue at deadline = %v, want ErrConfig", err)
	}
}

func TestOpenRescueAllowanceDoesNotExtendChannelDeadline(t *testing.T) {
	env := newEnv(t, 24*time.Hour)
	server := env.newServer(t, quickBudgets())
	sConn, cConn := memPair()
	accepted := acceptAsync(context.Background(), server, sConn)
	cfg := env.clientConfig(quickBudgets())
	now := env.deadline.Add(-MaxClientLifetime - 30*time.Second)
	client, err := OpenRescue(context.Background(), cfg, cConn, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenRescue with allowance = %v", err)
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
	cfg := newEnv(t, 24*time.Hour).clientConfig(quickBudgets())
	cfg.Deadline = time.Now().Add(MaxClientLifetime + 30*time.Second)
	if err := openAgainstClosedPeer(t, cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("strict Open = %v, want ErrConfig", err)
	}
}

func openRescueAgainstClosedPeer(t *testing.T, cfg ClientConfig, now func() time.Time) error {
	t.Helper()
	client, peer := memPair()
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	ch, err := OpenRescue(context.Background(), cfg, client, now)
	if ch != nil {
		_ = ch.Close()
	}
	return err
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
