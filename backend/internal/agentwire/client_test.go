package agentwire_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// A scripted peer stands in for a faulty or hostile server on a memory pipe.

func scripted(t *testing.T) (*agentwire.Client, net.Conn) {
	t.Helper()
	local, remote := net.Pipe()
	c, err := agentwire.NewClient(local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		_ = remote.Close()
	})
	return c, remote
}

// readRequest returns the wire id of the next request.
func readRequest(conn net.Conn) (uint64, error) {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return 0, err
	}
	body := make([]byte, binary.BigEndian.Uint32(head[:]))
	if _, err := io.ReadFull(conn, body); err != nil {
		return 0, err
	}
	var req struct {
		ID uint64 `json:"id"`
	}
	err := json.Unmarshal(body, &req)
	return req.ID, err
}

func reply(conn net.Conn, body string) error {
	_, err := conn.Write(frame(body))
	return err
}

func statusBody(id uint64, phase string) string {
	return fmt.Sprintf(`{"v":1,"id":%d,"status":{"profile":"mac_current_user","commandId":"%s","phase":"%s","exitKnown":false,"exitCode":0,"stop":"none","cleanup":"pending","terminal":false,"outputEnd":0,"truncated":false,"dropped":0}}`, id, foreignID, phase)
}

// serve answers each request with answer(id); an empty answer sends nothing.
func serve(conn net.Conn, answer func(id uint64) []string) {
	go func() {
		for {
			id, err := readRequest(conn)
			if err != nil {
				return
			}
			for _, body := range answer(id) {
				if reply(conn, body) != nil {
					return
				}
			}
		}
	}()
}

func closedSoon(c *agentwire.Client) bool {
	select {
	case <-c.Done():
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// After a Start reached the stream, an internal error or an unreadable status
// means the command may have started. It is reported as uncertain, not as a
// refusal, and is not resent.
func TestStartWithoutUsableReplyIsUncertain(t *testing.T) {
	for name, tc := range map[string]struct {
		body   func(id uint64) string
		cause  error
		closes bool
	}{
		"internal":     {func(id uint64) string { return fmt.Sprintf(`{"v":1,"id":%d,"error":"internal"}`, id) }, agentwire.ErrRemoteInternal, false},
		"unknown enum": {func(id uint64) string { return statusBody(id, "teleported") }, agentwire.ErrProtocol, true},
		"unknown code": {func(id uint64) string { return fmt.Sprintf(`{"v":1,"id":%d,"error":"maybe"}`, id) }, agentwire.ErrProtocol, true},
	} {
		t.Run(name, func(t *testing.T) {
			c, remote := scripted(t)
			requests := make(chan uint64, 4)
			serve(remote, func(id uint64) []string { requests <- id; return []string{tc.body(id)} })
			_, err := c.Start(finite(t), "req-1", []byte("true\n"), 1000)
			if !errors.Is(err, agentwire.ErrUncertain) || !errors.Is(err, tc.cause) {
				t.Fatalf("Start = %v", err)
			}
			if len(requests) != 1 {
				t.Fatalf("Start was sent %d times", len(requests))
			}
			select {
			case <-c.Done():
				if !tc.closes {
					t.Fatal("a reported internal error closed the stream")
				}
			default:
				if tc.closes && !closedSoon(c) {
					t.Fatal("a protocol violation left the stream open")
				}
			}
		})
	}
}

// A reply must answer an outstanding or abandoned request exactly once.
func TestUnsolicitedAndDuplicateRepliesAreProtocolViolations(t *testing.T) {
	c, remote := scripted(t)
	serve(remote, func(id uint64) []string { return []string{statusBody(id+7, "running")} })
	if _, err := c.Status(finite(t), foreignID); !errors.Is(err, agentwire.ErrUncertain) || !errors.Is(err, agentwire.ErrProtocol) {
		t.Fatalf("unsolicited reply = %v", err)
	}

	c, remote = scripted(t)
	serve(remote, func(id uint64) []string { return []string{statusBody(id, "running"), statusBody(id, "running")} })
	if _, err := c.Status(finite(t), foreignID); err != nil {
		t.Fatalf("first reply = %v", err)
	}
	if !closedSoon(c) {
		t.Fatal("a duplicate reply left the stream open")
	}
}

// Output may carry at most the requested bytes, not only the protocol limit.
func TestOutputLargerThanRequestedIsRejected(t *testing.T) {
	c, remote := scripted(t)
	serve(remote, func(id uint64) []string {
		return []string{fmt.Sprintf(`{"v":1,"id":%d,"output":{"chunks":[{"stream":"stdout","offset":0,"data":"YWJj"}],"next":3,"gap":0,"truncated":false,"dropped":0,"complete":false}}`, id)}
	})
	if _, err := c.Output(finite(t), foreignID, 0, 2); !errors.Is(err, agentwire.ErrProtocol) {
		t.Fatalf("oversized Output = %v", err)
	}
}

// A late reply to an abandoned request is consumed without harm, and the
// abandoned set is bounded: one abandonment too many ends the stream.
func TestLateRepliesAreDroppedAndAbandonmentIsBounded(t *testing.T) {
	c, remote := scripted(t)
	late := make(chan uint64, 1)
	serve(remote, func(id uint64) []string {
		if id == 1 {
			late <- id
			return nil
		}
		return []string{statusBody(id, "running")}
	})
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Status(short, foreignID); !errors.Is(err, agentwire.ErrUncertain) {
		t.Fatalf("unanswered Status = %v", err)
	}
	if err := reply(remote, statusBody(<-late, "running")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(finite(t), foreignID); err != nil {
		t.Fatalf("Status after a late reply = %v", err)
	}

	c, remote = scripted(t)
	serve(remote, func(uint64) []string { return nil })
	abandoned := 0
	for i := 0; i < 3*agentwire.MaxAbandoned; i++ {
		short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := c.Status(short, foreignID)
		cancel()
		if errors.Is(err, agentwire.ErrUncertain) {
			abandoned++
		}
		select {
		case <-c.Done():
			if abandoned != agentwire.MaxAbandoned+1 {
				t.Fatalf("stream closed after %d abandonments", abandoned)
			}
			return
		default:
		}
	}
	t.Fatal("abandoned requests grew without bound")
}
