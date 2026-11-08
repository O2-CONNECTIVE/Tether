// Package fileread contains an unexposed, memory-tested bounded read contract.
// It has no path opener, OS adapter, authority issuer or production caller.
package fileread

import (
	"context"
	"errors"
	"io"
)

const (
	maxBytes    = 48 << 10 // internal ceiling, not a published tool payload budget
	maxReads    = 64
	regularFile = 1
)

var (
	errInvalid = errors.New("fileread: invalid read contract")
	errUnsafe  = errors.New("fileread: target is not a regular file")
	errChanged = errors.New("fileread: target observation changed")
	errDenied  = errors.New("fileread: read authorization refused")
	errInspect = errors.New("fileread: target inspection failed")
	errRead    = errors.New("fileread: bounded read failed")
	errCleanup = errors.New("fileread: handle cleanup unconfirmed")
)

// observation is an opaque adapter report, not a credential, content hash or
// atomic snapshot. The eventual adapter must define identity/version semantics.
// Nonzero tokens do not prove OS identity, root containment or authorization.
type observation struct {
	identity [32]byte
	version  [32]byte
	size     int64
	kind     uint8
}

type request struct {
	expected observation
	offset   int64
	maxBytes int
}

type result struct {
	data []byte
	next int64
	eof  bool // reached the observed size, not a promise about later mutations
}

// handle is already supplied by a trusted owner; this core cannot open files.
// Methods must be synchronous and cooperate with ctx. readAt must not retain p
// or use it after returning. close must attempt release even after cancellation.
// No method may create unreported resources or panic. Real OS rules are absent.
type handle interface {
	inspect(context.Context) (observation, error)
	readAt(context.Context, []byte, int64) (int, error)
	close() error
}

// authorize must recheck the existing authority for this exact target AND
// range. It must not issue authority or interpret tokens as approval. It is a
// trusted injected port, not an implementation of a file permission policy.
type authorize func(context.Context, request) error

// readOwned consumes sole ownership of h on entry, including invalid requests.
// The caller must not reuse, close or concurrently access h. Every non-nil h is
// closed exactly once before any result is returned; a failed close suppresses
// bytes and leaves cleanup unknown, with no automatic retry.
//
// A deadline is mandatory but checked cooperatively: blocked adapter calls or
// close can outlive it. No detached goroutine, timer or hard-stop guarantee is
// provided. Reinspection observes change; it cannot make the read atomic.
func readOwned(ctx context.Context, h handle, auth authorize, req request) (out result, err error) {
	if h == nil {
		return result{}, errInvalid
	}
	var buffer []byte
	defer func() {
		if h.close() != nil {
			err = errors.Join(err, errCleanup)
		}
		// A revoke/cancel during close must still withhold the prepared reply.
		if err == nil {
			err = checkAuthorization(ctx, auth, req)
		}
		clear(buffer)
		if err != nil {
			clear(out.data)
			out = result{}
		}
	}()
	if ctx == nil || auth == nil || !valid(req) {
		return result{}, errInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return result{}, errInvalid
	}
	if err := checkpoint(ctx, h, auth, req); err != nil {
		return result{}, err
	}
	length := int(min(int64(req.maxBytes), req.expected.size-req.offset))
	buffer = make([]byte, length)
	n := 0
	for calls := 0; n < length; calls++ {
		if calls == maxReads {
			return result{}, errRead
		}
		if calls > 0 {
			if err := checkpoint(ctx, h, auth, req); err != nil {
				return result{}, err
			}
		}
		m, readErr := h.readAt(ctx, buffer[n:], req.offset+int64(n))
		if err := ctx.Err(); err != nil {
			return result{}, err
		}
		if m < 0 || m > length-n || m == 0 {
			return result{}, errRead
		}
		n += m
		if readErr != nil && (readErr != io.EOF || n != length || req.offset+int64(n) != req.expected.size) {
			return result{}, errRead
		}
	}
	if err := checkpoint(ctx, h, auth, req); err != nil {
		return result{}, err
	}
	// Detach before close and clear scratch on every path. This is best-effort
	// clearing, not a guarantee that Go or an adapter has no other copies.
	out = result{data: append([]byte(nil), buffer...), next: req.offset + int64(n)}
	out.eof = out.next == req.expected.size
	return out, nil
}

func valid(req request) bool {
	return req.expected.identity != [32]byte{} && req.expected.version != [32]byte{} &&
		req.expected.kind == regularFile && req.expected.size >= 0 &&
		req.offset >= 0 && req.offset <= req.expected.size && req.maxBytes > 0 && req.maxBytes <= maxBytes
}

func checkpoint(ctx context.Context, h handle, auth authorize, req request) error {
	if err := checkAuthorization(ctx, auth, req); err != nil {
		return err
	}
	current, err := h.inspect(ctx)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return errInspect
	}
	if current.kind != regularFile {
		return errUnsafe
	}
	if current != req.expected {
		return errChanged
	}
	return checkAuthorization(ctx, auth, req)
}

func checkAuthorization(ctx context.Context, auth authorize, req request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := auth(ctx, req)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return errDenied
	}
	return nil
}
