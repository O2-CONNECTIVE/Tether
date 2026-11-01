// rescue-mcp is the MCP stdio entry for the agent command tools. No trusted
// producer of an authenticated agent channel exists yet, so the entry refuses
// before reading stdin or writing stdout. It reads no flags, environment
// credentials or endpoint settings.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

// connector returns the authenticated agent-only channel for one approved
// grant. Only trusted composition may provide it; the channel's peer identity
// and grant never come from MCP requests or relay message IDs.
type connector func(context.Context) (io.ReadWriteCloser, error)

func main() { os.Exit(execute(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, nil)) }

// execute serves MCP over in/out once connect produced the channel. Nothing is
// read or written before connect succeeds; out and diagnostic stay open, and
// in is never closed here because the process, not this entry, owns stdin.
func execute(ctx context.Context, args []string, in io.Reader, out, diagnostic io.Writer, connect connector) int {
	if len(args) != 0 || connect == nil || ctx == nil || ctx.Err() != nil || in == nil || out == nil {
		report(diagnostic, "agent channelの信頼済みproducerが未構成です")
		return 2
	}
	conn, err := connect(ctx)
	if err != nil || conn == nil {
		if conn != nil {
			if closeErr := conn.Close(); closeErr != nil {
				report(diagnostic, "agent channel cleanup remains unconfirmed")
			}
		}
		report(diagnostic, "agent channelを取得できませんでした")
		return 1
	}
	client, err := agentwire.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		report(diagnostic, "agent channelを取得できませんでした")
		return 1
	}
	code := 0
	switch err := mcpadapter.Serve(ctx, mcpadapter.Config{Backend: client}, inputSource(in), out); {
	case err == nil:
	case errors.Is(err, mcpadapter.ErrChannelClosed):
		report(diagnostic, "agent channelが終了しました。承認は失効し、再開しません")
		code = 1
	default:
		report(diagnostic, "MCP stdioを終了しました（異常）")
		code = 1
	}
	// Closing the client ends the channel; the remote owner revokes the grant
	// on that transport loss. Nothing is resumed by a later start. The second
	// Close of the same stream reports its owner's join (for an agentchannel,
	// whether the conn close and workers were confirmed).
	_ = client.Close()
	if err := conn.Close(); err != nil {
		report(diagnostic, "agent channelの回収を確認できませんでした")
		code = 1
	}
	return code
}

// inputSource gives mcpadapter.Serve a stream it can close and join promptly.
// A blocking stdin file may not return from Read when closed, so stdin is
// copied by one pump goroutine into a pipe that Serve owns. execute must run
// once, not reuse in, and be followed promptly by process exit. The pump never
// changes the descriptor mode and is not joined: it ends at its next read
// result after Serve closed the pipe, or with the process. It starts only after
// the channel exists, so a refused entry reads nothing.
func inputSource(in io.Reader) io.ReadCloser {
	r, w := io.Pipe()
	go func() {
		_, err := io.Copy(w, in)
		_ = w.CloseWithError(err)
	}()
	return r
}

func report(w io.Writer, message string) {
	if w != nil {
		fmt.Fprintln(w, message)
	}
}
