package agenttransport

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"time"
)

func residentRead(ctx context.Context, ws *websocket.Conn) ([]byte, error) {
	typ, msg, e := ws.Read(ctx)
	if e != nil {
		return nil, e
	}
	if typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		return nil, ErrRefused
	}
	return msg, nil
}
func residentWrite(ctx context.Context, ws *websocket.Conn, msg []byte) error {
	if len(msg) > MaxRegistrationMessage {
		return ErrRefused
	}
	return ws.Write(ctx, websocket.MessageText, msg)
}

// The only extra goroutine is retained and joined with the original transport.
// Finite heartbeat failure ends this source; it never renews work authority.
func residentHeartbeat(ctx context.Context, ws *websocket.Conn, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			ping, stop := context.WithTimeout(ctx, 10*time.Second)
			e := ws.Ping(ping)
			stop()
			if e != nil {
				cancel()
				return
			}
		}
	}
}
func residentCleanup(c *closer, ping <-chan struct{}, budget time.Duration) error {
	ctx, stop := context.WithTimeout(context.Background(), budget)
	defer stop()
	var err error
	if ping != nil {
		err = waitFor(ctx, ping)
	}
	return errors.Join(err, c.wait(ctx))
}
