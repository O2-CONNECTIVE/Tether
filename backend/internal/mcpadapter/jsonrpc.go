package mcpadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"
)

// JSON-RPC error codes. Tool execution failures are tool results with
// isError, never these protocol errors.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeServerBusy     = -32000
)

// requestID keeps the received bytes for an exact echo and a normalized key
// for matching notifications/cancelled.
type requestID struct {
	raw json.RawMessage
	key string
}

// parseID accepts a string or an integer; MCP forbids null IDs.
// encoding/json decodes null into a string without error, so the JSON type is
// checked first.
func parseID(raw json.RawMessage) (*requestID, bool) {
	var s string
	if isString(raw) && json.Unmarshal(raw, &s) == nil {
		return &requestID{raw: raw, key: "s" + s}, true
	}
	var n int64
	if !isString(raw) && json.Unmarshal(raw, &n) == nil && firstByte(raw) != 'n' {
		return &requestID{raw: raw, key: "n" + strconv.FormatInt(n, 10)}, true
	}
	return nil, false
}

func isString(raw json.RawMessage) bool { return firstByte(raw) == '"' }

// readLine returns one newline-terminated message without its newline. An
// over-long line is drained to its newline with bounded memory and reported.
// A trailing fragment without a newline is not a message.
func readLine(r *bufio.Reader, max int) ([]byte, bool, error) {
	var line []byte
	tooLong := false
	for {
		frag, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(frag) > max+1 {
				tooLong, line = true, nil
			} else {
				line = append(line, frag...)
			}
		}
		switch {
		case err == nil:
			if tooLong {
				return nil, true, nil
			}
			return line[:len(line)-1], false, nil
		case errors.Is(err, bufio.ErrBufferFull):
		default:
			return nil, false, err
		}
	}
}

// writer serializes whole messages. json.Marshal escapes control characters,
// so a message never contains a raw newline. Nothing else is written to out.
// ErrOutputUnconfirmed means one caller-owned stdout write may still be
// blocked. No further writes start. The caller must not reuse that stream and
// must close the agent channel; the command-line entry exits the process.
var ErrOutputUnconfirmed = errors.New("MCP output write completion is unconfirmed")

const outputWriteTimeout = 5 * time.Second

type writer struct {
	failedCh chan struct{}
	failOnce sync.Once
	ctx      context.Context
	done     <-chan struct{}
	timeout  time.Duration
	mu       sync.Mutex
	out      io.Writer
	err      error
}

func (w *writer) send(msg map[string]any) {
	b, err := json.Marshal(msg)
	w.mu.Lock()
	defer func() {
		if w.err != nil && w.failedCh != nil {
			w.failOnce.Do(func() { close(w.failedCh) })
		}
		w.mu.Unlock()
	}()
	if w.err != nil {
		return
	}
	if err != nil {
		w.err = err
		return
	}
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		w.err = ctx.Err()
		return
	default:
	}

	budget := w.timeout
	if budget <= 0 {
		budget = outputWriteTimeout
	}
	result := make(chan error, 1)
	payload := append(b, '\n')
	// At most one abandoned Write exists: send holds mu, sets err on every
	// interrupted wait, and every later send refuses before starting a worker.
	go func() {
		n, err := w.out.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		result <- err
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	interrupted := func(reason error) {
		// A consumed response can race EOF/cancel before its writer publishes
		// the result. Give that same write a bounded chance to report completion
		// rather than marking a known-delivered reply unknown by scheduling.
		grace := time.NewTimer(100 * time.Millisecond)
		defer grace.Stop()
		select {
		case err := <-result:
			w.err = err
		case <-timer.C:
			w.err = errors.Join(ErrOutputUnconfirmed, reason)
		case <-grace.C:
			w.err = errors.Join(ErrOutputUnconfirmed, reason)
		}
	}
	select {
	case err := <-result:
		w.err = err
	case <-ctx.Done():
		interrupted(ctx.Err())
	case <-w.done:
		interrupted(ErrChannelClosed)
	case <-timer.C:
		w.err = ErrOutputUnconfirmed
	}
}

func (w *writer) failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *writer) result(id *requestID, result any) {
	w.send(map[string]any{"jsonrpc": "2.0", "id": id.raw, "result": result})
}

// fail answers with id null when the request ID could not be determined.
func (w *writer) fail(id *requestID, code int, message string) {
	raw := json.RawMessage("null")
	if id != nil {
		raw = id.raw
	}
	w.send(map[string]any{"jsonrpc": "2.0", "id": raw, "error": map[string]any{"code": code, "message": message}})
}
