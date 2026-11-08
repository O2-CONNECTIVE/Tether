package fileread

import (
	"context"
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type selectedResult struct {
	result
	observed observation
}

type selectedHandle struct {
	files  []*os.File // root first, leaf last; transferred sole ownership
	states []nativeState
	names  []string
}

// Per-call acquisition operations keep fault-injection local to a fixture.
// Chain reinspection always uses the native operations, not these callbacks.
type selectedAcquisition struct {
	lookup func(int, string) (nativeState, error)
	open   func(int, string, int, uint32) (int, error)
}

// readSelectedOwned consumes root even on refusal. The root must already be a
// separately approved owned directory handle, never a path from a tool call.
// No production caller or root-selection/permission provider exists. Retained
// descriptors plus observational checks are NOT atomic pathname containment.
func readSelectedOwned(ctx context.Context, root *os.File, permission readPermission, req selectedRequest) (out selectedResult, err error) {
	return readSelectedAcquiring(ctx, root, permission, req, selectedAcquisition{inspectName, unix.Openat})
}

func readSelectedAcquiring(ctx context.Context, root *os.File, permission readPermission, req selectedRequest, acquire selectedAcquisition) (out selectedResult, err error) {
	if root == nil {
		return selectedResult{}, errInvalid
	}
	h := &selectedHandle{files: []*os.File{root}}
	transferred := false
	defer func() {
		if !transferred && h.close() != nil {
			err = errors.Join(err, errCleanup)
		}
		if err != nil {
			clear(out.data)
			out = selectedResult{}
		}
	}()
	if acquire.lookup == nil || acquire.open == nil {
		return selectedResult{}, errInvalid
	}
	access := readAccess{request: req, rootIdentity: permission.rootIdentity}
	if err := permission.check(ctx, access); err != nil {
		return selectedResult{}, err
	}
	initial, err := inspectNative(int(root.Fd()))
	if err != nil {
		return selectedResult{}, errInspect
	}
	if !initial.directory() || initial.uid != uint32(os.Geteuid()) || initial.identity() != permission.rootIdentity {
		return selectedResult{}, errUnsafe
	}
	h.states = append(h.states, initial)
	parts := strings.Split(req.path, "/")
	for i, name := range parts {
		if err := permission.check(ctx, access); err != nil {
			return selectedResult{}, err
		}
		// The provider call may outlive a persistent root/ancestor change.
		// Refuse it before another child lookup/open through a retained fd.
		// This check is still not atomic with the following lookup or open.
		if _, err := h.inspect(ctx); err != nil {
			return selectedResult{}, errChanged
		}
		parent := int(h.files[len(h.files)-1].Fd())
		// The precheck avoids intentionally opening special files. Replacement
		// between this check and open can still invoke a device's open handler;
		// O_NONBLOCK is not a side-effect-free-open guarantee. This is one of
		// the unresolved reasons this candidate has no production caller.
		before, err := acquire.lookup(parent, name)
		if err != nil || !before.safeChild(initial, i == len(parts)-1) {
			return selectedResult{}, errUnsafe
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := acquire.open(parent, name, flags, 0)
		if err != nil {
			return selectedResult{}, errRead
		}
		file := os.NewFile(uintptr(fd), "selected-entry")
		if file == nil {
			if unix.Close(fd) != nil {
				return selectedResult{}, errors.Join(errRead, errCleanup)
			}
			return selectedResult{}, errRead
		}
		h.files = append(h.files, file)
		after, err := inspectNative(fd)
		if err != nil || after != before || !after.safeChild(initial, i == len(parts)-1) {
			return selectedResult{}, errChanged
		}
		h.states = append(h.states, after)
		h.names = append(h.names, name)
		if _, err := h.inspect(ctx); err != nil {
			return selectedResult{}, errChanged
		}
	}
	observed := h.states[len(h.states)-1].observation()
	if req.expectedVersion != [32]byte{} && req.expectedVersion != observed.version {
		return selectedResult{}, errChanged
	}
	access.resolved, access.target = true, observed
	authorize := func(ctx context.Context, actual request) error {
		if actual != (request{expected: observed, offset: req.offset, maxBytes: req.maxBytes}) {
			return errDenied
		}
		return permission.check(ctx, access)
	}
	transferred = true
	out.result, err = readOwned(ctx, h, authorize, request{expected: observed, offset: req.offset, maxBytes: req.maxBytes})
	if err == nil {
		out.observed = observed
	}
	return out, err
}

func (h *selectedHandle) inspect(ctx context.Context) (observation, error) {
	if err := ctx.Err(); err != nil {
		return observation{}, err
	}
	if len(h.states) != len(h.files) || len(h.names)+1 != len(h.files) {
		return observation{}, errInspect
	}
	for i, file := range h.files {
		current, err := inspectNative(int(file.Fd()))
		if err != nil || current != h.states[i] {
			return observation{}, errChanged
		}
		if i > 0 {
			named, err := inspectName(int(h.files[i-1].Fd()), h.names[i-1])
			if err != nil || named != current {
				return observation{}, errChanged
			}
		}
	}
	return h.states[len(h.states)-1].observation(), nil
}

func (h *selectedHandle) readAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return unix.Pread(int(h.files[len(h.files)-1].Fd()), p, offset)
}

func (h *selectedHandle) close() error {
	var err error
	for i := len(h.files) - 1; i >= 0; i-- {
		if h.files[i].Close() != nil {
			err = errCleanup
		}
	}
	h.files = nil
	return err
}
