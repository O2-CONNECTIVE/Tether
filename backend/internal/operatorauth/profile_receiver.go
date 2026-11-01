package operatorauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// All fields come from a retained launch record, never from the received frame,
// argv, environment, or a handle-number claim. launchID is correlation only.
type profileReceiverBinding struct {
	launchID    string
	profileRead uintptr
	child       helperBinding
}

func (b profileReceiverBinding) valid() bool {
	c := b.child
	return profileLaunchID(b.launchID) && reportHandle(b.profileRead) && c.pid != 0 && c.creation != 0 && c.tokenID != 0 && c.childModifiedID != 0 && c.sourceTokenID != 0 && c.sourceModifiedID != 0 && reportSIDValid(c.user) && localHelperImage(c.imagePath)
}

// The source must independently prove that the very inherited pipe belongs to
// the retained protected parent and this launch. Payload fields cannot create
// this proof. Production uses helperLaunchReceiver only; nil stays unconfigured. A source
// also retains its evidence after this receiver closes its local pipe handle.
type profileReceiverSource interface {
	check(context.Context, profileReceiverBinding) error
}
type profileReceiverOps struct {
	adopt func(uintptr) error
	start func(context.Context, uintptr, []byte) (*profileSyncOwner, error)
	close func(uintptr) error
}

type profileReceiver struct {
	mu        sync.Mutex
	binding   profileReceiverBinding
	source    profileReceiverSource
	ops       profileReceiverOps
	budget    time.Duration
	handle    uintptr
	buffer    []byte
	pending   *profileSyncOwner
	used      bool
	uncertain bool
	closing   chan receiverCloseResult
}
type receiverCloseResult struct {
	err     error
	unknown bool
}

// newProfileReceiver takes ownership of the sole inherited profile reader only
// when it returns a non-nil owner, including adoption failure. Any failure
// retains this same owner for bounded close retry.
func newProfileReceiver(ctx context.Context, handle uintptr, binding profileReceiverBinding, source profileReceiverSource, ops profileReceiverOps, budget time.Duration) (*profileReceiver, error) {
	if source == nil || ops.adopt == nil || ops.start == nil || ops.close == nil {
		return nil, ErrUnconfigured
	}
	if !reportContextValid(ctx) || !binding.valid() || binding.profileRead != handle || budget <= 0 || budget > time.Minute {
		return nil, ErrDenied
	}
	r := &profileReceiver{binding: binding, source: source, ops: ops, budget: budget, handle: handle, buffer: make([]byte, 4096)}
	if err := r.adopt(ctx); err != nil {
		r.used = true
		return r, err
	}
	return r, nil
}
func (r *profileReceiver) adopt(ctx context.Context) (result error) {
	defer func() {
		if recover() != nil {
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
	}()
	if err := r.ops.adopt(r.handle); err != nil {
		return errors.Join(ErrDenied, err)
	}
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
func (r *profileReceiver) check(ctx context.Context) error {
	if !reportContextValid(ctx) || r.source == nil {
		return ErrDenied
	}
	if err := r.source.check(ctx, r.binding); err != nil {
		return errors.Join(ErrDenied, err)
	}
	if !reportContextValid(ctx) {
		return ErrDenied
	}
	return nil
}

func (r *profileReceiver) consume(ctx context.Context) (profile helperProfile, result error) {
	if r == nil {
		return profile, ErrUnconfigured
	}
	if !r.mu.TryLock() {
		return profile, ErrUnconfirmed
	}
	defer r.mu.Unlock()
	defer func() {
		if recover() != nil {
			r.uncertain = true
			profile = helperProfile{}
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
	}()
	if r.used {
		return profile, ErrDenied
	}
	r.used = true
	if !reportContextValid(ctx) || !reportHandle(r.handle) {
		return profile, ErrDenied
	}
	if err := r.check(ctx); err != nil {
		return profile, err
	}
	stream := newProfileStream(r.binding.launchID)
	for reportContextValid(ctx) {
		// The previous synchronous owner must actually join and release before the
		// same buffer can be read or handed to a new worker.
		var err error
		r.pending, err = r.ops.start(ctx, r.handle, r.buffer)
		if err != nil || r.pending == nil {
			return profile, errors.Join(ErrDenied, err)
		}
		n, readErr := r.pending.wait(ctx, r.budget, false)
		if err = r.pending.release(); err != nil {
			return profile, errors.Join(ErrUnconfirmed, readErr, err)
		}
		r.pending = nil
		if !reportContextValid(ctx) || n > uint32(len(r.buffer)) {
			return profile, ErrDenied
		}
		if readErr == errProfileSyncEOF && n == 0 {
			if _, err = stream.accept(nil, true); err != nil {
				return profile, err
			}
			if err = r.check(ctx); err != nil {
				return profile, err
			}
			if err = r.closeHandle(ctx); err != nil {
				return profile, err
			}
			profile, err = stream.consume()
			if err != nil || !reportContextValid(ctx) {
				return helperProfile{}, errors.Join(ErrDenied, err)
			}
			return profile, nil
		}
		// Only the native adapter's exact ERROR_BROKEN_PIPE sentinel with zero
		// bytes is EOF. nil/0, io.EOF, joined errors, and error+data all fail closed.
		if readErr != nil || n == 0 {
			return profile, errors.Join(ErrDenied, readErr)
		}
		if _, err = stream.accept(r.buffer[:n], false); err != nil {
			return profile, err
		}
	}
	return profile, ErrDenied
}
func (r *profileReceiver) closeHandle(ctx context.Context) error {
	if r.uncertain || r.pending != nil || !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	if r.handle != 0 {
		if r.closing == nil {
			r.closing = make(chan receiverCloseResult, 1)
			result, handle, closeFn := r.closing, r.handle, r.ops.close
			go func() {
				out := receiverCloseResult{}
				defer func() {
					if recover() != nil {
						out.unknown = true
						out.err = ErrUnconfirmed
					}
					result <- out
				}()
				out.err = closeFn(handle)
			}()
		}
		select {
		case out := <-r.closing:
			r.closing = nil
			if out.unknown {
				r.uncertain = true
			}
			if out.err != nil {
				return errors.Join(ErrUnconfirmed, out.err)
			}
		case <-ctx.Done():
			return errors.Join(ErrUnconfirmed, ctx.Err())
		}
		r.handle = 0
	}
	r.buffer = nil
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	return nil
}

// close never closes a pipe under an unjoined worker. Cancel success itself is
// not proof; only the exact pending owner's completed release permits closing.
func (r *profileReceiver) close(ctx context.Context) (result error) {
	if r == nil {
		return nil
	}
	if !r.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer r.mu.Unlock()
	defer func() {
		if recover() != nil {
			r.uncertain = true
			result = ErrUnconfirmed
		}
	}()
	r.used = true
	if r.uncertain {
		return ErrUnconfirmed
	}
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	if r.pending != nil {
		_, err := r.pending.wait(ctx, r.budget, true)
		if releaseErr := r.pending.release(); releaseErr != nil {
			return errors.Join(ErrUnconfirmed, err, releaseErr)
		}
		r.pending = nil
	}
	return r.closeHandle(ctx)
}

func newNativeProfileReceiver(ctx context.Context, handle uintptr, binding profileReceiverBinding, source profileReceiverSource, budget time.Duration) (*profileReceiver, error) {
	if source == nil {
		return nil, ErrUnconfigured
	}
	if !reportContextValid(ctx) || !binding.valid() || binding.profileRead != handle || budget <= 0 || budget > time.Minute {
		return nil, ErrDenied
	}
	ops, err := nativeProfileReceiverOps()
	if err != nil {
		return nil, err
	}
	return newProfileReceiver(ctx, handle, binding, source, ops, budget)
}

// run is the private child entry seam. It cannot construct an authenticating
// Helper until the independent source, entire frame, real EOF and source
// recheck succeeded. RunFixed supplies no source and remains closed.
func (r *profileReceiver) run(ctx context.Context, ops helperOps) error {
	if r == nil {
		return ErrUnconfigured
	}
	if ops.thread == nil || !reportHandle(ops.pipe) || ops.pipe == r.binding.profileRead || ops.check == nil || ops.read == nil || ops.publish == nil {
		return ErrDenied
	}
	profile, err := r.consume(ctx)
	if err != nil {
		if errors.Is(err, ErrFatal) {
			return err
		}
		cleanup, cancel := context.WithTimeout(context.Background(), r.budget)
		defer cancel()
		return errors.Join(err, r.close(cleanup))
	}
	check := ops.check
	ops.check = func(c context.Context) error {
		if err := r.check(c); err != nil {
			return err
		}
		if err := check(c); err != nil {
			return err
		}
		return r.check(c)
	}
	helper := &Helper{profile: profile, ops: ops}
	return helper.Run(ctx)
}
