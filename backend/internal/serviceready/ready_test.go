package serviceready

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

const user = "S-1-5-21-1-2-3-1000"
const service = "S-1-5-80-1-2-3-4-5"
const operator = "S-1-5-21-1-2-3-1001"
const guest = "S-1-5-21-1-2-3-1002"

func profile() bootstrapauth.Profile {
	volume := protectedkey.VolumeProfile{Root: `C:\`, Serial: 7, Filesystem: "NTFS"}
	nodes := func(path string) []protectedkey.NodePolicy {
		return []protectedkey.NodePolicy{{Path: `C:\`, OwnerSID: operator, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}}, {Path: path, OwnerSID: operator, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1f01ff}}}}
	}
	keys := protectedkey.Profile{Version: 1, ProcessUserSID: user, ServiceSID: service, OperatorSID: operator, GuestSID: guest, Volume: volume, Provisioning: protectedkey.ProvisioningRecord{Reference: "synthetic", ProtectedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Catalog: map[string]protectedkey.KeyProfile{"fixed": {Path: `C:\key`, Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Nodes: nodes(`C:\key`)}}}
	image := func(path string) bootstrapauth.Image {
		return bootstrapauth.Image{Path: path, SHA256: sha256.Sum256([]byte("exe")), MaxBytes: 1024, Volume: volume, Nodes: nodes(path)}
	}
	return bootstrapauth.Profile{Keys: keys, ServiceName: "SyntheticService", SupervisorImage: image(`C:\supervisor.exe`), ChildImage: image(`C:\child.exe`)}
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
func TestUnconfiguredFactoryCannotPrepareNativeReady(t *testing.T) {
	if f, err := NewNative(Config{}); f != nil || !errors.Is(err, ErrConfig) {
		t.Fatal(f, err)
	}
	var f Factory
	if p, err := f.Prepare(testContext(t)); p != nil || !errors.Is(err, ErrConfig) {
		t.Fatal(p, err)
	}
}
func config() Config {
	return Config{Profile: profile(), PrimaryToken: 73, Endpoint: `\\.\pipe\rescue-service-SyntheticService`, ServiceCommandLine: `"C:\supervisor.exe"`, CleanupTimeout: 10 * time.Millisecond, ServiceSecurity: SecurityPolicy{OwnerSID: user, Protected: true, ACL: []protectedkey.ACE{{SID: service, Mask: 0xf01ff}}}, ProcessSecurity: SecurityPolicy{OwnerSID: user, Protected: true, ACL: []protectedkey.ACE{{SID: service, Mask: 0x1fffff}}}}
}

type fakeScope struct {
	checks, closes int
	err            error
	closeErr       error
}

func (s *fakeScope) Recheck(context.Context) error { s.checks++; return s.err }
func (s *fakeScope) Close(context.Context) error   { s.closes++; return s.closeErr }

type fakeService struct {
	value      serviceState
	closes     int
	onInspect  func()
	inspectErr error
}

func (s *fakeService) inspect() (serviceState, error) {
	if s.onInspect != nil {
		s.onInspect()
	}
	return s.value, s.inspectErr
}
func (s *fakeService) close() error { s.closes++; return nil }

type fakeProcess struct {
	value          processState
	closes         int
	token          processTokenState
	tokenInspects  int
	tokenErr       error
	onTokenInspect func()
	closeErr       error
}

func (p *fakeProcess) inspect() (processState, error) { return p.value, nil }
func (p *fakeProcess) inspectToken(uintptr) (processTokenState, error) {
	p.tokenInspects++
	if p.onTokenInspect != nil {
		p.onTokenInspect()
	}
	return p.token, p.tokenErr
}
func (p *fakeProcess) close() error { p.closes++; return p.closeErr }

type fakeOperation struct {
	done, cancelled, released bool
	err                       error
	polls                     int
}

func (o *fakeOperation) poll() (bool, error) { o.polls++; return o.done, o.err }
func (o *fakeOperation) cancel() error       { o.cancelled = true; return nil }
func (o *fakeOperation) release() error {
	if !o.done {
		panic("未完了解放")
	}
	o.released = true
	return nil
}

type fakeListener struct {
	value                  listenerState
	op                     fakeOperation
	closes                 int
	inspects               int
	connectedBeforeInspect bool
}

func (l *fakeListener) inspect() (listenerState, error) { l.inspects++; return l.value, nil }
func (l *fakeListener) connect() (operation, error) {
	l.connectedBeforeInspect = l.inspects == 0
	return &l.op, nil
}
func (l *fakeListener) close() error { l.closes++; return nil }

type fakeKernel struct {
	scope                      fakeScope
	svc                        fakeService
	proc                       fakeProcess
	listener                   fakeListener
	name, descriptor           string
	thread                     uint32
	callerErr                  error
	callerChecks, prepareCalls int
	serviceErr, listenerErr    error
}

func (k *fakeKernel) prepare(context.Context, uintptr) (preparedScope, error) {
	k.prepareCalls++
	return &k.scope, nil
}
func (k *fakeKernel) threadID() uint32                       { return k.thread }
func (k *fakeKernel) currentPID() uint32                     { return 55 }
func (k *fakeKernel) openService(string) (serviceRef, error) { return &k.svc, k.serviceErr }
func (k *fakeKernel) openProcess(uint32) (processRef, error) { return &k.proc, nil }
func (k *fakeKernel) createListener(name, descriptor string) (listenerRef, error) {
	k.name = name
	k.descriptor = descriptor
	return &k.listener, k.listenerErr
}
func fixture(c Config) *fakeKernel {
	k := &fakeKernel{thread: 7}
	k.svc.value = serviceState{state: 2, pid: 999, kind: 0x10, commandLine: c.ServiceCommandLine, security: cloneSecurity(c.ServiceSecurity)}
	k.proc.value = processState{pid: 55, creation: 42, alive: true, security: cloneSecurity(c.ProcessSecurity)}
	k.listener.value = listenerState{kind: 3, flags: 5, mode: 2, maxInstances: 1, inBytes: 16384, outBytes: 16384, serverPID: 55, security: listenerSecurity(c.Profile)}
	return k
}
func TestPrepareOwnsListenerBeforeRunningAndFreezesProvisioning(t *testing.T) {
	c := config()
	k := fixture(c)
	f, err := NewNative(c)
	if err != nil {
		t.Fatal(err)
	}
	f.api = k
	c.ServiceSecurity.ACL[0].Mask = 0
	c.Profile.Keys.Catalog["fixed"] = protectedkey.KeyProfile{}
	p, err := f.Prepare(testContext(t))
	if err != nil || p == nil {
		t.Fatal(p, err)
	}
	if err = p.Recheck(testContext(t)); err != nil {
		t.Fatal("START_PENDINGのPIDを照合に使った", err)
	}
	if k.name != `\\.\pipe\rescue-service-SyntheticService` || k.descriptor != "O:"+user+"D:P(D;;0x001f01ff;;;"+guest+")(A;;0x001f01ff;;;"+service+")(A;;0x00120183;;;"+operator+")" {
		t.Fatal(k.name, k.descriptor)
	}
	if k.scope.checks == 0 || k.listener.op.polls == 0 {
		t.Fatal("kernel準備を検査しない")
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.scope.closes != 1 || k.svc.closes != 1 || k.proc.closes != 1 || k.listener.closes != 1 {
		t.Fatal("所有参照を回収しない")
	}
}
func TestConfirmRunningRequiresKernelPIDAndMismatchIrreversiblyRevokesPlane(t *testing.T) {
	c := config()
	k := fixture(c)
	f, err := NewNative(c)
	if err != nil {
		t.Fatal(err)
	}
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.svc.value.state = 4
	k.svc.value.pid = 77
	if err = p.ConfirmRunning(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("別PIDをSCM ACK扱い", err)
	}
	k.svc.value.pid = 55
	if err = p.ConfirmRunning(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("同じPlaneの受理権が復活", err)
	}
	if err = p.Recheck(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestListenerConnectFailureIrreversiblyRevokesPlane(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.listener.op.done = true
	k.listener.op.err = errors.New("connect aborted")
	if err = p.Recheck(testContext(t)); err == nil {
		t.Fatal("失敗済みconnectをlistener準備済み扱い")
	}
	k.listener.op.err = nil
	if err = p.Recheck(testContext(t)); !errors.Is(err, ErrDenied) {
		t.Fatal("同じPlaneが復活", err)
	}
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestFatalThreadFailureIsNotReducedToOrdinaryDenial(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.thread = 8
	if err = p.Recheck(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	k.thread = 7
	if err = p.Recheck(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal("Fatalを通常拒否へ縮小", err)
	}
	if err = p.ConfirmRunning(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	if err = p.Close(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal("回収後にFatalが消えた", err)
	}
}

func TestActualListenerIsCheckedBeforeConnectIsStarted(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if k.listener.connectedBeforeInspect {
		t.Fatal("actual listener検査前にconnectした")
	}
	k.listener.op.done = true
	if err = p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
func TestFatalSurvivesInvalidCloseContext(t *testing.T) {
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	p, err := f.Prepare(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	k.thread = 8
	if err = p.Recheck(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	if err = p.Close(context.Background()); !errors.Is(err, ErrFatal) || !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("不正ctxでFatalを縮小", err)
	}
	k.thread = 7
	k.listener.op.done = true
	if err = p.Close(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
}
func TestCallerGuardIsRefusalOnlyAndDoesNotPrepareOwnedEvidence(t *testing.T) {
	var zero Factory
	if err := zero.CheckServiceCaller(testContext(t)); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	c := config()
	k := fixture(c)
	f, _ := NewNative(c)
	f.api = k
	if err := f.CheckServiceCaller(testContext(t)); err != nil {
		t.Fatal(err)
	}
	k.callerErr = ErrFatal
	if err := f.CheckServiceCaller(testContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal("元callerのFatalを縮小", err)
	}
	if k.callerChecks != 2 || k.prepareCalls != 0 || k.name != "" || k.scope.checks != 0 {
		t.Fatal("拒否guardがowned proofを準備した")
	}
}

func (k *fakeKernel) checkCaller(context.Context) error { k.callerChecks++; return k.callerErr }
