package maclaunch

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentwork"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
	"golang.org/x/crypto/ssh"
)

type residentClientFake struct{ runs, closes atomic.Int32 }

func (c *residentClientFake) Run(ctx context.Context) error {
	c.runs.Add(1)
	<-ctx.Done()
	return ctx.Err()
}
func (c *residentClientFake) Close(context.Context) error { c.closes.Add(1); return nil }
func (*residentClientFake) CleanupResult() (error, bool)  { return nil, true }
func residentFixture(t *testing.T) (*ResidentFactory, *ssh.Signer, *[]byte, *neverRuntime) {
	t.Helper()
	cfg := config(t)
	gateway, e := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(map[string]interface{}{"protocol": "rescue-mac-resident-policy/1", "policy_digest": agentexec.MacCurrentUserPolicyDigest(), "registration_endpoint": "wss://example.test/register", "host_label": "host1", "principal_label": "member1", "principal_public_key": base64.StdEncoding.EncodeToString(gateway.PublicKey().Marshal()), "host_key_path": "/Users/test/keys/host", "host_key_fingerprint": ssh.FingerprintSHA256(cfg.HostKey.PublicKey()), "work_dir": "/Users/test/work", "home": "/Users/test", "cleanup_seconds": 10, "operations": []interface{}{map[string]interface{}{"id": "status", "command": "printf TETHER_RESIDENT_OK", "timeout_ms": 15000}}})
	if e != nil {
		t.Fatal(e)
	}
	rt := &neverRuntime{}
	d := residentDeps{read: func(context.Context, string) ([]byte, error) { return append([]byte(nil), raw...), nil }, key: func(context.Context, agentkeys.Config) (ssh.Signer, error) { return cfg.HostKey, nil }, runtime: func(macagent.Config) (agentexec.ProfiledRuntime, error) { return rt, nil }, budgets: cfg.Budgets, client: func(c ResidentClientConfig, o *Approved) (ResidentClient, error) {
		b := agenttransport.DefaultBudgets()
		b.Cleanup = c.Cleanup
		return agenttransport.NewClient(agenttransport.ClientConfig{Endpoint: c.Binding.Endpoint, HostLabel: c.Binding.HostLabel, PrincipalLabel: c.Binding.PrincipalLabel, HostKey: c.HostKey, Budgets: b}, o)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	f, e := loadResident(ctx, "/Users/test/resident.json", d)
	if e != nil {
		t.Fatal(e)
	}
	return f, &gateway, &raw, rt
}
func residentIntent(t *testing.T, s *ResidentSource, signer ssh.Signer, id string) residentwork.Intent {
	t.Helper()
	var remote [32]byte
	remote[0] = 9
	if _, e := s.HostProof(remote); e != nil {
		t.Fatal(e)
	}
	a := s.Advertisement()
	i := residentwork.Intent{Version: residentwork.Version, SourceNonce: a.SourceNonce, ServerNonce: remote, PolicySHA256: a.PolicySHA256, RequestID: id, OperationID: "status", ExpiresUnixMilli: time.Now().Add(12 * time.Second).UnixMilli()}
	data, e := residentwork.IntentTranscript(a.Binding, i)
	if e != nil {
		t.Fatal(e)
	}
	sig, e := signer.Sign(nil, data)
	if e != nil {
		t.Fatal(e)
	}
	i.Signature = append([]byte(nil), sig.Blob...)
	return i
}
func TestResidentFactorySignedScopeCreatesActualCanonicalOwnerWithoutTTY(t *testing.T) {
	f, signer, _, rt := residentFixture(t)
	host, stop := context.WithCancel(context.Background())
	defer stop()
	s, e := f.OpenSource(host)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	i := residentIntent(t, s, *signer, "request1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, e := s.NewRequest(ctx, i)
	if e != nil {
		t.Fatal(e)
	}
	owned, e := r.Approve(ctx)
	if e != nil || owned == nil {
		t.Fatal(e)
	}
	w, ok := owned.Session.(*ResidentWorker)
	if !ok {
		t.Fatal("missing original real worker")
	}
	if d := w.Dispatch(); d.Binding.Profile != agentexec.ProfileMacCurrentUser || d.Binding.Grant.Generation == 0 || d.Binding.Grant.Identity.PolicyDigest != agentexec.MacCurrentUserPolicyDigest() {
		t.Fatal("canonical dispatch missing")
	}
	if rt.starts.Load() != 0 {
		t.Fatal("admission launched command")
	}
	if _, e = s.NewRequest(ctx, i); !errors.Is(e, ErrResident) {
		t.Fatal("intent replay accepted", e)
	}
	cleanup, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if e = w.Close(cleanup); e != nil {
		t.Fatal(e)
	}
	if w.Dispatch().Binding.Profile != 0 {
		t.Fatal("closed work published dispatch")
	}
}

func closeResidentTest(t *testing.T, owned *workerowner.Worker) {
	t.Helper()
	if owned == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := owned.Session.Close(ctx); e != nil {
		t.Fatal(e)
	}
}
func signResidentTest(t *testing.T, s *ResidentSource, signer ssh.Signer, i residentwork.Intent) residentwork.Intent {
	t.Helper()
	data, e := residentwork.IntentTranscript(s.Advertisement().Binding, i)
	if e != nil {
		t.Fatal(e)
	}
	sig, e := signer.Sign(nil, data)
	if e != nil {
		t.Fatal(e)
	}
	i.Signature = append([]byte(nil), sig.Blob...)
	return i
}
func TestResidentFactoryRejectsAlteredOrOutOfScopeIntents(t *testing.T) {
	for _, kind := range []string{"signature", "missing-signature", "source", "server", "policy", "operation", "expiry", "request-length"} {
		t.Run(kind, func(t *testing.T) {
			f, key, _, rt := residentFixture(t)
			s, e := f.OpenSource(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			i := residentIntent(t, s, *key, "request1")
			switch kind {
			case "signature":
				i.Signature[0] ^= 1
			case "missing-signature":
				i.Signature = nil
			case "source":
				i.SourceNonce[0] ^= 1
			case "server":
				i.ServerNonce[0] ^= 1
			case "policy":
				i.PolicySHA256[0] ^= 1
			case "operation":
				i.OperationID = "not-enrolled"
				i = signResidentTest(t, s, *key, i)
			case "expiry":
				i.ExpiresUnixMilli = time.Now().Add(time.Hour).UnixMilli()
				i = signResidentTest(t, s, *key, i)
			case "request-length":
				i.RequestID = strings.Repeat("x", 49)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if r, e := s.NewRequest(ctx, i); r != nil || !errors.Is(e, ErrResident) {
				t.Fatal("invalid intent admitted", kind, e)
			}
			if rt.starts.Load() != 0 {
				t.Fatal("rejection started native work")
			}
		})
	}
}
func TestResidentFactoryOriginalScopeCannotBeReplacedByLongCaller(t *testing.T) {
	f, key, _, _ := residentFixture(t)
	s, _ := f.OpenSource(context.Background())
	defer s.Close()
	i := residentIntent(t, s, *key, "original")
	original, stop := context.WithTimeout(context.Background(), time.Minute)
	r, e := s.NewRequest(original, i)
	if e != nil {
		t.Fatal(e)
	}
	stop()
	later, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	w, e := r.Approve(later)
	if !errors.Is(e, ErrResident) {
		t.Fatal("canceled original scope replaced", e)
	}
	closeResidentTest(t, w)
}
func TestResidentFactoryProtectedPolicyDriftRefusesBeforeGrant(t *testing.T) {
	f, key, raw, rt := residentFixture(t)
	s, _ := f.OpenSource(context.Background())
	defer s.Close()
	i := residentIntent(t, s, *key, "drift")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, e := s.NewRequest(ctx, i)
	if e != nil {
		t.Fatal(e)
	}
	*raw = append(*raw, ' ')
	w, e := r.Approve(ctx)
	if !errors.Is(e, ErrResident) || w == nil {
		t.Fatal(e)
	}
	if w.Session.(*ResidentWorker).Dispatch().Binding.Profile != 0 || rt.starts.Load() != 0 {
		t.Fatal("changed policy became a grant")
	}
	closeResidentTest(t, w)
}
func TestResidentFactoryCanonicalConstraintRejectsCommandExpansion(t *testing.T) {
	f, key, _, rt := residentFixture(t)
	s, _ := f.OpenSource(context.Background())
	defer s.Close()
	i := residentIntent(t, s, *key, "fixed")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, e := s.NewRequest(ctx, i)
	if e != nil {
		t.Fatal(e)
	}
	w, e := r.Approve(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer closeResidentTest(t, w)
	actual := w.Session.(*ResidentWorker)
	session := actual.approved.session
	if _, ok := actual.client.(*agenttransport.Client); !ok {
		t.Fatal("real finite transport client was not composed")
	}
	request := agentexec.StartRequest{Version: agentexec.Version, Binding: session.Binding(), Mode: session.Mode(), RequestID: "fixed", Command: []byte("printf EXPANDED"), TimeoutMillis: 15000}
	if _, e = session.Start(request); !errors.Is(e, agentexec.ErrPolicy) {
		t.Fatal("remote command expansion admitted", e)
	}
	if rt.starts.Load() != 0 {
		t.Fatal("expansion reached runtime")
	}
	request.Command = []byte("printf TETHER_RESIDENT_OK")
	if _, e = session.Start(request); e != nil {
		t.Fatal(e)
	}
	limit := time.Now().Add(time.Second)
	for rt.starts.Load() == 0 && time.Now().Before(limit) {
		time.Sleep(time.Millisecond)
	}
	if rt.starts.Load() != 1 {
		t.Fatal("exact scoped command did not reach canonical runtime port")
	}
}
func TestResidentFactoryCleanupUnknownAndPanicRetainCapsule(t *testing.T) {
	for _, kind := range []string{"read-cleanup", "runtime-panic"} {
		t.Run(kind, func(t *testing.T) {
			f, key, _, _ := residentFixture(t)
			s, _ := f.OpenSource(context.Background())
			defer s.Close()
			i := residentIntent(t, s, *key, "partial")
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			r, e := s.NewRequest(ctx, i)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "read-cleanup" {
				f.deps.read = func(context.Context, string) ([]byte, error) { return nil, agentkeys.ErrCleanup }
			} else {
				f.deps.runtime = func(macagent.Config) (agentexec.ProfiledRuntime, error) { panic("SENTINEL_PRIVATE_FAILURE") }
			}
			owned, e := r.Approve(ctx)
			if owned == nil || !errors.Is(e, ErrResidentUnknown) || strings.Contains(e.Error(), "SENTINEL") {
				t.Fatal("partial state lost or raw failure leaked", e)
			}
			if fresh, e := f.OpenSource(context.Background()); fresh != nil || !errors.Is(e, ErrResident) {
				t.Fatal("uncertain factory admitted a replacement source", e)
			}
			w, observed := s.Worker("partial")
			if w != owned.Session || !errors.Is(observed, ErrResidentUnknown) {
				t.Fatal("original capsule not retained")
			}
			closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if e = w.Close(closeCtx); !errors.Is(e, workerowner.ErrUnknown) {
				t.Fatal("uncertain native read normalized to success", e)
			}
			if w.Dispatch().Binding.Profile != 0 {
				t.Fatal("partial work exposed dispatch")
			}
		})
	}
}
func TestResidentPolicySchemaIsStrict(t *testing.T) {
	_, _, raw, _ := residentFixture(t)
	for _, kind := range []string{"duplicate", "case", "unknown", "null", "command", "timeout", "operations", "key"} {
		t.Run(kind, func(t *testing.T) {
			input := append([]byte(nil), (*raw)...)
			switch kind {
			case "duplicate":
				input = append([]byte(`{"protocol":"rescue-mac-resident-policy/1",`), input[1:]...)
			case "case":
				input = bytes.Replace(input, []byte(`"protocol"`), []byte(`"Protocol"`), 1)
			default:
				var obj map[string]interface{}
				if json.Unmarshal(input, &obj) != nil {
					t.Fatal("fixture")
				}
				switch kind {
				case "unknown":
					obj["approved"] = true
				case "null":
					obj["operations"] = nil
				case "command":
					obj["operations"].([]interface{})[0].(map[string]interface{})["command"] = ""
				case "timeout":
					obj["operations"].([]interface{})[0].(map[string]interface{})["timeout_ms"] = 0
				case "operations":
					obj["operations"] = []interface{}{}
				case "key":
					obj["principal_public_key"] = "invalid"
				}
				input, _ = json.Marshal(obj)
			}
			if _, e := parseResidentPolicy(input); !errors.Is(e, ErrResident) {
				t.Fatal("untrusted schema accepted", kind, e)
			}
		})
	}
}
func TestResidentSourceCloseRevokesActualGrantAndBlocksFreshRequests(t *testing.T) {
	f, key, _, _ := residentFixture(t)
	s, _ := f.OpenSource(context.Background())
	i := residentIntent(t, s, *key, "revoke")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, e := s.NewRequest(ctx, i)
	if e != nil {
		t.Fatal(e)
	}
	owned, e := r.Approve(ctx)
	if e != nil {
		t.Fatal(e)
	}
	w := owned.Session.(*ResidentWorker)
	s.Close()
	if w.Dispatch().Binding.Profile != 0 || w.approved.issuer.Snapshot().HasAuthority {
		t.Fatal("source closed without revoking original authority")
	}
	if _, e := s.NewRequest(ctx, i); !errors.Is(e, ErrResident) {
		t.Fatal("closed source accepted work", e)
	}
	closeResidentTest(t, owned)
}

type residentObservedClient struct {
	owner    *Approved
	started  chan context.Context
	release  <-chan struct{}
	closes   atomic.Int32
	mu       sync.Mutex
	cleanup  error
	observed bool
}

func (c *residentObservedClient) Run(ctx context.Context) error {
	c.started <- ctx
	if c.release != nil {
		<-c.release
	} else {
		<-ctx.Done()
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.owner.Close(cleanup)
	c.closes.Add(1)
	c.mu.Lock()
	c.cleanup, c.observed = err, true
	c.mu.Unlock()
	return ctx.Err()
}
func (c *residentObservedClient) Close(ctx context.Context) error {
	c.closes.Add(1)
	return c.owner.Close(ctx)
}
func (c *residentObservedClient) CleanupResult() (error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cleanup, c.observed
}
func TestResidentWorkerRunKeepsOriginalDeadlineAndObservesCleanupOnce(t *testing.T) {
	f, key, _, _ := residentFixture(t)
	var client *residentObservedClient
	f.deps.client = func(_ ResidentClientConfig, a *Approved) (ResidentClient, error) {
		client = &residentObservedClient{owner: a, started: make(chan context.Context, 1)}
		return client, nil
	}
	s, _ := f.OpenSource(context.Background())
	defer s.Close()
	i := residentIntent(t, s, *key, "run")
	original, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, e := s.NewRequest(original, i)
	if e != nil {
		t.Fatal(e)
	}
	owned, e := r.Approve(original)
	if e != nil {
		t.Fatal(e)
	}
	w := owned.Session.(*ResidentWorker)
	later, end := context.WithTimeout(context.Background(), time.Hour)
	defer end()
	done := make(chan error, 1)
	go func() { done <- w.Run(later) }()
	var observed context.Context
	select {
	case observed = <-client.started:
	case <-time.After(time.Second):
		t.Fatal("real worker did not invoke transport port")
	}
	d, _ := observed.Deadline()
	want, _ := original.Deadline()
	if d.After(want) {
		t.Fatal("Run renewed original work deadline")
	}
	s.Close()
	select {
	case <-w.approved.session.Done():
	default:
		t.Fatal("source close left canonical session live")
	}
	cleanup, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if e = w.Close(cleanup); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-cleanup.Done():
		t.Fatal("Run not joined")
	}
	if client.closes.Load() != 1 {
		t.Fatal("worker retried completed client cleanup", client.closes.Load())
	}
	if e = w.Close(cleanup); e != nil || client.closes.Load() != 1 {
		t.Fatal("repeated Close repeated native cleanup", e)
	}
}
func TestResidentWorkerUnknownRunJoinRetainsOriginalCapsule(t *testing.T) {
	f, key, _, _ := residentFixture(t)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var client *residentObservedClient
	f.deps.client = func(_ ResidentClientConfig, a *Approved) (ResidentClient, error) {
		client = &residentObservedClient{owner: a, started: make(chan context.Context, 1), release: release}
		return client, nil
	}
	s, _ := f.OpenSource(context.Background())
	defer s.Close()
	i := residentIntent(t, s, *key, "unknown")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, _ := s.NewRequest(ctx, i)
	owned, e := r.Approve(ctx)
	if e != nil {
		t.Fatal(e)
	}
	w := owned.Session.(*ResidentWorker)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("Run absent")
	}
	cleanup, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if e = w.Close(cleanup); !errors.Is(e, workerowner.ErrUnknown) {
		t.Fatal(e)
	}
	if client.closes.Load() != 0 || w.Dispatch().Binding.Profile != 0 {
		t.Fatal("unjoined Run was normalized")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("late Run did not return")
	}
	later, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if e = w.Close(later); !errors.Is(e, workerowner.ErrUnknown) || client.closes.Load() != 1 {
		t.Fatal("unknown close was retried or upgraded", e)
	}
}

type residentCancelAtRegistration struct {
	context.Context
	cancel context.CancelFunc
}

func (c residentCancelAtRegistration) Value(interface{}) interface{} { return nil }
func (c residentCancelAtRegistration) AfterFunc(f func()) func() bool {
	c.cancel()
	go f()
	return func() bool { return false }
}
func TestResidentWorkerCancellationBeforeClientEntryStillClosesRealClient(t *testing.T) {
	f, key, _, _ := residentFixture(t)
	s, _ := f.OpenSource(context.Background())
	defer s.Close()
	i := residentIntent(t, s, *key, "entry-race")
	original, stopOriginal := context.WithTimeout(context.Background(), time.Minute)
	defer stopOriginal()
	r, _ := s.NewRequest(original, i)
	owned, e := r.Approve(original)
	if e != nil {
		t.Fatal(e)
	}
	w := owned.Session.(*ResidentWorker)
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e = w.Run(residentCancelAtRegistration{caller, cancel}); !errors.Is(e, ErrResident) {
		t.Fatal("controlled cancellation did not precede client entry", e)
	}
	cleanup, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if e = w.Close(cleanup); e != nil {
		t.Fatal("unstarted client cleanup skipped", e)
	}
	if err, observed := w.client.CleanupResult(); !observed || err != nil {
		t.Fatal("original client cleanup not observed", observed, err)
	}
}
func TestResidentRequestCapturesMonotonicReceiptBeforeDelayedAdmission(t *testing.T) {
	f, key, _, _ := residentFixture(t)
	s, _ := f.OpenSource(context.Background())
	defer s.Close()
	i := residentIntent(t, s, *key, "elapsed")
	original, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	r, e := s.NewRequest(original, i)
	if e != nil {
		t.Fatal(e)
	}
	// Round(0) strips monotonic clock data. A wall-only signed expiry would be
	// renewed by a later admission after a backward wall-clock step.
	if r.Deadline == r.Deadline.Round(0) {
		t.Fatal("signed expiry was not frozen to receipt's monotonic clock")
	}
	if !r.Deadline.Equal(time.UnixMilli(i.ExpiresUnixMilli)) {
		t.Fatal("receipt conversion changed signed wall expiry")
	}
	owned, e := r.Approve(original)
	if e != nil {
		t.Fatal(e)
	}
	defer closeResidentTest(t, owned)
	got, _ := owned.Session.(*ResidentWorker).operation.Deadline()
	if got != r.Deadline {
		t.Fatal("admission recomputed original elapsed cap")
	}
}

// Shared dispatch conformance belongs with the concrete approval owner.
var _ agenttransport.Owner = (*Approved)(nil)
