package fileread

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"reflect"
	"testing"
	"time"
)

// Anonymous fake kernel: no filesystem, path, credential or OS identity.
type fakeHandle struct {
	state                      observation
	data                       []byte
	reads, inspections, closes int
	chunk                      int
	readHook                   func([]byte, int64) (int, error)
	inspectHook                func() error
	closeHook                  func() error
	buffers                    [][]byte
}

func fixture(data []byte) (*fakeHandle, request) {
	h := &fakeHandle{state: observation{identity: [32]byte{1}, version: [32]byte{2}, size: int64(len(data)), kind: regularFile}, data: append([]byte(nil), data...)}
	return h, request{expected: h.state, maxBytes: maxBytes}
}

func (h *fakeHandle) inspect(context.Context) (observation, error) {
	h.inspections++
	if h.inspectHook != nil {
		if err := h.inspectHook(); err != nil {
			return observation{}, err
		}
	}
	return h.state, nil
}

func (h *fakeHandle) readAt(_ context.Context, p []byte, off int64) (int, error) {
	h.reads++
	// Retained only to assert clearing/detachment. Production adapters must not
	// retain caller memory, and this fixture never accesses it concurrently.
	h.buffers = append(h.buffers, p)
	if h.readHook != nil {
		return h.readHook(p, off)
	}
	if h.chunk > 0 && len(p) > h.chunk {
		p = p[:h.chunk]
	}
	n := copy(p, h.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (h *fakeHandle) close() error {
	h.closes++
	if h.closeHook != nil {
		return h.closeHook()
	}
	return nil
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func allow(context.Context, request) error { return nil }

func assertRefused(t *testing.T, h *fakeHandle, out result, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(out, result{}) || h.closes != 1 {
		t.Fatalf("result=%+v err=%v want=%v closes=%d", out, err, want, h.closes)
	}
	for _, p := range h.buffers {
		if !bytes.Equal(p, make([]byte, len(p))) {
			t.Fatal("scratch payload retained on refusal")
		}
	}
}

func TestReadOwnedBoundedBinaryAndEOF(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []byte
		offset int64
		max    int
		want   []byte
		eof    bool
	}{
		{"binary", []byte{0, 0xff, 0xc3, 0x28, 10}, 1, 3, []byte{0xff, 0xc3, 0x28}, false},
		{"partial-at-end", []byte("abc"), 1, 8, []byte("bc"), true},
		{"exact-end", []byte("abc"), 0, 3, []byte("abc"), true},
		{"at-end", []byte("abc"), 3, 1, nil, true},
		{"empty", nil, 0, 1, nil, true},
		{"maximum", bytes.Repeat([]byte{7}, maxBytes+1), 0, maxBytes, bytes.Repeat([]byte{7}, maxBytes), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, req := fixture(tc.data)
			req.offset, req.maxBytes = tc.offset, tc.max
			checks := 0
			auth := func(_ context.Context, got request) error {
				checks++
				if got != req {
					t.Fatal("authorization did not receive exact target/range")
				}
				return nil
			}
			out, err := readOwned(deadline(t), h, auth, req)
			if err != nil || !bytes.Equal(out.data, tc.want) || out.next != tc.offset+int64(len(tc.want)) || out.eof != tc.eof || h.closes != 1 || checks != 5 {
				t.Fatalf("result=%+v err=%v closes=%d checks=%d", out, err, h.closes, checks)
			}
			if len(tc.want) == 0 && h.reads != 0 {
				t.Fatal("empty range performed read")
			}
			if h.reads > 1 {
				t.Fatal("unexpected extra probe beyond range")
			}
		})
	}
}

func TestReadOwnedInvalidRequestsCloseWithoutReading(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*request)
	}{
		{"negative-offset", func(r *request) { r.offset = -1 }},
		{"beyond-end", func(r *request) { r.offset = 4 }},
		{"zero-limit", func(r *request) { r.maxBytes = 0 }},
		{"negative-limit", func(r *request) { r.maxBytes = -1 }},
		{"over-limit", func(r *request) { r.maxBytes = maxBytes + 1 }},
		{"overflow-offset", func(r *request) { r.offset = math.MaxInt64 }},
		{"zero-identity", func(r *request) { r.expected.identity = [32]byte{} }},
		{"zero-version", func(r *request) { r.expected.version = [32]byte{} }},
		{"negative-size", func(r *request) { r.expected.size = -1 }},
		{"unknown-kind", func(r *request) { r.expected.kind = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, req := fixture([]byte("abc"))
			tc.change(&req)
			out, err := readOwned(deadline(t), h, allow, req)
			assertRefused(t, h, out, err, errInvalid)
			if h.reads != 0 || h.inspections != 0 {
				t.Fatal("invalid request touched target")
			}
		})
	}
}

func TestReadOwnedMaximumOffsetDoesNotOverflow(t *testing.T) {
	h, req := fixture(nil)
	h.state.size = math.MaxInt64
	req.expected = h.state
	req.offset = math.MaxInt64 - 1
	h.readHook = func(p []byte, off int64) (int, error) {
		if len(p) != 1 || off != math.MaxInt64-1 {
			t.Fatal("unbounded or overflowed range")
		}
		p[0] = 1
		return 1, nil
	}
	out, err := readOwned(deadline(t), h, allow, req)
	if err != nil || out.next != math.MaxInt64 || !out.eof || !bytes.Equal(out.data, []byte{1}) {
		t.Fatalf("result=%+v err=%v", out, err)
	}
}

func TestReadOwnedPartialReadsAndReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func([]byte, int64) (int, error)
		want error
	}{
		{"short-progress", func(p []byte, off int64) (int, error) { p[0] = byte(off); return 1, nil }, nil},
		{"final-eof", func(p []byte, _ int64) (int, error) { return copy(p, []byte{0, 1, 2}), io.EOF }, nil},
		{"partial-error", func(p []byte, _ int64) (int, error) { p[0] = 9; return 1, errors.New("private detail") }, errRead},
		{"full-error", func(p []byte, _ int64) (int, error) { return len(p), errors.New("private detail") }, errRead},
		{"premature-eof", func(p []byte, _ int64) (int, error) { p[0] = 9; return 1, io.EOF }, errRead},
		{"zero-progress", func([]byte, int64) (int, error) { return 0, nil }, errRead},
		{"negative-count", func([]byte, int64) (int, error) { return -1, nil }, errRead},
		{"oversized-count", func(p []byte, _ int64) (int, error) { return len(p) + 1, nil }, errRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, req := fixture([]byte{0, 1, 2})
			h.readHook = tc.hook
			out, err := readOwned(deadline(t), h, allow, req)
			if tc.want != nil {
				assertRefused(t, h, out, err, tc.want)
				return
			}
			if err != nil || !bytes.Equal(out.data, []byte{0, 1, 2}) || !out.eof || h.closes != 1 {
				t.Fatalf("result=%+v err=%v", out, err)
			}
		})
	}
}

func TestReadOwnedReadCallBudget(t *testing.T) {
	for _, size := range []int{maxReads, maxReads + 1} {
		h, req := fixture(bytes.Repeat([]byte{1}, size))
		h.chunk = 1
		out, err := readOwned(deadline(t), h, allow, req)
		if size > maxReads {
			assertRefused(t, h, out, err, errRead)
		} else if err != nil || len(out.data) != size {
			t.Fatalf("result=%+v err=%v", out, err)
		}
		if h.reads != maxReads {
			t.Fatalf("read calls=%d", h.reads)
		}
	}
}
