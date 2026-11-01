//go:build windows

package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"golang.org/x/sys/windows"
)

const (
	supplierServiceAccess = windows.READ_CONTROL | windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS | windows.SERVICE_START | windows.SERVICE_STOP
	supplierProcessAccess = windows.READ_CONTROL | windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE
	supplierPoll          = 10 * time.Millisecond
)

type nativeSupplierKernel struct{ parent bootstrapauth.Profile }
type nativeSupplierEvidence struct {
	parent               bootstrapauth.Profile
	config               HelperSupplierConfig
	thread               uint32
	manager, service     windows.Handle
	process              windows.Handle
	query, strong        windows.Token
	image                *supplierImage
	requested            uint64
	started              supplierGeneration
	startIssued, stopped bool
}
type supplierServiceStatus struct {
	pid, state uint32
}

// acquire starts the configured supplier only from STOPPED, so the observed
// generation follows this owner's start request. A running or pending supplier
// belongs to another owner or to an unreclaimed attempt and is refused. It
// neither changes configuration nor duplicates, mints or adjusts a token.
func (k nativeSupplierKernel) acquire(ctx context.Context, c HelperSupplierConfig) (out supplierEvidence, result error) {
	p := &nativeSupplierEvidence{parent: k.parent, config: c, thread: windows.GetCurrentThreadId()}
	out = p
	defer func() {
		if recover() != nil {
			out, result = p, ErrUnconfirmed
		}
	}()
	if err := p.guard(ctx); err != nil {
		return p, err
	}
	var err error
	p.manager, err = windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil || p.manager == 0 {
		return p, ErrDenied
	}
	name, err := windows.UTF16PtrFromString(c.ServiceName)
	if err != nil {
		return p, ErrDenied
	}
	if err = p.guard(ctx); err != nil {
		return p, err
	}
	p.service, err = windows.OpenService(p.manager, name, supplierServiceAccess)
	if err != nil || p.service == 0 {
		return p, ErrDenied
	}
	before, err := p.serviceSnapshot()
	if err != nil || before.state != windows.SERVICE_STOPPED || before.pid != 0 || !p.serviceConfigured(before) {
		return p, errors.Join(ErrDenied, err)
	}
	// Ancestors and leaf stay pinned before SCM maps the image.
	p.image, err = openSupplierImage(ctx, c.Image)
	if err != nil || p.image == nil {
		return p, errors.Join(ErrDenied, err)
	}
	if err = p.guard(ctx); err != nil {
		return p, err
	}
	var now windows.Filetime
	windows.GetSystemTimeAsFileTime(&now)
	p.requested = supplierFiletime(now)
	// Once requested, a generation may exist even if SCM reports an error. Only
	// an already-running service is known not to be this owner's. Unless
	// awaitRunning pins the generation here, cleanup never stops or confirms one.
	p.startIssued = true
	if err = windows.StartService(p.service, 0, nil); err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			p.startIssued = false
		}
		return p, ErrDenied
	}
	if err = p.awaitRunning(ctx); err != nil {
		return p, err
	}
	if err = p.guard(ctx); err != nil {
		return p, err
	}
	if err = windows.OpenProcessToken(p.process, windows.TOKEN_QUERY|windows.READ_CONTROL, &p.query); err != nil || p.query == 0 {
		return p, ErrDenied
	}
	before, err = p.inspect(ctx)
	if err != nil || !supplierSnapshotValid(c, p.started, before) {
		return p, errors.Join(ErrDenied, err)
	}
	// Strong access is requested only after the query evidence, including the
	// same token object's owner/DACL, passed. It is a separate exact handle.
	if err = p.guard(ctx); err != nil {
		return p, err
	}
	err = windows.OpenProcessToken(p.process, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY, &p.strong)
	if err != nil || p.strong == 0 || p.strong == p.query {
		return p, ErrDenied
	}
	after, err := p.inspect(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		return p, errors.Join(ErrDenied, err)
	}
	return p, nil
}

// awaitRunning holds the process from the SCM-reported PID, then confirms SCM
// still reports that PID. Holding the handle prevents PID reuse afterwards.
func (p *nativeSupplierEvidence) awaitRunning(ctx context.Context) error {
	for {
		if err := p.guard(ctx); err != nil {
			return err
		}
		status, err := p.status()
		if err != nil {
			return ErrDenied
		}
		switch status.state {
		case windows.SERVICE_RUNNING:
			if status.pid == 0 {
				return ErrDenied
			}
			return p.hold(status.pid)
		case windows.SERVICE_START_PENDING:
		default:
			return ErrDenied
		}
		if !supplierWait(ctx) {
			return ErrDenied
		}
	}
}

// hold keeps an opened handle even when verification fails; the same owner
// closes it only after stop has been confirmed.
func (p *nativeSupplierEvidence) hold(pid uint32) error {
	if p.process == 0 {
		process, err := windows.OpenProcess(supplierProcessAccess, false, pid)
		if process != 0 {
			p.process = process
		}
		if err != nil || process == 0 {
			return ErrDenied
		}
	}
	held, err := windows.GetProcessId(p.process)
	if err != nil || held != pid {
		return ErrDenied
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(p.process, &creation, &exit, &kernel, &user) != nil {
		return ErrDenied
	}
	created := supplierFiletime(creation)
	status, err := p.status()
	if err != nil || created == 0 || created < p.requested || status.pid != pid || status.state != windows.SERVICE_RUNNING {
		return ErrDenied
	}
	p.started = supplierGeneration{pid: pid, creation: created}
	return nil
}

func (p *nativeSupplierEvidence) guard(ctx context.Context) error {
	return supplierOwnerGuard(ctx, p.thread, p.parent)
}

// supplierOwnerGuard requires the owner's fixed thread without a thread token,
// inside the SYSTEM supervisor holding its Record's enabled service SID.
func supplierOwnerGuard(ctx context.Context, owner uint32, parent bootstrapauth.Profile) error {
	if !finite(ctx) {
		return ErrDenied
	}
	if windows.GetCurrentThreadId() != owner {
		return ErrFatal
	}
	thread := &nativeThread{}
	if thread.clean() != nil {
		return ErrFatal
	}
	actual, err := launchPrimaryProjection(windows.GetCurrentProcessToken())
	if err != nil || actual.user != "S-1-5-18" {
		return ErrFatal
	}
	if _, err = launchServiceGroup(windows.GetCurrentProcessToken(), parent.Keys.ServiceSID); err != nil {
		return ErrFatal
	}
	return nil
}

const supplierRegistrationAccess = windows.DELETE | windows.READ_CONTROL | windows.WRITE_DAC | windows.WRITE_OWNER |
	windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS

type nativeSupplierSCM struct{ parent bootstrapauth.Profile }

func nativeSupplierRegistrar(parent bootstrapauth.Profile) supplierRegistrar {
	return nativeSupplierSCM{parent: parent}
}

// nativeSupplierRegistration holds only a Service this owner created. It never
// starts, stops or reconfigures a generation; Batches own those.
type nativeSupplierRegistration struct {
	parent           bootstrapauth.Profile
	config           HelperSupplierConfig
	thread           uint32
	manager, service windows.Handle
	deleted, absent  bool
}

// register creates the stopped demand-start LocalService supplier with an
// unrestricted service SID and the Record's exact service security, then
// re-reads every value. An existing Service of the same name, including one
// marked for deletion, is refused and never held. No privilege, failure
// action, dependency or password is set.
func (k nativeSupplierSCM) register(ctx context.Context, c HelperSupplierConfig) (out supplierRegistered, result error) {
	r := &nativeSupplierRegistration{parent: k.parent, config: c, thread: windows.GetCurrentThreadId()}
	out = r
	defer func() {
		if recover() != nil {
			out, result = r, errors.Join(ErrFatal, ErrUnconfirmed)
		}
	}()
	if err := supplierOwnerGuard(ctx, r.thread, r.parent); err != nil {
		return r, err
	}
	var err error
	r.manager, err = windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_CREATE_SERVICE)
	if err != nil || r.manager == 0 {
		return r, ErrDenied
	}
	name, err := windows.UTF16PtrFromString(c.ServiceName)
	if err != nil {
		return r, ErrDenied
	}
	command, err := windows.UTF16PtrFromString(c.ServiceCommandLine)
	if err != nil {
		return r, ErrDenied
	}
	account, err := windows.UTF16PtrFromString(`NT AUTHORITY\LocalService`)
	if err != nil {
		return r, ErrDenied
	}
	if err = supplierOwnerGuard(ctx, r.thread, r.parent); err != nil {
		return r, err
	}
	service, err := windows.CreateService(r.manager, name, name, supplierRegistrationAccess, windows.SERVICE_WIN32_OWN_PROCESS,
		windows.SERVICE_DEMAND_START, windows.SERVICE_ERROR_NORMAL, command, nil, nil, nil, account, nil)
	if service != 0 {
		r.service = service
	}
	if err != nil || service == 0 {
		return r, ErrDenied
	}
	sidType := uint32(supplierSIDUnrestricted)
	if windows.ChangeServiceConfig2(r.service, windows.SERVICE_CONFIG_SERVICE_SID_INFO, (*byte)(unsafe.Pointer(&sidType))) != nil {
		return r, ErrDenied
	}
	if err = supplierSetServiceSecurity(r.service, c.ServiceSecurity); err != nil {
		return r, err
	}
	if err = supplierOwnerGuard(ctx, r.thread, r.parent); err != nil {
		return r, err
	}
	view := &nativeSupplierEvidence{config: c, service: r.service}
	v, err := view.serviceSnapshot()
	if err != nil || v.state != windows.SERVICE_STOPPED || v.pid != 0 || !view.serviceConfigured(v) {
		return r, errors.Join(ErrDenied, err)
	}
	return r, nil
}

// remove issues deletion only for a STOPPED registration without a process; a
// running generation is never stopped or adopted here. Deletion is confirmed
// only after this owner's handle is closed and the name no longer resolves.
func (r *nativeSupplierRegistration) remove(ctx context.Context) error {
	if err := supplierOwnerGuard(ctx, r.thread, r.parent); err != nil {
		return errors.Join(ErrUnconfirmed, err)
	}
	if r.absent || r.service == 0 && !r.deleted {
		r.absent = true
		return nil
	}
	if !r.deleted {
		status, err := (&nativeSupplierEvidence{service: r.service}).status()
		if err != nil || status.state != windows.SERVICE_STOPPED || status.pid != 0 {
			return ErrUnconfirmed
		}
		if err = windows.DeleteService(r.service); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
			return ErrUnconfirmed
		}
		r.deleted = true
	}
	if r.service != 0 {
		if windows.CloseServiceHandle(r.service) != nil {
			return ErrUnconfirmed
		}
		r.service = 0
	}
	name, err := windows.UTF16PtrFromString(r.config.ServiceName)
	if err != nil {
		return ErrUnconfirmed
	}
	for {
		if err = supplierOwnerGuard(ctx, r.thread, r.parent); err != nil {
			return errors.Join(ErrUnconfirmed, err)
		}
		other, err := windows.OpenService(r.manager, name, windows.SERVICE_QUERY_STATUS)
		if other != 0 {
			// A still-pending deletion or a later Service of the same name; never held.
			_ = windows.CloseServiceHandle(other)
		}
		if other == 0 && errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			r.absent = true
			return nil
		}
		if !supplierWait(ctx) {
			return ErrUnconfirmed
		}
	}
}

func (r *nativeSupplierRegistration) close(ctx context.Context) error {
	if !finite(ctx) || windows.GetCurrentThreadId() != r.thread || r.service != 0 && !r.absent {
		return ErrUnconfirmed
	}
	for _, h := range []*windows.Handle{&r.service, &r.manager} {
		if *h != 0 {
			if windows.CloseServiceHandle(*h) != nil {
				return ErrUnconfirmed
			}
			*h = 0
		}
	}
	return nil
}

func (p *nativeSupplierEvidence) serviceConfigured(v supplierSnapshot) bool {
	return v.serviceType == supplierOwnProcess && v.startType == supplierDemandStart && v.sidType == supplierSIDUnrestricted && v.failureActions == 0 &&
		v.serviceAccount == `NT AUTHORITY\LocalService` && v.commandLine == p.config.ServiceCommandLine &&
		supplierServiceProtected(supplierWithSecurity(p.config, v)) && reflect.DeepEqual(v.serviceSecurity, p.config.ServiceSecurity)
}

func (p *nativeSupplierEvidence) status() (supplierServiceStatus, error) {
	var status windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if p.service == 0 || windows.QueryServiceStatusEx(p.service, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed) != nil {
		return supplierServiceStatus{}, ErrDenied
	}
	return supplierServiceStatus{pid: status.ProcessId, state: status.CurrentState}, nil
}

func (p *nativeSupplierEvidence) serviceSnapshot() (supplierSnapshot, error) {
	var storage [1024]uint64
	var pins runtime.Pinner
	pins.Pin(&storage[0])
	defer pins.Unpin()
	c := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&storage[0]))
	var needed uint32
	if windows.QueryServiceConfig(p.service, c, uint32(unsafe.Sizeof(storage)), &needed) != nil {
		return supplierSnapshot{}, ErrDenied
	}
	command, err := supplierBoundedString(c.BinaryPathName, unsafe.Pointer(&storage[0]), unsafe.Sizeof(storage), unsafe.Sizeof(*c))
	if err != nil {
		return supplierSnapshot{}, err
	}
	account, err := supplierBoundedString(c.ServiceStartName, unsafe.Pointer(&storage[0]), unsafe.Sizeof(storage), unsafe.Sizeof(*c))
	if err != nil {
		return supplierSnapshot{}, err
	}
	var status windows.SERVICE_STATUS_PROCESS
	if windows.QueryServiceStatusEx(p.service, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed) != nil || status.ServiceType != c.ServiceType {
		return supplierSnapshot{}, ErrDenied
	}
	var sidType uint32
	if windows.QueryServiceConfig2(p.service, windows.SERVICE_CONFIG_SERVICE_SID_INFO, (*byte)(unsafe.Pointer(&sidType)), 4, &needed) != nil {
		return supplierSnapshot{}, ErrDenied
	}
	// SCM recovery could start a generation that no owner requested.
	var failure [256]uint64
	pins.Pin(&failure[0])
	if windows.QueryServiceConfig2(p.service, windows.SERVICE_CONFIG_FAILURE_ACTIONS, (*byte)(unsafe.Pointer(&failure[0])), uint32(unsafe.Sizeof(failure)), &needed) != nil {
		return supplierSnapshot{}, ErrDenied
	}
	actions := (*windows.SERVICE_FAILURE_ACTIONS)(unsafe.Pointer(&failure[0])).ActionsCount
	security, err := supplierObjectSecurity(p.service, windows.SE_SERVICE)
	if err != nil {
		return supplierSnapshot{}, err
	}
	return supplierSnapshot{pid: status.ProcessId, serviceType: c.ServiceType, startType: c.StartType, state: status.CurrentState, sidType: sidType, failureActions: actions, serviceAccount: account, commandLine: command, serviceSecurity: security}, nil
}

func (p *nativeSupplierEvidence) generation() supplierGeneration { return p.started }

func (p *nativeSupplierEvidence) inspect(ctx context.Context) (supplierSnapshot, error) {
	if err := p.guard(ctx); err != nil {
		return supplierSnapshot{}, err
	}
	v, err := p.serviceSnapshot()
	if err != nil {
		return supplierSnapshot{}, err
	}
	if p.process == 0 || launchHandleAccess(uintptr(p.process), false, supplierProcessAccess) != nil {
		return supplierSnapshot{}, ErrDenied
	}
	pid, err := windows.GetProcessId(p.process)
	if err != nil || pid != v.pid {
		return supplierSnapshot{}, ErrDenied
	}
	alive, err := windows.WaitForSingleObject(p.process, 0)
	if err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return supplierSnapshot{}, ErrDenied
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(p.process, &creation, &exit, &kernel, &user) != nil {
		return supplierSnapshot{}, ErrDenied
	}
	v.creation = supplierFiletime(creation)
	var path [32768]uint16
	n := uint32(len(path))
	if windows.QueryFullProcessImageName(p.process, 0, &path[0], &n) != nil || n == 0 || n >= uint32(len(path)) {
		return supplierSnapshot{}, ErrDenied
	}
	v.imagePath = windows.UTF16ToString(path[:n])
	v.processSecurity, err = supplierObjectSecurity(p.process, windows.SE_KERNEL_OBJECT)
	if err != nil {
		return supplierSnapshot{}, err
	}
	if launchHandleAccess(uintptr(p.query), false, windows.TOKEN_QUERY|windows.READ_CONTROL) != nil {
		return supplierSnapshot{}, ErrDenied
	}
	before, err := launchPrimaryProjection(p.query)
	if err != nil {
		return supplierSnapshot{}, err
	}
	v.primary, v.tokenID, v.modifiedID = before.tokenProjection, before.tokenID, before.modifiedID
	v.group, err = launchServiceGroup(p.query, p.config.ServiceSID)
	if err != nil {
		return supplierSnapshot{}, err
	}
	// The held token object's owner/DACL is inspected separately from its
	// default DACL; neither substitutes for the other.
	v.tokenSecurity, err = supplierObjectSecurity(windows.Handle(p.query), windows.SE_KERNEL_OBJECT)
	if err != nil {
		return supplierSnapshot{}, err
	}
	if err = supplierDefaultACL(p.query, p.config); err != nil {
		return supplierSnapshot{}, err
	}
	if p.strong != 0 {
		if launchHandleAccess(uintptr(p.strong), false, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY) != nil {
			return supplierSnapshot{}, ErrDenied
		}
		strong, e := launchPrimaryProjection(p.strong)
		if e != nil || strong != before {
			return supplierSnapshot{}, ErrDenied
		}
	}
	after, err := launchPrimaryProjection(p.query)
	if err != nil || after != before {
		return supplierSnapshot{}, ErrDenied
	}
	if p.image == nil || p.image.check(ctx) != nil {
		return supplierSnapshot{}, ErrDenied
	}
	alive, err = windows.WaitForSingleObject(p.process, 0)
	if err != nil || alive != uint32(windows.WAIT_TIMEOUT) {
		return supplierSnapshot{}, ErrDenied
	}
	if err = p.guard(ctx); err != nil {
		return supplierSnapshot{}, err
	}
	return v, nil
}

func (p *nativeSupplierEvidence) token() uintptr { return uintptr(p.strong) }

// release ends this owner's borrowing. It does not prove that the child or any
// token duplicated into it has ended; the Batch confirms that first.
func (p *nativeSupplierEvidence) release(ctx context.Context) error {
	if !finite(ctx) || windows.GetCurrentThreadId() != p.thread {
		return ErrUnconfirmed
	}
	if p.strong != 0 {
		if p.strong.Close() != nil {
			return ErrUnconfirmed
		}
		p.strong = 0
	}
	return nil
}

// stop requests SCM stop only for the generation pinned during acquisition and
// waits for that held process to exit and SCM to report STOPPED. Without a
// pinned generation, or with a different running one, no stop is issued and
// cleanup stays unconfirmed (supplierStopDecision); a later PID is not adopted.
func (p *nativeSupplierEvidence) stop(ctx context.Context) error {
	if !finite(ctx) || windows.GetCurrentThreadId() != p.thread {
		return ErrUnconfirmed
	}
	if p.stopped || !p.startIssued {
		p.stopped = true
		return nil
	}
	for {
		status, err := p.status()
		if err != nil {
			return ErrUnconfirmed
		}
		exited := false
		if p.started.pid != 0 && p.process != 0 {
			event, e := windows.WaitForSingleObject(p.process, 0)
			if e != nil {
				return ErrUnconfirmed
			}
			exited = event == uint32(windows.WAIT_OBJECT_0)
		}
		// A stop request is not a stop. Only the held exit and SCM STOPPED count.
		switch supplierStopDecision(p.started, status.state, status.pid, exited) {
		case supplierStopConfirmed:
			p.stopped = true
			return nil
		case supplierStopUnowned:
			return ErrUnconfirmed
		case supplierStopRequest:
			var reply windows.SERVICE_STATUS
			// A refused or lost request is retried; its result proves nothing.
			_ = windows.ControlService(p.service, windows.SERVICE_CONTROL_STOP, &reply)
		}
		if !supplierWait(ctx) {
			return ErrUnconfirmed
		}
	}
}

// close returns the remaining references only after stop was confirmed.
func (p *nativeSupplierEvidence) close(ctx context.Context) error {
	if !finite(ctx) || windows.GetCurrentThreadId() != p.thread || p.startIssued && !p.stopped || p.strong != 0 {
		return ErrUnconfirmed
	}
	if p.query != 0 {
		if p.query.Close() != nil {
			return ErrUnconfirmed
		}
		p.query = 0
	}
	if p.process != 0 {
		if windows.CloseHandle(p.process) != nil {
			return ErrUnconfirmed
		}
		p.process = 0
	}
	if p.image != nil {
		if err := p.image.close(ctx); err != nil {
			return err
		}
	}
	for _, h := range []*windows.Handle{&p.service, &p.manager} {
		if *h != 0 {
			if !finite(ctx) || windows.CloseServiceHandle(*h) != nil {
				return ErrUnconfirmed
			}
			*h = 0
		}
	}
	return nil
}

func supplierWait(ctx context.Context) bool {
	timer := time.NewTimer(supplierPoll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return finite(ctx)
	}
}

func supplierFiletime(v windows.Filetime) uint64 {
	return uint64(v.HighDateTime)<<32 | uint64(v.LowDateTime)
}

func supplierBoundedString(ptr *uint16, base unsafe.Pointer, size, min uintptr) (string, error) {
	start, origin := uintptr(unsafe.Pointer(ptr)), uintptr(base)
	if ptr == nil || start < origin+min || start >= origin+size || (start-origin)%2 != 0 {
		return "", ErrDenied
	}
	units := unsafe.Slice(ptr, int((origin+size-start)/2))
	end := -1
	for i, v := range units {
		if v == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return "", ErrDenied
	}
	units = units[:end]
	for i := 0; i < len(units); i++ {
		v := units[i]
		if v >= 0xd800 && v <= 0xdbff {
			if i+1 == len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", ErrDenied
			}
			i++
		} else if v >= 0xdc00 && v <= 0xdfff {
			return "", ErrDenied
		}
	}
	return string(utf16.Decode(units)), nil
}

var _ supplierKernel = nativeSupplierKernel{}
var _ supplierEvidence = (*nativeSupplierEvidence)(nil)
