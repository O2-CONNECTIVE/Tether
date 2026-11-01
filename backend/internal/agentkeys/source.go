package agentkeys

import (
	"context"
	"errors"
	"io"
	"reflect"
)

// kernel is the private OS seam. Native implementations only query, open
// read-only, read and close; they never create, chmod, chown, re-ACL,
// impersonate or follow links.
type kernel interface {
	// enter and exit report the current thread's effective identity. They
	// fail if the thread impersonates or runs with changed/elevated identity.
	enter() (account, error)
	exit() (account, error)
	// open opens n without following a final link. parent is nil for the root
	// and otherwise the held, already-validated handle of the previous node.
	open(parent handle, n node) (handle, error)
}

type handle interface {
	io.Reader
	// inspect reads state from this held handle. ErrCleanup reports that an
	// auxiliary probe handle it opened could not be confirmed closed.
	inspect() (state, error)
	// resolve reports the identity currently bound to name in this directory,
	// without following links and without returning a handle.
	resolve(name string) (objectID, error)
	close() error
}

const maxReadCalls = 64

// readKey must run on a locked OS thread. It reports keepLocked when the
// thread's identity could not be confirmed, so the caller discards the thread.
// Every opened handle is closed exactly once in reverse order, also on failure.
func readKey(ctx context.Context, k kernel, style pathStyle, nodes []node) (data []byte, keepLocked bool, result error) {
	if len(nodes) < 3 {
		return nil, false, ErrConfig
	}
	acct, err := k.enter()
	if err != nil || !acct.valid() {
		if errors.Is(err, ErrCleanup) {
			return nil, true, ErrCleanup
		}
		return nil, true, ErrDenied
	}
	var handles []handle
	var buffer []byte
	defer func() {
		for i := len(handles) - 1; i >= 0; i-- {
			if handles[i].close() != nil {
				result = ErrCleanup
			}
		}
		after, err := k.exit()
		if err != nil || !reflect.DeepEqual(after, acct) {
			keepLocked = true
			if errors.Is(err, ErrCleanup) {
				result = ErrCleanup
			} else if result != ErrCleanup {
				result = ErrDenied
			}
		}
		if result != nil {
			wipe(buffer)
			data = nil
		}
	}()
	before := make([]state, 0, len(nodes))
	for i, n := range nodes {
		if ctx.Err() != nil {
			return nil, false, ErrDenied
		}
		var parent handle
		if i > 0 {
			parent = handles[i-1]
		}
		h, err := k.open(parent, n)
		if err != nil || h == nil {
			return nil, false, ErrDenied
		}
		handles = append(handles, h)
		root := state{}
		if i > 0 {
			root = before[0]
		}
		st, err := bound(acct, style, n, h, parent, root)
		if err != nil {
			return nil, false, err
		}
		before = append(before, st)
	}
	// Same-handle revalidation: every held object must still be bound to its
	// name and unchanged in identity, ownership and protection (see unchanged).
	// It runs once before the read (a component replaced during the walk is
	// refused without reading key bytes) and once after it.
	revalidate := func() error {
		for i, h := range handles {
			var parent handle
			if i > 0 {
				parent = handles[i-1]
			}
			st, err := bound(acct, style, nodes[i], h, parent, before[0])
			if err != nil {
				return err
			}
			if !unchanged(style, nodes[i], before[i], st) {
				return ErrDenied
			}
		}
		if ctx.Err() != nil {
			return ErrDenied
		}
		return nil
	}
	if err := revalidate(); err != nil {
		return nil, false, err
	}
	leaf := len(handles) - 1
	buffer = make([]byte, maxKeyBytes+1)
	n, err := readBounded(handles[leaf], buffer)
	if err != nil || int64(n) != before[leaf].size {
		return nil, false, ErrDenied
	}
	if err := revalidate(); err != nil {
		return nil, false, err
	}
	return buffer[:n], false, nil
}

// unchanged compares a revalidated state with the first inspection. The leaf
// must be identical in every field. Adding or removing any unrelated entry in
// a directory moves its size, link count and modification stamps (Darwin
// mtime/ctime, Windows last-write time), so only those are excluded for
// directories; identity, final path, kind, owner, grants, ACL presence,
// attributes/flags and the Windows creation time must still match.
func unchanged(style pathStyle, n node, before, after state) bool {
	if n.role != roleLeaf {
		before, after = withoutDirectoryStamps(style, before), withoutDirectoryStamps(style, after)
	}
	return reflect.DeepEqual(before, after)
}

func withoutDirectoryStamps(style pathStyle, st state) state {
	st.size, st.links = 0, 0
	st.stamp[2], st.stamp[3] = 0, 0 // Darwin ctime, Windows last-write time
	if style == posixPaths {
		st.stamp[0], st.stamp[1] = 0, 0 // Darwin mtime
	}
	return st
}

// bound inspects held handle h, applies the policy and, below the root, checks
// that n.name in the held parent still resolves to the same object.
func bound(acct account, style pathStyle, n node, h, parent handle, root state) (state, error) {
	st, err := h.inspect()
	if errors.Is(err, ErrCleanup) { // a probe handle opened by inspect was not confirmed closed
		return state{}, ErrCleanup
	}
	if err != nil || !checkNode(acct, style, n, st) {
		return state{}, ErrDenied
	}
	if parent == nil {
		return st, nil
	}
	if style.sameVolume() && root.id.volume != st.id.volume {
		return state{}, ErrDenied
	}
	id, err := parent.resolve(n.name)
	if errors.Is(err, ErrCleanup) {
		return state{}, ErrCleanup
	}
	if err != nil || id != st.id {
		return state{}, ErrDenied
	}
	return st, nil
}

// readBounded reads to EOF into buf. It fails on any error, on a call that
// makes no progress, on a reader that over-reports, or when buf fills (the
// file is larger than the key limit).
func readBounded(r io.Reader, buf []byte) (int, error) {
	n := 0
	for range maxReadCalls {
		if n == len(buf) {
			return n, ErrDenied
		}
		m, err := r.Read(buf[n:])
		if m < 0 || m > len(buf)-n {
			return n, ErrDenied
		}
		n += m
		if err == io.EOF {
			return n, nil
		}
		if err != nil || m == 0 {
			return n, ErrDenied
		}
	}
	return n, ErrDenied
}
