package operatorauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type launchWitnessFixture struct {
	observation                 helperLaunchObservation
	fixed                       helperLaunchFixed
	locators                    helperLaunchLocators
	acquired, inspected, closed int
	acquireFn                   func() error
	inspectFn                   func() error
	closeFn                     func() error
}

func (f *launchWitnessFixture) acquire(_ context.Context, fixed helperLaunchFixed, loc helperLaunchLocators) error {
	f.acquired++
	f.fixed, f.locators = fixed, loc
	if f.acquireFn != nil {
		return f.acquireFn()
	}
	return nil
}
func (f *launchWitnessFixture) inspect(context.Context) (helperLaunchObservation, error) {
	f.inspected++
	if f.inspectFn != nil {
		if err := f.inspectFn(); err != nil {
			return helperLaunchObservation{}, err
		}
	}
	return f.observation, nil
}
func (f *launchWitnessFixture) close(context.Context) error {
	f.closed++
	if f.closeFn != nil {
		return f.closeFn()
	}
	return nil
}

const launchServiceSID = "S-1-5-80-1-2-3-4-5"
const launchParentSID = "S-1-5-80-11-12-13-14-15"

func launchFixed() helperLaunchFixed {
	return helperLaunchFixed{parentService: "RescueHostSupervisor", supplierService: "RescueAuthSupplier",
		parentImage: `C:\supervisor.exe`, helperImage: `C:\helper.exe`, helperServiceSID: launchServiceSID, parentServiceSID: launchParentSID,
		runBudget: time.Second, cleanup: 100 * time.Millisecond}
}

func launchObservation() helperLaunchObservation {
	primary := tokenProjection{user: "S-1-5-19", authentication: 65, kind: 1, integrity: 8192}
	group := helperServiceGroup{launchServiceSID, 4}
	// The last ACE stands for a guest-like principal that may only query.
	acl := []helperLaunchACE{{mask: 0xf01ff, sid: "S-1-5-18"}, {mask: 0xf01ff, sid: launchServiceSID}, {mask: 0x20008, sid: "S-1-5-21-1-2-3-501"}}
	// The helper SID may only query the SYSTEM supervisor's token.
	parentACL := []helperLaunchACE{{mask: 0xf01ff, sid: "S-1-5-18"}, {mask: 0xf01ff, sid: launchParentSID}, {mask: 0x20008, sid: launchServiceSID}}
	parentToken := helperLaunchToken{tokenID: 43, modifiedID: 44, primary: tokenProjection{user: "S-1-5-18", authentication: 999, kind: 1, integrity: 16384},
		serviceGroup: helperServiceGroup{launchParentSID, 4}, owner: "S-1-5-18", acl: parentACL}
	return helperLaunchObservation{
		parentToken:     parentToken,
		parentService:   helperLaunchService{41, 4},
		supplierService: helperLaunchService{61, 4},
		inheritedFrom:   41,
		parent:          helperLaunchProcess{pid: 41, creation: 42, imagePath: `C:\supervisor.exe`, alive: true},
		supplier:        helperLaunchProcess{pid: 61, creation: 43, imagePath: `C:\helper.exe`, alive: true},
		self:            helperLaunchProcess{pid: 51, creation: 52, imagePath: `C:\helper.exe`, alive: true},
		supplierToken:   helperLaunchToken{tokenID: 63, modifiedID: 64, primary: primary, serviceGroup: group, owner: "S-1-5-18", acl: acl},
		selfToken:       helperLaunchToken{tokenID: 53, modifiedID: 54, primary: primary, serviceGroup: group},
		pipes:           [3]helperLaunchPipe{{9, 101, 0x120089, 0, 1, 5, 41}, {10, 102, 0x120102, 0, 1, 0, 41}, {77, 103, 0x120089, 0, 2, 1, 41}},
	}
}

// Endpoint/profile I/O stays the existing memory fixture; launch evidence and
// the binding handed to the profile receiver come only from the new source.
func launchSourceFixture(t *testing.T) (*launchWitnessFixture, helperEntryFactory) {
	t.Helper()
	entry, factory, _, _, _ := entryFixture(t)
	entry.lease.checked = 1
	profileFactory := factory.profile
	factory.profile = func(ctx context.Context, h uintptr, binding profileReceiverBinding, source profileReceiverSource, budget time.Duration) (*profileReceiver, error) {
		receiver, err := profileFactory(ctx, h, binding, source, budget)
		if receiver != nil {
			receiver.binding, receiver.source = binding, source
		}
		return receiver, err
	}
	return &launchWitnessFixture{observation: launchObservation()}, factory
}

func launchSource(t *testing.T, w helperLaunchWitness, args []string) *helperLaunchSource {
	t.Helper()
	source, err := newHelperLaunchSource(launchFixed(), args, w)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestHelperLaunchSourceRunsOneBoundEntryAndReclaimsEvidenceLast(t *testing.T) {
	witness, factory := launchSourceFixture(t)
	var bound profileReceiverBinding
	profileFactory := factory.profile
	factory.profile = func(ctx context.Context, h uintptr, b profileReceiverBinding, s profileReceiverSource, budget time.Duration) (*profileReceiver, error) {
		bound = b
		return profileFactory(ctx, h, b, s, budget)
	}
	if err := runFixedHelper(requestContext(t), entryArgs(), launchSource(t, witness, entryArgs()), factory); err != nil {
		t.Fatal(err)
	}
	if witness.acquired != 1 || witness.inspected < 4 || witness.closed != 1 {
		t.Fatalf("acquire=%d inspect=%d close=%d", witness.acquired, witness.inspected, witness.closed)
	}
	want := helperLaunchLocators{operatorRead: 9, reportWrite: 10, profileRead: 77, launchID: strings.Repeat("a", 64)}
	if witness.fixed != launchFixed() || witness.locators != want {
		t.Fatal("witness was not steered by fixed configuration and locators", witness.fixed, witness.locators)
	}
	c := bound.child
	if c.pid != 51 || c.creation != 52 || c.tokenID != 53 || c.childModifiedID != 54 || c.sourceTokenID != 63 || c.sourceModifiedID != 64 ||
		c.imagePath != `C:\helper.exe` || !c.childServiceGroup.matches(launchServiceSID) || c.sourceServiceGroup != c.childServiceGroup {
		t.Fatalf("receiver binding not derived from native observation: %+v", c)
	}
}

func TestHelperLaunchSourceUnconfiguredFixedConfigurationRefuses(t *testing.T) {
	for name, mutate := range map[string]func(*helperLaunchFixed){
		"zero":              func(f *helperLaunchFixed) { *f = helperLaunchFixed{} },
		"same service":      func(f *helperLaunchFixed) { f.supplierService = strings.ToLower(f.parentService) },
		"same image":        func(f *helperLaunchFixed) { f.parentImage = f.helperImage },
		"relative image":    func(f *helperLaunchFixed) { f.helperImage = `helper.exe` },
		"all services SID":  func(f *helperLaunchFixed) { f.helperServiceSID = "S-1-5-80-0" },
		"shared user SID":   func(f *helperLaunchFixed) { f.helperServiceSID = "S-1-5-19" },
		"unbounded budget":  func(f *helperLaunchFixed) { f.runBudget = 2 * time.Minute },
		"no cleanup budget": func(f *helperLaunchFixed) { f.cleanup = 0 },
		"no parent SID":     func(f *helperLaunchFixed) { f.parentServiceSID = "" },
		"parent is helper":  func(f *helperLaunchFixed) { f.parentServiceSID = f.helperServiceSID },
	} {
		t.Run(name, func(t *testing.T) {
			f := launchFixed()
			mutate(&f)
			if _, err := newHelperLaunchSource(f, entryArgs(), &launchWitnessFixture{}); !errors.Is(err, ErrUnconfigured) {
				t.Fatal(err)
			}
			if _, err := newHelperLaunchSource(f, entryArgs(), nativeHelperLaunchWitness()); !errors.Is(err, ErrUnconfigured) {
				t.Fatal("native source accepted invalid fixed configuration", err)
			}
		})
	}
	if _, err := newHelperLaunchSource(launchFixed(), entryArgs(), nil); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
}

func TestHelperLaunchSourceExpiredCheckDoesNotRevive(t *testing.T) {
	witness, _ := launchSourceFixture(t)
	lease, err := launchSource(t, witness, entryArgs()).prepare(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(requestContext(t))
	cancel()
	if err = lease.check(cancelled, lease.config()); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = lease.check(requestContext(t), lease.config()); !errors.Is(err, ErrDenied) {
		t.Fatal("cancelled check revived", err)
	}
	if err = lease.close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestHelperLaunchSourceUnknownCloseIsNotRetried(t *testing.T) {
	witness, _ := launchSourceFixture(t)
	lease, err := launchSource(t, witness, entryArgs()).prepare(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	witness.closeFn = func() error { panic("unknown close outcome") }
	if err = lease.close(requestContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	witness.closeFn = nil
	if err = lease.close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) || witness.closed != 1 {
		t.Fatal("unknown close was repeated", err, witness.closed)
	}
}

func TestHelperLaunchSourceRejectsForeignOrChangedEvidenceBeforeAuthentication(t *testing.T) {
	for name, mutate := range map[string]func(*helperLaunchObservation){
		"other creator":             func(o *helperLaunchObservation) { o.inheritedFrom = 61 },
		"parent not the service":    func(o *helperLaunchObservation) { o.parentService.pid = 99 },
		"parent service stopped":    func(o *helperLaunchObservation) { o.parentService.state = 3 },
		"parent generation":         func(o *helperLaunchObservation) { o.parent.creation = o.self.creation },
		"parent exited":             func(o *helperLaunchObservation) { o.parent.alive = false },
		"parent image":              func(o *helperLaunchObservation) { o.parent.imagePath = `C:\other.exe` },
		"supplier not the service":  func(o *helperLaunchObservation) { o.supplierService.pid = 99 },
		"supplier image":            func(o *helperLaunchObservation) { o.supplier.imagePath = `C:\other.exe` },
		"supplier newer than child": func(o *helperLaunchObservation) { o.supplier.creation = 60 },
		"supplier user":             func(o *helperLaunchObservation) { o.supplierToken.primary.user = "S-1-5-20" },
		"supplier group deny-only":  func(o *helperLaunchObservation) { o.supplierToken.serviceGroup.attributes = 0x10 },
		"supplier restricted":       func(o *helperLaunchObservation) { o.supplierToken.primary.restricted = true },
		"supplier token owner":      func(o *helperLaunchObservation) { o.supplierToken.owner = "S-1-5-19" },
		"supplier token no DACL":    func(o *helperLaunchObservation) { o.supplierToken.acl = nil },
		"guest token duplicate":     func(o *helperLaunchObservation) { o.supplierToken.acl[2].mask |= 2 },
		"shared user impersonate": func(o *helperLaunchObservation) {
			o.supplierToken.acl[1].sid, o.supplierToken.acl[1].mask = "S-1-5-19", 4
		},
		"inherited ACE":              func(o *helperLaunchObservation) { o.supplierToken.acl[0].flags = 0x10 },
		"self user":                  func(o *helperLaunchObservation) { o.selfToken.primary.user = "S-1-5-21-1-2-3-501" },
		"self group missing":         func(o *helperLaunchObservation) { o.selfToken.serviceGroup = helperServiceGroup{} },
		"self primary":               func(o *helperLaunchObservation) { o.selfToken.primary.authentication++ },
		"self image":                 func(o *helperLaunchObservation) { o.self.imagePath = `C:\other.exe` },
		"pipe server substituted":    func(o *helperLaunchObservation) { o.pipes[2].server = 61 },
		"report server substituted":  func(o *helperLaunchObservation) { o.pipes[1].server = 99 },
		"pipe locator":               func(o *helperLaunchObservation) { o.pipes[0].original = 11 },
		"pipe rights":                func(o *helperLaunchObservation) { o.pipes[1].access |= 4 },
		"unknown mode":               func(o *helperLaunchObservation) { o.pipes[0].mode = 0 },
		"profile overlapped":         func(o *helperLaunchObservation) { o.pipes[2].mode = 1 },
		"operator client end":        func(o *helperLaunchObservation) { o.pipes[0].info = 4 },
		"inheritable held reference": func(o *helperLaunchObservation) { o.pipes[0].flags = 1 },
		"raw alias":                  func(o *helperLaunchObservation) { o.pipes[0].retained = 77 },
		"held alias":                 func(o *helperLaunchObservation) { o.pipes[0].retained = o.pipes[1].retained },
		"parent token missing":       func(o *helperLaunchObservation) { o.parentToken = helperLaunchToken{} },
		"parent token not SYSTEM":    func(o *helperLaunchObservation) { o.parentToken.primary.user = "S-1-5-19" },
		"parent token other service": func(o *helperLaunchObservation) { o.parentToken.serviceGroup.sid = launchServiceSID },
		"parent token null DACL":     func(o *helperLaunchObservation) { o.parentToken.acl = nil },
		"parent token unknown ACE":   func(o *helperLaunchObservation) { o.parentToken.acl[0].kind = 2 },
		"helper duplicates parent":   func(o *helperLaunchObservation) { o.parentToken.acl[2].mask |= 2 },
		"parent token user owner":    func(o *helperLaunchObservation) { o.parentToken.owner = "S-1-5-21-1-2-3-501" },
	} {
		t.Run(name, func(t *testing.T) {
			witness, factory := launchSourceFixture(t)
			mutate(&witness.observation)
			factory.message = func(uintptr, endpointRole, time.Duration) (*messageEndpoint, error) {
				t.Fatal("invalid launch adopted operator input")
				return nil, ErrDenied
			}
			if err := runFixedHelper(requestContext(t), entryArgs(), launchSource(t, witness, entryArgs()), factory); !errors.Is(err, ErrDenied) {
				t.Fatal(err)
			}
			if witness.closed != 1 {
				t.Fatal("partial evidence lost", witness.closed)
			}
		})
	}
}

func TestHelperLaunchSourceArgumentsAreOnlyLocators(t *testing.T) {
	for _, args := range [][]string{nil, {"--operator-auth-internal", "09", "10", "77", strings.Repeat("a", 64)},
		{"--operator-auth-internal", "9", "9", "77", strings.Repeat("a", 64)}, {"--operator-auth-internal", "9", "10", "77", "untrusted"}} {
		witness, factory := launchSourceFixture(t)
		if err := runFixedHelper(requestContext(t), args, launchSource(t, witness, args), factory); !errors.Is(err, ErrDenied) || witness.acquired != 0 {
			t.Fatal("malformed locator reached native acquisition", args, err)
		}
	}
	// A well-formed launch ID that the parent-created profile stream does not
	// carry is rejected before the existing Helper authenticates.
	witness, factory := launchSourceFixture(t)
	args := entryArgs()
	args[4] = strings.Repeat("b", 64)
	if err := runFixedHelper(requestContext(t), args, launchSource(t, witness, args), factory); !errors.Is(err, ErrDenied) || witness.closed != 1 {
		t.Fatal("argv launch ID was trusted over the profile stream", err, witness.closed)
	}
}

func TestHelperLaunchSourceKeepsSingleUseAfterCancelledAcquisition(t *testing.T) {
	witness, factory := launchSourceFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	witness.acquireFn = func() error { close(entered); <-release; return nil }
	source := launchSource(t, witness, entryArgs())
	ctx, cancel := context.WithCancel(requestContext(t))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runFixedHelper(ctx, entryArgs(), source, factory) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatal("acquisition return was not awaited", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if witness.acquired != 1 || witness.closed != 1 {
		t.Fatal(witness.acquired, witness.closed)
	}
	if _, err := source.prepare(requestContext(t)); !errors.Is(err, ErrDenied) || witness.acquired != 1 {
		t.Fatal("cancelled source reused", err)
	}
}

func TestHelperLaunchSourceRetainsFailedAcquisitionForCleanupRetry(t *testing.T) {
	witness, _ := launchSourceFixture(t)
	witness.acquireFn = func() error { return ErrDenied }
	lease, err := launchSource(t, witness, entryArgs()).prepare(requestContext(t))
	if lease == nil || !errors.Is(err, ErrDenied) {
		t.Fatal("partial owner lost", err)
	}
	if c := lease.config(); c.valid() || c.operatorRead != 9 || c.reportWrite != 10 || c.binding.profileRead != 77 {
		t.Fatal("unverified config usable or raw locators lost", c)
	}
	witness.closeFn = func() error { return ErrUnconfirmed }
	if err = lease.close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if err = lease.check(requestContext(t), lease.config()); !errors.Is(err, ErrDenied) {
		t.Fatal("cleanup revived proof", err)
	}
	witness.closeFn = nil
	if err = lease.close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err = lease.close(requestContext(t)); err != nil || witness.closed != 2 {
		t.Fatal("completed close repeated", err, witness.closed)
	}
}

func TestHelperLaunchSourcePinsHeldObjectReferencesAndLatchesDrift(t *testing.T) {
	for name, mutate := range map[string]func(*helperLaunchObservation){
		"replacement reference": func(o *helperLaunchObservation) { o.pipes[0].retained = 201 },
		"token DACL in place":   func(o *helperLaunchObservation) { o.supplierToken.acl[2].mask = 0x8 },
		"self token modified":   func(o *helperLaunchObservation) { o.selfToken.modifiedID++ },
		"supplier restarted":    func(o *helperLaunchObservation) { o.supplier.creation, o.supplierToken.tokenID = 44, 73 },
	} {
		t.Run(name, func(t *testing.T) {
			witness, _ := launchSourceFixture(t)
			lease, err := launchSource(t, witness, entryArgs()).prepare(requestContext(t))
			if err != nil {
				t.Fatal(err)
			}
			config := lease.config()
			mutate(&witness.observation)
			if err = lease.check(requestContext(t), config); !errors.Is(err, ErrDenied) {
				t.Fatal("drift accepted", err)
			}
			witness.observation = launchObservation()
			if err = lease.check(requestContext(t), config); !errors.Is(err, ErrDenied) {
				t.Fatal("drift revived", err)
			}
			if err = lease.close(requestContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHelperLaunchSourceReceiverRejectsSubstitutedBinding(t *testing.T) {
	witness, _ := launchSourceFixture(t)
	lease, err := launchSource(t, witness, entryArgs()).prepare(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	binding := lease.config().binding
	binding.child.tokenID++
	if err = lease.receiverSource().check(requestContext(t), binding); !errors.Is(err, ErrDenied) {
		t.Fatal("substituted receiver binding accepted", err)
	}
	if err = lease.receiverSource().check(requestContext(t), lease.config().binding); !errors.Is(err, ErrDenied) {
		t.Fatal("rejected binding revived", err)
	}
	if err = lease.close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestHelperLaunchSourceKeepsEvidenceUntilAllOriginalPipesClose(t *testing.T) {
	witness, factory := launchSourceFixture(t)
	var profile *profileReceiver
	var messages []*messageEndpoint
	profileFactory, messageFactory := factory.profile, factory.message
	factory.profile = func(ctx context.Context, h uintptr, b profileReceiverBinding, s profileReceiverSource, budget time.Duration) (*profileReceiver, error) {
		var err error
		profile, err = profileFactory(ctx, h, b, s, budget)
		return profile, err
	}
	factory.message = func(h uintptr, role endpointRole, budget time.Duration) (*messageEndpoint, error) {
		m, err := messageFactory(h, role, budget)
		messages = append(messages, m)
		return m, err
	}
	witness.closeFn = func() error {
		if profile == nil || profile.handle != 0 {
			t.Fatal("launch evidence released before profile close")
		}
		for _, m := range messages {
			if m == nil || m.o.read != 0 || m.o.write != 0 {
				t.Fatal("launch evidence released before message close")
			}
		}
		return nil
	}
	if err := runFixedHelper(requestContext(t), entryArgs(), launchSource(t, witness, entryArgs()), factory); err != nil {
		t.Fatal(err)
	}
}

func TestHelperLaunchSourceFatalEvidenceNeverReturnsToNativeCleanup(t *testing.T) {
	for _, stage := range []string{"acquire", "inspect", "close"} {
		t.Run(stage, func(t *testing.T) {
			witness, _ := launchSourceFixture(t)
			if stage == "acquire" {
				witness.acquireFn = func() error { return ErrFatal }
			}
			if stage == "inspect" {
				witness.inspectFn = func() error { return ErrFatal }
			}
			lease, err := launchSource(t, witness, entryArgs()).prepare(requestContext(t))
			if lease == nil {
				t.Fatal("fatal lost retained evidence")
			}
			wantCloses := 0
			if stage == "close" {
				if err != nil {
					t.Fatal(err)
				}
				witness.closeFn = func() error { return ErrFatal }
				err = lease.close(requestContext(t))
				wantCloses = 1
			}
			if !errors.Is(err, ErrFatal) {
				t.Fatal(err)
			}
			witness.acquireFn, witness.inspectFn, witness.closeFn = nil, nil, nil
			if err = lease.close(requestContext(t)); !errors.Is(err, ErrFatal) || witness.closed != wantCloses {
				t.Fatal("fatal resumed native cleanup", err, witness.closed)
			}
		})
	}
}
