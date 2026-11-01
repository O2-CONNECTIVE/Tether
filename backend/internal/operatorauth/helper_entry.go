package operatorauth

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"time"
)

// A lease comes only from retained, same-launch OS evidence. Its snapshot is
// read once and never supplemented by argv. prepare/check/close must honor the
// supplied finite context on this fixed thread. close releases evidence only;
// the entry owns all three inherited handles as soon as prepare returns a lease.
type helperEntrySource interface {
	prepare(context.Context) (helperEntryLease, error)
}
type helperEntryLease interface {
	config() helperEntryConfig
	receiverSource() profileReceiverSource
	check(context.Context, helperEntryConfig) error
	close(context.Context) error
}
type helperEntryConfig struct {
	binding                   profileReceiverBinding
	operatorRead, reportWrite uintptr
	runBudget, cleanup        time.Duration
}

func (c helperEntryConfig) valid() bool {
	return c.binding.valid() && reportHandle(c.operatorRead) && reportHandle(c.reportWrite) &&
		c.operatorRead != c.reportWrite && c.operatorRead != c.binding.profileRead && c.reportWrite != c.binding.profileRead &&
		c.runBudget > 0 && c.runBudget <= time.Minute && c.cleanup > 0 && c.cleanup <= time.Minute
}
func (c helperEntryConfig) matches(args []string) bool {
	return len(args) == 5 && args[0] == "--operator-auth-internal" &&
		args[1] == strconv.FormatUint(uint64(c.operatorRead), 10) &&
		args[2] == strconv.FormatUint(uint64(c.reportWrite), 10) &&
		args[3] == strconv.FormatUint(uint64(c.binding.profileRead), 10) && args[4] == c.binding.launchID
}

type helperEntryFactory struct {
	message  func(uintptr, endpointRole, time.Duration) (*messageEndpoint, error)
	profile  func(context.Context, uintptr, profileReceiverBinding, profileReceiverSource, time.Duration) (*profileReceiver, error)
	thread   func() threadKernel
	closeRaw func(uintptr) error
}

func (f helperEntryFactory) valid() bool {
	return f.message != nil && f.profile != nil && f.thread != nil && f.closeRaw != nil
}

// protectedHelperLaunch is an unapproved candidate: a value compiled into both
// the supervisor and helper images. The supervisor's root-protected Record pins
// the helper image by path and SHA-256 before SCM or CreateProcess maps it, so
// this value would be covered by that digest; the supervisor additionally
// requires it to equal the Record. No protected producer for a deployed value
// exists, so it stays unset and inactive: argv, environment and files never
// assign it.
var protectedHelperLaunch helperLaunchFixed

const supplierEntryArg = "--auth-supplier-internal"

// Arguments select one fixed role and carry locators only; they cannot supply
// protection, evidence, profile, Service names or any budget.
func runNativeFixedHelper(ctx context.Context, args []string) error {
	return runHelperProcess(ctx, args, protectedHelperLaunch, nativeHelperLaunchWitness(), nativeSupplierEntry(), helperEntryFactory{
		message: newNativeMessageEndpoint, profile: newNativeProfileReceiver,
		thread: func() threadKernel { return &nativeThread{} }, closeRaw: closeNativeHelperEntryHandle,
	})
}

// runHelperProcess is the single production dispatch for the helper image. An
// unset or invalid compiled value refuses before any native call.
func runHelperProcess(ctx context.Context, args []string, fixed helperLaunchFixed, witness helperLaunchWitness, supplier supplierEntryKernel, factory helperEntryFactory) error {
	if !fixed.valid() {
		return ErrUnconfigured
	}
	if ctx == nil || ctx.Err() != nil {
		return ErrDenied
	}
	if len(args) == 1 && args[0] == supplierEntryArg {
		return runSupplierEntry(ctx, fixed, supplier)
	}
	// The preparation deadline comes from the same compiled value, not argv.
	bounded, cancel := context.WithTimeout(ctx, fixed.runBudget+fixed.cleanup)
	defer cancel()
	source, err := newHelperLaunchSource(fixed, args, witness)
	if err != nil || source == nil {
		return errors.Join(ErrUnconfigured, err)
	}
	return runFixedHelper(bounded, args, source, factory)
}

// supplierEntryKernel is the SCM dispatcher of the fixed supplier Service. run
// blocks until the dispatcher returns and serves through serveSupplierEntry.
type supplierEntryKernel interface {
	run(ctx context.Context, name string, lifetime time.Duration) error
}

// runSupplierEntry is the dedicated LocalService supplier. It opens no token,
// pipe or handle: the supervisor opens its process token. It reports running
// and ends at a stop request, at the compiled lifetime, or on cancellation, so
// a generation whose owner retired fatally still stops without that owner.
func runSupplierEntry(ctx context.Context, fixed helperLaunchFixed, k supplierEntryKernel) error {
	if k == nil {
		return ErrUnconfigured
	}
	return k.run(ctx, fixed.supplierService, fixed.supplierLifetime())
}

// serveSupplierEntry is the SCM handler body. handle reports whether a request
// is a stop; it may answer an interrogation itself but never extends lifetime.
func serveSupplierEntry[T any](ctx context.Context, lifetime time.Duration, requests <-chan T, running func(), handle func(T) bool) {
	timer := time.NewTimer(lifetime)
	defer timer.Stop()
	running()
	for {
		select {
		case r, ok := <-requests:
			if !ok || handle(r) {
				return
			}
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// runFixedHelper is a private dedicated-process entry, never a registration API.
// A future source still needs an independently protected preparation deadline;
// a caller without a finite context cannot trigger even source preparation.
func runFixedHelper(ctx context.Context, args []string, source helperEntrySource, factory helperEntryFactory) (result error) {
	if source == nil {
		return ErrUnconfigured
	}
	if !reportContextValid(ctx) {
		return ErrDenied
	}
	runtime.LockOSThread()
	fatal := false
	var owner *helperEntryOwner
	defer func() {
		if recover() != nil {
			result = errors.Join(result, ErrFatal, ErrUnconfirmed)
		}
		if errors.Is(result, ErrFatal) {
			fatal = true
		}
		if !fatal {
			if owner != nil {
				result = errors.Join(result, owner.close())
				if errors.Is(result, ErrFatal) {
					return
				}
			}
			runtime.UnlockOSThread()
			if !reportContextValid(ctx) {
				result = errors.Join(result, ErrDenied)
			}
		}
	}()
	lease, err := source.prepare(ctx)
	if lease == nil {
		return errors.Join(ErrDenied, err)
	}
	owner = &helperEntryOwner{lease: lease, factory: factory}
	owner.config = lease.config()
	c := owner.config
	owner.raw = [3]uintptr{c.operatorRead, c.reportWrite, c.binding.profileRead}
	if err != nil {
		return errors.Join(ErrDenied, err)
	}
	if !reportContextValid(ctx) || !c.valid() || !c.matches(args) {
		return ErrDenied
	}
	if !factory.valid() {
		return ErrUnconfigured
	}
	run, cancel := context.WithTimeout(ctx, c.runBudget)
	defer cancel()
	check := func(checkCtx context.Context) error {
		if !reportContextValid(checkCtx) {
			return ErrDenied
		}
		if err := lease.check(checkCtx, c); err != nil {
			return errors.Join(ErrDenied, err)
		}
		if !reportContextValid(checkCtx) {
			return ErrDenied
		}
		return nil
	}
	if err = check(run); err != nil {
		return err
	}
	receiverSource := lease.receiverSource()
	if receiverSource == nil {
		return ErrUnconfigured
	}
	if err = check(run); err != nil {
		return err
	}
	if err = owner.adoptMessage(0, endpointServerRead); err != nil {
		return err
	}
	if err = check(run); err != nil {
		return err
	}
	if err = owner.adoptMessage(1, endpointClientWrite); err != nil {
		return err
	}
	if err = check(run); err != nil {
		return err
	}
	if err = owner.adoptProfile(run, receiverSource); err != nil {
		return err
	}
	if err = check(run); err != nil {
		return err
	}
	return owner.receiver.run(run, helperOps{thread: factory.thread(), pipe: c.operatorRead,
		check: check, read: owner.reader.read, publish: owner.writer.write})
}
