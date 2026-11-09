package residentclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

const MaxInput = 128

// IO is owned by the trusted composition. Both methods must honor ctx, bound
// one complete LF frame and report partial I/O as an error. Serve never retries.
type IO interface {
	ReadLine(context.Context) ([]byte, error)
	WriteLine(context.Context, []byte) error
}

// Session is already authenticated and initialized. The port itself grants
// nothing; implementations must never rebind a failed/old session implicitly.
type Session interface {
	Invoke(context.Context, string, string) (json.RawMessage, error)
	Close(context.Context) error
}

// Serve drives one original session. Close/EOF/protocol failure DELETE that
// session once, not work cancellation, result release or host OFF/cleanup.
func Serve(ctx context.Context, stream IO, session Session) (result error) {
	if ctx == nil || stream == nil || session == nil {
		return ErrUnavailable
	}
	broken := false
	write := func(v any) error {
		b, e := json.Marshal(v)
		if e != nil || len(b) > mcpadapter.MaxLine {
			broken = true
			return ErrProtocol
		}
		budget, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stream.WriteLine(budget, append(b, '\n')) != nil {
			broken = true
			return ErrUnavailable
		}
		return nil
	}
	defer func() {
		closing, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := session.Close(closing)
		cancel()
		outcome := "confirmed"
		if err != nil {
			outcome = "unknown"
			result = ErrUnavailable
		}
		if !broken {
			if e := write(map[string]any{"type": "closed", "session": outcome, "work_cleanup": "not_observed"}); e != nil {
				result = e
			}
		}
	}()
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	if e := write(map[string]any{"type": "ready", "protocolVersion": mcpadapter.ProtocolVersion}); e != nil {
		return e
	}
	for {
		read, cancel := context.WithTimeout(ctx, time.Minute)
		line, e := stream.ReadLine(read)
		cancel()
		if errors.Is(e, io.EOF) && len(line) == 0 {
			return nil
		}
		if e != nil || ctx.Err() != nil {
			return ErrUnavailable
		}
		if len(line) == 0 || len(line) > MaxInput || line[len(line)-1] != '\n' || strings.ContainsAny(string(line[:len(line)-1]), "\x00\r\n\t") {
			return ErrProtocol
		}
		command := strings.Split(string(line[:len(line)-1]), " ")
		if len(command) == 1 && command[0] == "close" {
			return nil
		}
		if command[0] == "list" || command[0] == "get_execution" || command[0] == "read_output" {
			value, e := dictionaryCommand(ctx, session, command)
			if e != nil {
				return e
			}
			frame := map[string]any{"type": "result", "operation": command[0], "value": value}
			if command[0] != "list" {
				frame["request_id"] = command[1]
			}
			if e = write(frame); e != nil {
				return e
			}
			continue
		}
		if len(command) != 2 || operation(command[0]) == "" || !agentwire.ValidCommandID(command[1]) {
			return ErrProtocol
		}
		value, e := session.Invoke(ctx, command[0], command[1])
		if e != nil {
			return ErrUnavailable
		}
		if e = write(map[string]any{"type": "result", "operation": command[0], "request_id": command[1], "value": value}); e != nil {
			return e
		}
	}
}
