package agentlink

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
)

// Connector returns the single-use producer that the MCP entry calls once to
// get its authenticated agent channel. conn is the owner's existing transport
// (for example the opaque relay stream); cfg carries the pinned host key, the
// enrolled signer and the dispatched binding. The returned stream is the
// agentchannel.Channel: closing it tears the channel down and its Close
// reports whether the conn close and workers were confirmed. On an error a
// non-nil stream is an unconfirmed owner: retain it and retry only its Close.
// A second call is
// refused and closes nothing; there is no redial or reconnect.
func Connector(cfg agentchannel.ClientConfig, conn net.Conn) func(context.Context) (io.ReadWriteCloser, error) {
	var once sync.Once
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		var (
			ch  *agentchannel.Channel
			err = ErrConfig
		)
		once.Do(func() {
			if conn == nil {
				return
			}
			ch, err = agentchannel.Open(ctx, cfg, conn)
		})
		if err != nil {
			if ch != nil {
				// Return the same unconfirmed owner even on failure. The caller
				// must retain it and retry Close, not silently discard it.
				return ch, errors.Join(err, agentchannel.ErrCleanupUnknown)
			}
			return nil, err
		}
		return ch, nil
	}
}
