//go:build darwin

package residentmac

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"golang.org/x/sys/unix"
)

// elapsedNow reads the clock standby's length is counted on: the one that
// keeps counting while the Mac sleeps (CLOCK_MONOTONIC_RAW, the clock of
// mach_continuous_time). Go's own monotonic reading stops during sleep on
// macOS, so a wall clock set back before a sleep would stretch standby (#399).
// A clock that cannot be read reads as the far future: standby ends rather
// than outlives its length.
func elapsedNow() time.Time {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts) != nil {
		return time.Unix(1<<62, 0)
	}
	return time.Unix(ts.Unix())
}

// ponytail: 待ちの間の 1 秒ごとの確認。遅れや電力が問題になれば IORegisterForSystemPower・NWPathMonitor に上げる。
func startReconnectSignals(ctx context.Context, connector residentConnector, nudge func(uint64)) {
	startReconnectSignalsUsing(ctx, connector, reconnectAddresses, reconnectWakePoll, nudge)
}

func startReconnectSignalsUsing(ctx context.Context, connector residentConnector, addressesFn func() (string, error), poll time.Duration, nudge func(uint64)) {
	changesSource, ok := connector.(interface{ Changes() <-chan struct{} })
	if !ok || nudge == nil {
		return
	}
	changes := changesSource.Changes()
	if changes == nil {
		return
	}
	for {
		snapshot, addresses, err := reconnectWaitObservation(connector, addressesFn)
		if !snapshot.RetryWaiting {
			select {
			case <-ctx.Done():
				return
			case <-changes:
			}
			continue
		}
		var detector reconnectWakeDetector
		detector.observe(elapsedNow(), time.Now(), addresses, err == nil)
		waitID := snapshot.RetryWaitID
		ticker := time.NewTicker(poll)
		waiting := true
		for waiting {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-changes:
				snapshot = connector.Snapshot()
				waiting = snapshot.RetryWaiting
				if waiting && snapshot.RetryWaitID != waitID {
					snapshot, addresses, err = reconnectWaitObservation(connector, addressesFn)
					waiting = snapshot.RetryWaiting
					if !waiting {
						continue
					}
					waitID = snapshot.RetryWaitID
					detector = reconnectWakeDetector{}
					detector.observe(elapsedNow(), time.Now(), addresses, err == nil)
				}
			case <-ticker.C:
				snapshot, addresses, err = reconnectWaitObservation(connector, addressesFn)
				if !snapshot.RetryWaiting {
					waiting = false
					continue
				}
				if snapshot.RetryWaitID != waitID {
					waitID = snapshot.RetryWaitID
					detector = reconnectWakeDetector{}
					detector.observe(elapsedNow(), time.Now(), addresses, err == nil)
					continue
				}
				if detector.observe(elapsedNow(), time.Now(), addresses, err == nil) {
					nudge(waitID)
					waiting = false
				}
			}
		}
		ticker.Stop()
	}
}

func reconnectWaitObservation(connector residentConnector, addressesFn func() (string, error)) (agenttransport.ResidentControlSnapshot, string, error) {
	for {
		snapshot := connector.Snapshot()
		if !snapshot.RetryWaiting {
			return snapshot, "", nil
		}
		addresses, err := addressesFn()
		current := connector.Snapshot()
		if !current.RetryWaiting {
			return current, addresses, err
		}
		if current.RetryWaitID == snapshot.RetryWaitID {
			return snapshot, addresses, err
		}
	}
}

func reconnectAddresses() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	addresses := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		current, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, address := range current {
			addresses = append(addresses, fmt.Sprintf("%d:%s/%s", iface.Index, iface.Name, address.String()))
		}
	}
	return reconnectNetworkFingerprint(addresses), nil
}
