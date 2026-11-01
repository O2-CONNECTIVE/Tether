package operatorauth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// nativeLaunchSpec comes only from the retained Record via supplierBatchSpec.
// The leaf hash is not proof of ancestor ACLs, service provenance or guest
// separation; the unset compiled helper value keeps RunFixed closed.
type nativeLaunchSpec struct {
	imagePath                                            string
	imageSHA256                                          [32]byte
	launchID                                             string
	maxImageBytes                                        int64
	primaryToken, operatorRead, reportWrite, profileRead uintptr
	cleanup                                              time.Duration
	processSDDL, threadSDDL, helperUserSID               string
	helperServiceSID                                     string
}
type helperBinding struct {
	pid                                              uint32
	creation, tokenID                                uint64
	childModifiedID, sourceTokenID, sourceModifiedID uint64
	user, imagePath                                  string
	primary                                          tokenProjection
	sourceServiceGroup, childServiceGroup            helperServiceGroup
}
type helperServiceGroup struct {
	sid        string
	attributes uint32
}

func (g helperServiceGroup) matches(sid string) bool {
	if sid == "" {
		return g == (helperServiceGroup{})
	}
	// Service SIDs may have mandatory, enabled-by-default, enabled and OWNER.
	// OWNER is a group attribute, not evidence of any kernel object's owner.
	return g.sid == sid && g.attributes&4 != 0 && g.attributes & ^uint32(15) == 0
}

type helperLaunchKernel interface {
	validate(context.Context, nativeLaunchSpec) error
	pinImage(context.Context, nativeLaunchSpec) (uintptr, error)
	newJob() (uintptr, error)
	createSuspended(nativeLaunchSpec) (uintptr, uintptr, error)
	inspect(context.Context, uintptr, uintptr, nativeLaunchSpec) (helperBinding, error)
	assign(uintptr, uintptr) error
	resume(uintptr) error
	terminateJob(uintptr) error
	terminateProcess(uintptr) error
	waitProcess(context.Context, uintptr) (uint32, error)
	waitEmpty(context.Context, uintptr) error
	close(uintptr) error
}

// helperChild owns the original process/thread/image/job references. The token
// and three inherited pipe references remain borrowed from the outer owner.
type helperChild struct {
	mu                          sync.RWMutex
	kernel                      helperLaunchKernel
	spec                        nativeLaunchSpec
	process, thread, image, job uintptr
	binding                     helperBinding
	resumed                     bool
}

func helperHandle(h uintptr) bool { return h != 0 && h < ^uintptr(0)-15 }
func localHelperImage(path string) bool {
	if len(path) < 8 || len(path) > 32700 || path[0] < 'A' || path[0] > 'Z' || path[1:3] != `:\` || !strings.HasSuffix(strings.ToLower(path), ".exe") {
		return false
	}
	for _, part := range strings.Split(path[3:], `\`) {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, "/:\x00\"<>|?*") {
			return false
		}
	}
	return true
}
func (s nativeLaunchSpec) helperIdentityValid() bool {
	if s.helperUserSID == "S-1-5-19" {
		// A service SID has authority 5, subauthority 80 and five hash words.
		// S-1-5-80-0 (ALL SERVICES) is not an isolated service identity.
		return reportSIDValid(s.helperServiceSID) && strings.HasPrefix(s.helperServiceSID, "S-1-5-80-") && len(strings.Split(s.helperServiceSID, "-")) == 9
	}
	return reportSIDValid(s.helperUserSID) && s.helperUserSID != "S-1-5-18" && s.helperUserSID != "S-1-5-20" && s.helperServiceSID == ""
}
func (s nativeLaunchSpec) helperReportSID() string {
	if s.helperServiceSID != "" {
		return s.helperServiceSID
	}
	return s.helperUserSID
}
func (s nativeLaunchSpec) valid() bool {
	return localHelperImage(s.imagePath) && s.imageSHA256 != ([32]byte{}) && profileLaunchID(s.launchID) && s.maxImageBytes > 0 && s.maxImageBytes <= 1<<30 && helperHandle(s.primaryToken) && helperHandle(s.operatorRead) && helperHandle(s.reportWrite) && helperHandle(s.profileRead) && s.primaryToken != s.operatorRead && s.primaryToken != s.reportWrite && s.primaryToken != s.profileRead && s.operatorRead != s.reportWrite && s.operatorRead != s.profileRead && s.reportWrite != s.profileRead && s.cleanup > 0 && s.cleanup <= time.Minute && s.processSDDL != "" && s.threadSDDL != "" && s.helperIdentityValid()
}

// startHelper returns a suspended, assigned child. The outer owner must release
// its report writer and unwanted read copies before calling resume.
func startHelper(ctx context.Context, s nativeLaunchSpec, k helperLaunchKernel) (child *helperChild, result error) {
	if !finite(ctx) || !s.valid() || k == nil {
		return nil, ErrDenied
	}
	c := &helperChild{kernel: k, spec: s}
	// 取得後のpanicでも元の参照を外側ownerへ返す。回収済みとは扱わない。
	defer func() {
		if recover() != nil {
			child = c
			result = ErrUnconfirmed
		}
	}()
	fail := func(err error) (*helperChild, error) {
		cleanup, cancel := context.WithTimeout(context.Background(), s.cleanup)
		defer cancel()
		stop := c.terminate()
		closed := c.close(cleanup)
		if stop != nil || closed != nil {
			return c, errors.Join(err, ErrUnconfirmed, stop, closed)
		}
		return nil, err
	}
	if err := k.validate(ctx, s); err != nil || !finite(ctx) {
		return nil, ErrDenied
	}
	var err error
	if c.image, err = k.pinImage(ctx, s); err != nil || !helperHandle(c.image) || !finite(ctx) {
		return fail(ErrDenied)
	}
	if c.job, err = k.newJob(); err != nil || !helperHandle(c.job) {
		return fail(ErrDenied)
	}
	if !finite(ctx) {
		return fail(ErrDenied)
	}
	if c.process, c.thread, err = k.createSuspended(s); err != nil || !helperHandle(c.process) || !helperHandle(c.thread) {
		return fail(ErrDenied)
	}
	if c.binding, err = k.inspect(ctx, c.process, c.image, s); err != nil || !c.binding.valid(s) || !finite(ctx) {
		return fail(ErrDenied)
	}
	if k.assign(c.job, c.process) != nil || !finite(ctx) {
		return fail(ErrDenied)
	}
	return c, nil
}
func (b helperBinding) valid(s nativeLaunchSpec) bool {
	return b.pid != 0 && b.creation != 0 && b.tokenID != 0 && b.childModifiedID != 0 && b.sourceTokenID != 0 && b.sourceModifiedID != 0 && b.user == s.helperUserSID && b.imagePath == s.imagePath && b.sourceServiceGroup.matches(s.helperServiceSID) && b.childServiceGroup == b.sourceServiceGroup
}

// Go lock contention is bounded separately from the native operation itself.
// Native API nonreturn is not made cancellable by this lock admission loop.
func (c *helperChild) take(ctx context.Context, write bool) bool {
	for finite(ctx) {
		acquired := false
		if write {
			acquired = c.mu.TryLock()
		} else {
			acquired = c.mu.TryRLock()
		}
		if acquired {
			if finite(ctx) {
				return true
			}
			if write {
				c.mu.Unlock()
			} else {
				c.mu.RUnlock()
			}
			return false
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}
func (c *helperChild) resume(ctx context.Context) error {
	if !c.take(ctx, true) {
		return ErrUnconfirmed
	}
	defer c.mu.Unlock()
	if c.resumed || !finite(ctx) || !helperHandle(c.thread) || !helperHandle(c.image) {
		return ErrDenied
	}
	if err := c.recheckLocked(ctx); err != nil {
		return err
	}
	if c.kernel.resume(c.thread) != nil {
		return ErrUnconfirmed
	}
	c.resumed = true
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	return nil
}
func (c *helperChild) recheckLocked(ctx context.Context) error {
	if !finite(ctx) || !helperHandle(c.process) || !helperHandle(c.image) {
		return ErrDenied
	}
	b, err := c.kernel.inspect(ctx, c.process, c.image, c.spec)
	if err != nil || b != c.binding || !finite(ctx) {
		return ErrDenied
	}
	return nil
}

// Binding identity is independent of liveness; normal exit can retain binding.
func (c *helperChild) recheck(ctx context.Context) error {
	if !c.take(ctx, true) {
		return ErrUnconfirmed
	}
	defer c.mu.Unlock()
	return c.recheckLocked(ctx)
}
func (c *helperChild) waitExit(ctx context.Context) (uint32, error) {
	if !c.take(ctx, false) {
		return 0, ErrUnconfirmed
	}
	defer c.mu.RUnlock()
	if !finite(ctx) || !helperHandle(c.process) {
		return 0, ErrUnconfirmed
	}
	exit, err := c.kernel.waitProcess(ctx, c.process)
	if err != nil || !finite(ctx) {
		return 0, ErrUnconfirmed
	}
	return exit, nil
}
func (c *helperChild) terminate() error {
	if !c.mu.TryRLock() {
		return ErrUnconfirmed
	}
	defer c.mu.RUnlock()
	var errs []error
	if helperHandle(c.job) {
		if err := c.kernel.terminateJob(c.job); err != nil {
			errs = append(errs, err)
		}
	}
	if helperHandle(c.process) {
		if err := c.kernel.terminateProcess(c.process); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// close never treats Terminate success as exit. On incomplete wait it retains
// every handle so the same owner can retry bounded reclamation.
func (c *helperChild) close(ctx context.Context) error {
	if !c.take(ctx, true) {
		return ErrUnconfirmed
	}
	defer c.mu.Unlock()
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	if helperHandle(c.process) {
		if _, err := c.kernel.waitProcess(ctx, c.process); err != nil || !finite(ctx) {
			return ErrUnconfirmed
		}
	}
	if helperHandle(c.job) {
		if err := c.kernel.waitEmpty(ctx, c.job); err != nil || !finite(ctx) {
			return ErrUnconfirmed
		}
	}
	var errs []error
	for _, h := range []*uintptr{&c.thread, &c.process, &c.image, &c.job} {
		if *h != 0 {
			if !finite(ctx) {
				return errors.Join(ErrUnconfirmed, errors.Join(errs...))
			}
			if err := c.kernel.close(*h); err != nil {
				errs = append(errs, ErrUnconfirmed)
			} else {
				*h = 0
			}
		}
	}
	return errors.Join(errs...)
}
