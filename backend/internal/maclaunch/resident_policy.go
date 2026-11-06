package maclaunch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentwork"
	"golang.org/x/crypto/ssh"
)

var ErrResident = errors.New("maclaunch: resident scope refused or unavailable")
var ErrResidentUnknown = errors.New("maclaunch: resident original cleanup unconfirmed")

func residentReadError(err error) error {
	if errors.Is(err, agentkeys.ErrCleanup) {
		return errors.Join(ErrResident, ErrResidentUnknown, agentkeys.ErrCleanup)
	}
	return ErrResident
}

var ErrResidentPending = errors.New("maclaunch: resident work not prepared")

type residentOperation struct {
	id, command string
	timeout     uint32
}
type residentPolicy struct {
	binding    residentwork.Binding
	peer       ssh.PublicKey
	key        agentkeys.Config
	runtime    macagent.Config
	cleanup    time.Duration
	operations map[string]residentOperation
}

// ResidentClient is the output transport port, not an authentication producer.
type ResidentClient interface {
	Run(context.Context) error
	Close(context.Context) error
	CleanupResult() (error, bool)
}
type ResidentClientConfig struct {
	Binding    residentwork.Binding
	HostKey    ssh.Signer
	Cleanup    time.Duration
	WireIntent *agenttransport.ResidentFixedIntent
}
type ResidentTransport interface {
	NewClient(ResidentClientConfig, *Approved) (ResidentClient, error)
}
type residentDeps struct {
	budgets agentchannel.Budgets
	read    func(context.Context, string) ([]byte, error)
	key     func(context.Context, agentkeys.Config) (ssh.Signer, error)
	runtime func(macagent.Config) (agentexec.ProfiledRuntime, error)
	client  func(ResidentClientConfig, *Approved) (ResidentClient, error)
}
type ResidentFactory struct {
	blocked  atomic.Bool
	stopOnce sync.Once
	stopped  chan struct{}
	self     *ResidentFactory
	path     string
	digest   [32]byte
	policy   residentPolicy
	signer   ssh.Signer
	deps     residentDeps
}

func finiteResident(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	d, ok := ctx.Deadline()
	return ok && time.Now().Before(d)
}
func loadResident(ctx context.Context, policyPath string, d residentDeps) (*ResidentFactory, error) {
	return loadResidentMode(ctx, policyPath, d, false)
}
func loadResidentMode(ctx context.Context, policyPath string, d residentDeps, standby bool) (*ResidentFactory, error) {
	if !finiteResident(ctx) || d.read == nil || d.key == nil || d.runtime == nil || d.client == nil || !path.IsAbs(policyPath) || path.Clean(policyPath) != policyPath || policyPath == "/" {
		return nil, ErrResident
	}
	raw, e := d.read(ctx, policyPath)
	if e != nil {
		return nil, residentReadError(e)
	}
	defer clear(raw)
	p, e := parseResidentPolicy(raw)
	if e != nil || p.key.Path == policyPath || (standby && !fixedResidentPolicy(p)) {
		if standby {
			// The actual protected read succeeded: its tracked file handles
			// were closed and identity checked before return. No key/factory
			// has been acquired. The deferred raw clear precedes public return.
			return nil, startupPolicyRejected{}
		}
		return nil, ErrResident
	}
	signer, e := d.key(ctx, p.key)
	if e != nil {
		return nil, residentReadError(e)
	}
	if signer == nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != p.key.Fingerprint || !finiteResident(ctx) {
		return nil, ErrResident
	}
	f := &ResidentFactory{stopped: make(chan struct{}), path: policyPath, digest: sha256.Sum256(raw), policy: p, signer: signer, deps: d}
	f.self = f
	return f, nil
}
func parseResidentPolicy(raw []byte) (residentPolicy, error) {
	var p residentPolicy
	names := []string{"protocol", "policy_digest", "registration_endpoint", "host_label", "principal_label", "principal_public_key", "host_key_path", "host_key_fingerprint", "work_dir", "home", "cleanup_seconds", "operations"}
	if len(raw) == 0 || len(raw) > agentkeys.MaxTrustConfigBytes || agentwire.CheckJSON(raw, false) != nil {
		return p, ErrResident
	}
	fields, e := agentwire.Fields(raw, names...)
	if e != nil || len(fields) != len(names) {
		return p, ErrResident
	}
	str := func(n string) string {
		var s string
		if json.Unmarshal(fields[n], &s) != nil {
			return ""
		}
		return s
	}
	if str("protocol") != "rescue-mac-resident-policy/1" || str("policy_digest") != agentexec.MacCurrentUserPolicyDigest() {
		return p, ErrResident
	}
	p.binding = residentwork.Binding{Endpoint: str("registration_endpoint"), HostLabel: str("host_label"), PrincipalLabel: str("principal_label")}
	var a, b, c [32]byte
	a[0] = 1
	b[0] = 2
	c[0] = 3
	if _, e = residentwork.HostProofTranscript(p.binding, a, b, c); e != nil {
		return p, ErrResident
	}
	key := str("principal_public_key")
	encoded, e := base64.StdEncoding.DecodeString(key)
	if e != nil || base64.StdEncoding.EncodeToString(encoded) != key {
		return p, ErrResident
	}
	p.peer, e = ssh.ParsePublicKey(encoded)
	if e != nil || p.peer.Type() != ssh.KeyAlgoED25519 {
		return p, ErrResident
	}
	p.key = agentkeys.Config{Path: str("host_key_path"), Fingerprint: str("host_key_fingerprint")}
	p.runtime = macagent.Config{WorkDir: str("work_dir"), Home: str("home")}
	for _, v := range []string{p.key.Path, p.runtime.WorkDir, p.runtime.Home} {
		if !path.IsAbs(v) || path.Clean(v) != v || v == "/" || bytes.ContainsAny([]byte(v), "\x00\r\n") {
			return p, ErrResident
		}
	}
	var seconds uint32
	if json.Unmarshal(fields["cleanup_seconds"], &seconds) != nil || seconds < 7 || seconds > 10 {
		return p, ErrResident
	}
	p.cleanup = time.Duration(seconds) * time.Second
	var ops []json.RawMessage
	if json.Unmarshal(fields["operations"], &ops) != nil || len(ops) < 1 || len(ops) > 8 {
		return p, ErrResident
	}
	p.operations = make(map[string]residentOperation, len(ops))
	for _, raw := range ops {
		fields, e := agentwire.Fields(raw, "id", "command", "timeout_ms")
		if e != nil || len(fields) != 3 {
			return residentPolicy{}, ErrResident
		}
		var o residentOperation
		if json.Unmarshal(fields["id"], &o.id) != nil || json.Unmarshal(fields["command"], &o.command) != nil || json.Unmarshal(fields["timeout_ms"], &o.timeout) != nil || !residentwork.ValidOperationID(o.id) || len(o.command) < 1 || len(o.command) > agentwire.MaxCommandBytes || bytes.IndexByte([]byte(o.command), 0) >= 0 || o.timeout < 2000 || o.timeout > 30*60*1000 {
			return residentPolicy{}, ErrResident
		}
		if _, duplicate := p.operations[o.id]; duplicate {
			return residentPolicy{}, ErrResident
		}
		p.operations[o.id] = o
	}
	return p, nil
}

// block is irreversible for this exact loaded enrollment. Its sources revoke
// their owned grants; a fresh object is not a cleanup-recovery permission.
func (f *ResidentFactory) block() {
	if f == nil || f.self != f {
		return
	}
	f.blocked.Store(true)
	f.stopOnce.Do(func() { close(f.stopped) })
}
func (f *ResidentFactory) recheck(ctx context.Context) error {
	if f == nil || f.self != f || f.blocked.Load() || !finiteResident(ctx) {
		return ErrResident
	}
	raw, e := f.deps.read(ctx, f.path)
	if e != nil {
		if errors.Is(e, agentkeys.ErrCleanup) || ctx.Err() == nil {
			f.block()
		}
		return residentReadError(e)
	}
	defer clear(raw)
	if len(raw) > agentkeys.MaxTrustConfigBytes || sha256.Sum256(raw) != f.digest {
		f.block()
		return ErrResident
	}
	if !finiteResident(ctx) {
		return ErrResident
	}
	return nil
}
