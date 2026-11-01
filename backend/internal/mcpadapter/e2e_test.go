package mcpadapter_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func commandID(t *testing.T, st map[string]any) string {
	t.Helper()
	id, _ := st["command_id"].(string)
	if !agentwire.ValidCommandID(id) {
		t.Fatalf("command_id = %v", st)
	}
	return id
}

func TestStartOutputStatusCancelThroughWireAndCore(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	st, isErr := h.start("req-1", "Get-Date\n")
	if isErr || st["cleanup"] != "pending" || st["terminal"] != false || st["profile"] != "windows_guest" {
		t.Fatalf("rescue_start = %v", st)
	}
	id := commandID(t, st)
	p := h.proc()
	emit(t, p, agentexec.Stdout, []byte("héllo\n"))
	emit(t, p, agentexec.Stderr, []byte{0xff, 0xfe})
	var chunks []any
	for cursor, deadline := 0.0, time.Now().Add(2*time.Second); cursor < 9; time.Sleep(time.Millisecond) {
		out, isErr := h.tool("rescue_output", map[string]any{"command_id": id, "cursor": cursor, "max_bytes": 1024})
		if isErr || out["command_id"] != id {
			t.Fatalf("rescue_output = %v", out)
		}
		chunks, cursor = append(chunks, out["chunks"].([]any)...), out["next_cursor"].(float64)
		if time.Now().After(deadline) {
			t.Fatalf("output stalled at %v", cursor)
		}
	}
	want := []any{
		map[string]any{"stream": "stdout", "offset": 0.0, "encoding": "utf-8", "data": "héllo\n"},
		map[string]any{"stream": "stderr", "offset": 7.0, "encoding": "base64", "data": "//4="},
	}
	if !equalJSON(chunks, want) {
		t.Fatalf("chunks = %v", chunks)
	}
	running, _ := h.tool("rescue_status", map[string]any{"command_id": id})
	if running["phase"] != "running" || running["output_end"] != 9.0 || running["cleanup"] != "pending" ||
		!equalJSON(running["exit"], map[string]any{"known": false}) {
		t.Fatalf("running status = %v", running)
	}
	canceled, isErr := h.tool("rescue_cancel", map[string]any{"command_id": id})
	if isErr || canceled["stop"] != "canceled" {
		t.Fatalf("rescue_cancel = %v", canceled)
	}
	final := h.awaitTerminal(id)
	// A forced stop reports stop and leaves the natural exit unknown.
	if final["phase"] != "canceled" || final["cleanup"] != "confirmed" || !equalJSON(final["exit"], map[string]any{"known": false}) {
		t.Fatalf("final status = %v", final)
	}
	if again, isErr := h.tool("rescue_cancel", map[string]any{"command_id": id}); isErr || !equalJSON(again, final) {
		t.Fatalf("repeated rescue_cancel = %v", again)
	}
	if !h.issuer.Snapshot().HasAuthority {
		t.Fatal("command cancel revoked the grant")
	}
	if err := h.session.Close(finite(t)); err != nil {
		t.Fatalf("owner Close = %v", err)
	}
}

func equalJSON(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

func TestUnconfirmedCleanupIsReportedAndOnlyTheOwnerRetries(t *testing.T) {
	h := newHarness(t, options{})
	h.runtime.configure = func(p *fakeProc) { p.releaseErrs = []error{errors.New("job still active")} }
	h.initialize()
	st, _ := h.start("req-1", "exit 0\n")
	id := commandID(t, st)
	p := h.proc()
	close(p.events)
	close(p.exited)
	final := h.awaitTerminal(id)
	if final["phase"] != "exited" || final["cleanup"] != "unknown" || !equalJSON(final["exit"], map[string]any{"known": true, "code": 0.0}) {
		t.Fatalf("final status = %v", final)
	}
	if res, isErr := h.start("req-2", "whoami\n"); !isErr || res["error"] != "cleanup_unknown" {
		t.Fatalf("start with unconfirmed cleanup = %v", res)
	}
	// Cleanup retry is owner-only; it is not reachable as a tool.
	retried, err := h.session.Cleanup(finite(t), agentexec.CleanupRequest{Version: agentexec.Version, Binding: h.session.Binding(), CommandID: id})
	if err != nil || retried.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("owner Cleanup = %+v, %v", retried, err)
	}
	if st, _ := h.tool("rescue_status", map[string]any{"command_id": id}); st["cleanup"] != "confirmed" {
		t.Fatalf("status after owner cleanup = %v", st)
	}
}

// A timeout above the host limit is rejected, not clamped, and starts nothing.
func TestTimeoutAboveTheHostLimitIsRejectedNotClamped(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	res, isErr := h.tool("rescue_start", map[string]any{"request_id": "req-1", "command": "whoami\n", "timeout_ms": 60 * 60 * 1000})
	if !isErr || res["error"] != "invalid" {
		t.Fatalf("rescue_start over the limit = %v", res)
	}
	if n := h.runtime.startCount(); n != 0 {
		t.Fatalf("guest starts = %d", n)
	}
	if st, isErr := h.tool("rescue_start", map[string]any{"request_id": "req-1", "command": "whoami\n", "timeout_ms": 30 * 60 * 1000}); isErr {
		t.Fatalf("rescue_start at the limit = %v", st)
	}
	h.proc()
}

func TestSameRequestIDReplayHasNoSecondSideEffect(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	first, _ := h.start("req-1", "whoami\n")
	h.proc()
	again, isErr := h.start("req-1", "whoami\n")
	if isErr || commandID(t, again) != commandID(t, first) {
		t.Fatalf("replay = %v, first %v", again, first)
	}
	if res, isErr := h.start("req-1", "hostname\n"); !isErr || res["error"] != "request_mismatch" {
		t.Fatalf("mismatched replay = %v", res)
	}
	if n := h.runtime.startCount(); n != 1 {
		t.Fatalf("guest starts = %d", n)
	}
}

func TestForeignCommandsAreNotFound(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	ref := agentexec.StatusRequest{Version: agentexec.Version, Binding: h.session.Binding()}
	owner, err := h.session.Start(agentexec.StartRequest{
		Version: agentexec.Version, Binding: h.session.Binding(), RequestID: "req-1", Mode: agentexec.ModeFull,
		Command: []byte("whoami\n"), TimeoutMillis: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.proc()
	for _, id := range []string{owner.CommandID, foreignID} {
		for _, name := range []string{"rescue_status", "rescue_cancel"} {
			if res, isErr := h.tool(name, map[string]any{"command_id": id}); !isErr || res["error"] != "not_found" {
				t.Fatalf("%s(%s) = %v", name, id, res)
			}
		}
		if res, isErr := h.tool("rescue_output", map[string]any{"command_id": id, "cursor": 0, "max_bytes": 16}); !isErr || res["error"] != "not_found" {
			t.Fatalf("rescue_output(%s) = %v", id, res)
		}
	}
	ref.CommandID = owner.CommandID
	if st, _ := h.session.Status(ref); st.Stop != agentexec.StopNone {
		t.Fatalf("foreign cancel reached the owner's command: %+v", st)
	}
	if _, err := h.session.Cancel(ref); err != nil {
		t.Fatal(err)
	}
}

func TestUncertainStartIsReportedAndNeverRetried(t *testing.T) {
	h := newHarness(t, options{callTimeout: 150 * time.Millisecond})
	h.initialize()
	h.gate.Hold()
	res, isErr := h.start("req-1", "whoami\n")
	if !isErr || res["error"] != "start_uncertain" {
		t.Fatalf("start without reply = %v", res)
	}
	h.proc()
	time.Sleep(50 * time.Millisecond)
	if n := h.runtime.startCount(); n != 1 {
		t.Fatalf("guest starts = %d", n)
	}
	h.gate.Release()
	again, isErr := h.start("req-1", "whoami\n")
	if isErr {
		t.Fatalf("explicit replay = %v", again)
	}
	commandID(t, again)
	if n := h.runtime.startCount(); n != 1 {
		t.Fatalf("explicit replay started the guest again: %d", n)
	}
}

func TestCancelledToolCallGetsNoResponse(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	h.gate.Hold()
	h.raw(`{"jsonrpc":"2.0","id":"c1","method":"tools/call","params":{"name":"rescue_status","arguments":{"command_id":"` + foreignID + `"}}}`)
	h.notify("notifications/cancelled", map[string]any{"requestId": "c1", "reason": "test"})
	if resp := h.request("ping", nil); resp["result"] == nil {
		t.Fatalf("ping = %v", resp)
	}
	h.gate.Release()
	if resp := h.request("ping", nil); resp["result"] == nil {
		t.Fatalf("ping = %v", resp)
	}
	h.noMessage(100 * time.Millisecond)
}

func TestConcurrencyIsBoundedAndPingStaysResponsive(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	h.gate.Hold()
	status := func(id int) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"rescue_status","arguments":{"command_id":"%s"}}}`, id, foreignID)
	}
	h.raw(status(40))
	h.raw(status(40))
	if resp := h.recv(); errorCode(t, resp) != -32600 || resp["id"] != 40.0 ||
		!strings.Contains(resp["error"].(map[string]any)["message"].(string), "original call with this id may still execute") {
		t.Fatalf("duplicate in-flight id = %v", resp)
	}
	for i := 1; i < mcpadapter.MaxConcurrent; i++ {
		h.raw(status(40 + i))
	}
	h.raw(status(50))
	if resp := h.recv(); errorCode(t, resp) != -32000 || resp["id"] != 50.0 {
		t.Fatalf("call over the bound = %v", resp)
	}
	if resp := h.request("ping", nil); resp["result"] == nil {
		t.Fatalf("ping while calls block = %v", resp)
	}
	h.gate.Release()
	seen := map[float64]bool{}
	for range mcpadapter.MaxConcurrent {
		resp := h.recv()
		result := resp["result"].(map[string]any)
		if result["isError"] != true || result["structuredContent"].(map[string]any)["error"] != "not_found" {
			t.Fatalf("blocked call = %v", resp)
		}
		seen[resp["id"].(float64)] = true
	}
	if len(seen) != mcpadapter.MaxConcurrent {
		t.Fatalf("responses = %v", seen)
	}
}

func TestTransportLossIsReportedWithoutResume(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	st, _ := h.start("req-1", "whoami\n")
	id := commandID(t, st)
	p := h.proc()
	var mu sync.Mutex
	var atTerminate supervisor.State
	p.mu.Lock()
	p.onTerminate = func() {
		mu.Lock()
		defer mu.Unlock()
		atTerminate = h.authority.Snapshot(h.clock.Now()).State
	}
	p.mu.Unlock()
	// One call is in flight when the owner ends the channel.
	h.gate.Hold()
	h.raw(`{"jsonrpc":"2.0","id":"t1","method":"tools/call","params":{"name":"rescue_status","arguments":{"command_id":"` + id + `"}}}`)
	time.Sleep(50 * time.Millisecond)
	h.stopWire()
	resp := h.recv()
	result := resp["result"].(map[string]any)
	if code := result["structuredContent"].(map[string]any)["error"]; resp["id"] != "t1" || result["isError"] != true || (code != "uncertain" && code != "transport_closed") {
		t.Fatalf("in-flight call at transport loss = %v", resp)
	}
	mu.Lock()
	state := atTerminate
	mu.Unlock()
	if state != supervisor.Revoked {
		t.Fatalf("authority at Terminate = %q", state)
	}
	// The adapter stops instead of answering every later call with the loss.
	if err := h.waitDone(); !errors.Is(err, mcpadapter.ErrChannelClosed) {
		t.Fatalf("Serve after transport loss = %v", err)
	}
	for line := range h.lines {
		t.Fatalf("message after the channel ended: %s", line)
	}
	if err := h.session.Close(finite(t)); err != nil {
		t.Fatalf("owner Close = %v", err)
	}
	if n := h.runtime.startCount(); n != 1 {
		t.Fatalf("guest starts = %d", n)
	}
}

func TestRevokeAndExpiryDenyNewWork(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	st, _ := h.start("req-1", "whoami\n")
	id := commandID(t, st)
	h.proc()
	h.session.Revoke()
	if res, isErr := h.start("req-2", "whoami\n"); !isErr || res["error"] != "denied" || !strings.Contains(res["message"].(string), "This call started nothing") {
		t.Fatalf("start after revoke = %v", res)
	}
	// Denied output must not claim that nothing ever ran.
	if res, isErr := h.tool("rescue_output", map[string]any{"command_id": id, "cursor": 0, "max_bytes": 16}); !isErr || res["error"] != "denied" ||
		strings.Contains(res["message"].(string), "started nothing") || !strings.Contains(res["message"].(string), "rescue_status") {
		t.Fatalf("output after revoke = %v", res)
	}
	final := h.awaitTerminal(id)
	if final["stop"] != "revoked" || final["cleanup"] != "confirmed" {
		t.Fatalf("status after revoke = %v", final)
	}

	expired := newHarness(t, options{})
	expired.initialize()
	expired.clock.Advance(2 * time.Hour)
	if res, isErr := expired.start("req-1", "whoami\n"); !isErr || res["error"] != "denied" {
		t.Fatalf("start after expiry = %v", res)
	}
	if expired.issuer.Snapshot().HasAuthority || expired.runtime.startCount() != 0 {
		t.Fatal("expired grant stayed live or started the guest")
	}
}

// The profile comes read-only from the remote Session; the tool text states
// the Mac current-user guarantee instead of promising guest isolation.
func TestMacCurrentUserProfileAndGuaranteeAreStated(t *testing.T) {
	h := newHarness(t, options{mac: true})
	h.initialize()
	st, isErr := h.start("req-1", "id -un\n")
	if isErr || st["profile"] != "mac_current_user" {
		t.Fatalf("rescue_start = %v", st)
	}
	h.proc()
	tools := h.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	start := tools[0].(map[string]any)["description"].(string)
	for _, want := range []string{"mac_current_user", "no sandbox", "no isolation", "process group", "does not stop"} {
		if !strings.Contains(start, want) {
			t.Fatalf("rescue_start description lacks %q: %s", want, start)
		}
	}
	if strings.Contains(start, "non-administrator guest shell") {
		t.Fatalf("description promises a guest for every profile: %s", start)
	}
	if _, isErr := h.tool("rescue_cancel", map[string]any{"command_id": commandID(t, st)}); isErr {
		t.Fatal("rescue_cancel failed")
	}
}

// Large escaped output is bounded by the encoded result size, including the
// duplicated text content, and the rest is delivered from next_cursor.
func TestEscapedOutputIsBoundedByEncodedSize(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	st, _ := h.start("req-1", "control-bytes\n")
	id := commandID(t, st)
	p := h.proc()
	want := strings.Repeat("\x01", 40000)
	emit(t, p, agentexec.Stdout, []byte(want))
	var got strings.Builder
	replies := 0
	for cursor, deadline := 0.0, time.Now().Add(5*time.Second); cursor < 40000; replies++ {
		out, isErr := h.tool("rescue_output", map[string]any{"command_id": id, "cursor": cursor, "max_bytes": agentwire.MaxOutputBytes})
		if isErr || h.lastLen > mcpadapter.MaxToolResultBytes+256 {
			t.Fatalf("rescue_output: %d bytes, %v", h.lastLen, out["error"])
		}
		for _, c := range out["chunks"].([]any) {
			chunk := c.(map[string]any)
			if chunk["encoding"] != "utf-8" || chunk["offset"] != float64(got.Len()) {
				t.Fatalf("chunk = %v", chunk["offset"])
			}
			got.WriteString(chunk["data"].(string))
		}
		if next := out["next_cursor"].(float64); next != float64(got.Len()) || (next < 40000 && out["complete"] != false) {
			t.Fatalf("next_cursor %v after %d bytes", next, got.Len())
		}
		cursor = out["next_cursor"].(float64)
		if time.Now().After(deadline) {
			t.Fatalf("output stalled at %v", cursor)
		}
	}
	if replies < 2 || got.String() != want {
		t.Fatalf("replies=%d, got %d bytes", replies, got.Len())
	}
}

// scriptedHarness runs the MCP adapter against a hand-written wire peer that
// answers every Start with body(id), standing in for a faulty server.
func scriptedHarness(t *testing.T, body func(id uint64) string) *harness {
	t.Helper()
	local, remote := net.Pipe()
	client, err := agentwire.NewClient(local)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, client: client, lines: make(chan []byte, 64), done: make(chan error, 1)}
	go func() {
		for {
			var head [4]byte
			if _, err := io.ReadFull(remote, head[:]); err != nil {
				return
			}
			raw := make([]byte, binary.BigEndian.Uint32(head[:]))
			if _, err := io.ReadFull(remote, raw); err != nil {
				return
			}
			var req struct {
				ID uint64 `json:"id"`
			}
			_ = json.Unmarshal(raw, &req)
			reply := body(req.ID)
			frame := binary.BigEndian.AppendUint32(nil, uint32(len(reply)))
			if _, err := remote.Write(append(frame, reply...)); err != nil {
				return
			}
		}
	}()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h.in = inW
	go func() {
		scanner := bufio.NewScanner(outR)
		for scanner.Scan() {
			h.lines <- append([]byte(nil), scanner.Bytes()...)
		}
		close(h.lines)
	}()
	go func() {
		err := mcpadapter.Serve(context.Background(), mcpadapter.Config{Backend: client}, inR, outW)
		_ = outW.Close()
		h.done <- err
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		go func() {
			for range h.lines {
			}
		}()
		_ = h.waitDone()
		_ = client.Close()
		_ = remote.Close()
	})
	return h
}

// After a start reached the channel, an internal error or an unreadable reply
// is a possibly started command: start_uncertain with same-request_id recovery,
// never a refusal and never an automatic replay.
func TestStartWithoutUsableReplyIsReportedUncertain(t *testing.T) {
	for name, body := range map[string]func(uint64) string{
		"internal": func(id uint64) string { return fmt.Sprintf(`{"v":1,"id":%d,"error":"internal"}`, id) },
		"unknown enum": func(id uint64) string {
			return fmt.Sprintf(`{"v":1,"id":%d,"status":{"profile":"windows_guest","commandId":"%s","phase":"teleported","exitKnown":false,"exitCode":0,"stop":"none","cleanup":"pending","terminal":false,"outputEnd":0,"truncated":false,"dropped":0}}`, id, foreignID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := scriptedHarness(t, body)
			h.initialize()
			res, isErr := h.start("req-1", "whoami\n")
			if !isErr || res["error"] != "start_uncertain" || !strings.Contains(res["message"].(string), "not retried") {
				t.Fatalf("rescue_start = %v", res)
			}
		})
	}
}

func TestEndOfInputCancelsInFlightCallsWithoutReply(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	h.gate.Hold()
	h.raw(`{"jsonrpc":"2.0","id":"late","method":"tools/call","params":{"name":"rescue_status","arguments":{"command_id":"` + foreignID + `"}}}`)
	_ = h.in.Close()
	if err := h.waitDone(); err != nil {
		t.Fatalf("Serve after end of input = %v", err)
	}
	for line := range h.lines {
		t.Fatalf("message after end of input: %s", line)
	}
}

func TestServeRefusesMissingBackend(t *testing.T) {
	if err := mcpadapter.Serve(finite(t), mcpadapter.Config{}, nil, nil); !errors.Is(err, mcpadapter.ErrUnconfigured) {
		t.Fatalf("Serve without backend = %v", err)
	}
}
