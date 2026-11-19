// Package agentgateway is one shared remote MCP (Streamable HTTP, 2025-06-18)
// resource server for at most four configured principals. Each request is
// authenticated in exactly one configured mode, either an existing provider's
// bearer token or a Cloudflare Access application assertion, authorized by the
// exact verified subject, and routed only to that principal's current
// approved grant: one authenticated agentchannel opened with the principal's
// enrolled signer from a recipient-approved dispatch. It issues no grant,
// token, key, route or account, keeps every mapping in memory, never resumes
// after a restart and never forwards a bearer token. It does not listen.
package agentgateway

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"golang.org/x/crypto/ssh"
)

const (
	// MaxPrincipals is also a premise of the rescue code guessing bound
	// (docs/rescue-connection-start.md, 推測への耐性): redo it before raising.
	MaxPrincipals           = 4
	MaxSessionsPerPrincipal = 4
	MaxInflightRequests     = 32
	SessionIdle             = 30 * time.Minute
	// MaxAttachments bounds the LaunchIDs remembered in one process. Beyond
	// it Connect refuses until restart, which forgets every route anyway.
	MaxAttachments = 1024
)

var (
	ErrRefused = errors.New("agentgateway: registration refused")
	ErrClosed  = errors.New("agentgateway: gateway closed")
)

// Principal is one configured team member. Label is an operator name for
// diagnostics only; Subject is the exact verified "sub" of the provider.
// Signer is that member's enrolled raw Ed25519 agent key, supplied by trusted
// composition from protected storage; it is optional at New, and Connect
// refuses a principal without one.
type Principal struct {
	Label string
	// DisplayName is shown to a host in a rescue request; see docs/rescue-connection-start.md, 名前と組織の出どころ.
	DisplayName string
	Subject     string
	Roles       []Role
	Signer      ssh.Signer
}

// Config is chosen by the trusted operator composition, never by a request.
// Exactly one of Verifier and Access selects the authentication mode.
type Config struct {
	// DiscoveryOnly permanently disables all registration and execution.
	// Signers are forbidden, rather than silently ignored in this mode.
	ResidentOnly  bool // only correlated fixed work, never generic Connect or MCP binding
	DiscoveryOnly bool
	Verifier      *Verifier       // OAuth bearer JWT mode
	Access        *AccessVerifier // Cloudflare Access assertion mode
	Resource      string          // public MCP resource URL; OAuth audience may differ
	// Organization is shown to a host in a rescue request; see docs/rescue-connection-start.md, 名前と組織の出どころ.
	Organization   string
	Principals     []Principal
	RoleAssurance  map[Role]Assurance   // IdP strength per role; a missing role requires AssuranceAny
	AllowedOrigins []string             // exact Origin values; requests with any other Origin are refused
	Budgets        agentchannel.Budgets // every agent channel's budgets; see ChannelBudgets
	CallTimeout    time.Duration        // one wire round trip; zero selects mcpadapter.DefaultCallTimeout
	Clock          func() time.Time
	// RescueSigner is the rescue identity key (raw Ed25519) from protected
	// storage. Without it the rescue reception is closed.
	RescueSigner ssh.Signer
}

// ChannelBudgets are the fixed product budgets for an agent channel over a
// wide-area opaque stream.
func ChannelBudgets() agentchannel.Budgets {
	return agentchannel.Budgets{Handshake: 15 * time.Second, HeartbeatInterval: 10 * time.Second, HeartbeatTimeout: 10 * time.Second,
		ReadStall: 30 * time.Second, WriteStall: 15 * time.Second, Cleanup: 5 * time.Second}
}

// Dispatch aliases the channel's public routing/pinning metadata, not a
// credential. Host authentication and binding checks are unchanged.
type Dispatch = agentchannel.Dispatch

type principal struct {
	label, subject string
	displayName    string
	rescue         bool // RoleRescueOperator: the rescue connection (Connect)
	resident       bool // RoleResidentOwner: a pair slot and resident work
	signer         ssh.Signer
	route          *route // the current live route, guarded by Gateway.mu
	connecting     bool
	rescueBusy     bool
	rescueState    string
	rescueChanged  chan struct{}
	sessions       int
}

// route is one approved grant reachable through one authenticated channel.
// Its binding never changes; a new approval is a new route.
type route struct {
	owner       *principal
	binding     agentchannel.Binding
	channel     *agentchannel.Channel
	client      *agentwire.Client
	slots       chan struct{}
	rescue      bool          // opened by Connect; only such a route binds generic MCP sessions
	ended       chan struct{} // closed once the route is no longer routable
	cleanupDone chan struct{}
	cleanupErr  error
}

type launchGeneration struct {
	launchID   string
	generation uint64
}

type hostSlot struct {
	key        []byte
	principals map[string]bool
	paired     *principal
	replacing  bool
	record     *PairRecord
}

type session struct {
	owner    *principal
	route    *route // immutable; nil means permanently discovery-only
	mcp      *mcpadapter.HTTPSession
	lastUsed time.Time
	resident *residentSession
}

// Gateway holds every mapping in memory only.
type Gateway struct {
	hosts         map[string]*hostSlot
	pairing       *PairingConfig
	pairMu        sync.Mutex
	residentOnly  bool // immutable, trusted composition only
	discoveryOnly bool // immutable capability restriction, never request-selected
	organization  string
	ctx           context.Context
	cancel        context.CancelFunc
	verifier      *Verifier       // nil in Access mode
	access        *AccessVerifier // nil in OAuth bearer mode
	path          string
	metadataURL   string // OAuth bearer mode only
	metadata      []byte // OAuth bearer mode only
	origins       map[string]bool
	bySubject     map[string]*principal
	byLabel       map[string]*principal
	budgets       agentchannel.Budgets
	callTimeout   time.Duration
	clock         func() time.Time
	inflight      chan struct{}
	rescueCodes   *rescueCodes // rescue connection codes; guarded by its own lock
	rescueSigner  ssh.Signer
	rescue        *rescueReceptionState

	mu          sync.Mutex
	closed      bool
	sessions    map[string]*session
	launches    map[launchGeneration]struct{} // every LaunchID and connection generation registered in this process
	unconfirmed []*agentchannel.Channel
	watchers    sync.WaitGroup
	joinOnce    sync.Once
	joined      chan struct{}
	retry       *cleanupAttempt
	resident    *residentSetup
}

// New validates the configuration. It opens nothing and listens nowhere.
func New(cfg Config) (*Gateway, error) {
	if (cfg.DiscoveryOnly && cfg.ResidentOnly) || len(cfg.Principals) == 0 || len(cfg.Principals) > MaxPrincipals || cfg.CallTimeout < 0 {
		return nil, ErrConfig
	}
	var raw string
	switch {
	case cfg.Verifier != nil && cfg.Access == nil:
		raw = cfg.Resource
		if raw == "" {
			raw = cfg.Verifier.Audience()
		}
		if !httpsURL(raw, true) {
			return nil, ErrConfig
		}
	case cfg.Access != nil && cfg.Verifier == nil && httpsURL(cfg.Resource, true):
		raw = cfg.Resource
	default:
		return nil, ErrConfig
	}
	resource, err := url.Parse(raw)
	if err != nil {
		return nil, ErrConfig
	}
	if cfg.RescueSigner != nil && (cfg.DiscoveryOnly || cfg.RescueSigner.PublicKey().Type() != ssh.KeyAlgoED25519) {
		return nil, ErrConfig
	}
	if !validRescueIdentity(cfg.Organization) {
		return nil, ErrConfig
	}
	g := &Gateway{
		residentOnly: cfg.ResidentOnly, discoveryOnly: cfg.DiscoveryOnly, organization: cfg.Organization, verifier: cfg.Verifier, access: cfg.Access, path: resource.Path, origins: map[string]bool{},
		bySubject: map[string]*principal{}, byLabel: map[string]*principal{},
		budgets: cfg.Budgets, callTimeout: cfg.CallTimeout, clock: cfg.Clock, inflight: make(chan struct{}, MaxInflightRequests),
		sessions: map[string]*session{}, launches: map[launchGeneration]struct{}{}, joined: make(chan struct{}),
		rescueSigner: cfg.RescueSigner,
	}
	if g.clock == nil {
		g.clock = time.Now
	}
	g.rescue = &rescueReceptionState{endpoint: "wss://" + resource.Host + RescuePath, records: make(map[string]*rescueReceptionRecord)}
	g.rescueCodes = newRescueCodes(g.clock)
	if err := checkAssurance(cfg.RoleAssurance); err != nil {
		return nil, err
	}
	// In Access mode discovery belongs to the Access edge (Managed OAuth); the
	// origin publishes no metadata of its own that could name another issuer.
	if cfg.Verifier != nil {
		if g.metadataURL, g.metadata, err = protectedResourceMetadata(resource, cfg.Verifier); err != nil {
			return nil, err
		}
	}
	for _, o := range cfg.AllowedOrigins {
		if u, err := url.Parse(o); err != nil || !printable(o) || u.Scheme == "" || u.Host == "" || u.Path != "" || u.RawQuery != "" {
			return nil, ErrConfig
		}
		g.origins[o] = true
	}
	for i, p := range cfg.Principals {
		if !validLabel(p.Label) || !printable(p.Subject) || g.byLabel[p.Label] != nil || g.bySubject[p.Subject] != nil {
			return nil, ErrConfig
		}
		if p.Signer != nil && (cfg.DiscoveryOnly || p.Signer.PublicKey().Type() != ssh.KeyAlgoED25519) {
			return nil, ErrConfig
		}
		rescue, resident, ok := principalRoles(p.Roles)
		if !ok {
			return nil, rolesError(i)
		}
		if !validRescueIdentity(p.DisplayName) {
			return nil, ErrConfig
		}
		pr := &principal{label: p.Label, subject: p.Subject, displayName: p.DisplayName, rescue: rescue, resident: resident, signer: p.Signer, rescueState: "none", rescueChanged: make(chan struct{})}
		g.byLabel[p.Label], g.bySubject[p.Subject] = pr, pr
	}
	g.ctx, g.cancel = context.WithCancel(context.Background())
	if g.rescueSigner != nil {
		g.watchers.Add(1)
		go g.sweepRescue()
	}
	return g, nil
}

func validLabel(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Connect is the registration contract for the transport adapter. conn is the
// opaque stream to the recipient's agentlink.Host (for example over the future
// stream adapter); d came from that recipient's explicit approval. Connect
// takes ownership of conn on every call, opens exactly one agent channel with
// the principal's enrolled signer and the pinned host key, and only then makes
// it the principal's route. It refuses, closing conn, an unknown label, a
// principal without a signer, a principal that already has or is acquiring a
// route, a profile other than the Mac current user, and any (LaunchID,
// ConnectionGeneration) already registered in this process. It admits only
// generation 1; resume is bound to the same reception by AnswerRescue. It
// also refuses a principal without RoleRescueOperator and a dispatch from a
// resident host (a pair slot or an enrolled resident), whoever owns it. A
// refused or failed registration consumes that pair only once opening was
// admitted. A pre-open refusal closes the supplied stream; it does not resume
// the approval.
// The gateway never issues or extends a grant.
func (g *Gateway) Connect(label string, d Dispatch, conn net.Conn) error {
	// ConnectResident uses the private connect port only after correlation and
	// enrolled-host proof. Never let legacy registration enter that path.
	if g != nil && g.residentOnly {
		if conn != nil {
			_ = conn.Close()
		}
		return ErrRefused
	}
	if d.Binding.ConnectionGeneration != 1 {
		if conn != nil {
			_ = conn.Close()
		}
		return ErrRefused
	}
	if g != nil && g.rescue != nil {
		g.mu.Lock()
		principal := g.byLabel[label]
		g.mu.Unlock()
		g.rescue.mu.Lock()
		for _, record := range g.rescue.records {
			if record.principal != nil && record.dispatch.Binding.Grant.Identity.LaunchID == d.Binding.Grant.Identity.LaunchID && record.principal != principal {
				g.rescue.mu.Unlock()
				if conn != nil {
					_ = conn.Close()
				}
				return ErrRefused
			}
		}
		g.rescue.mu.Unlock()
	}
	_, e := g.connect(label, d, conn, true)
	return e
}

func (g *Gateway) connect(label string, d Dispatch, conn net.Conn, rescue bool) (*route, error) {
	return g.connectWithSigner(label, d, conn, rescue, nil, false)
}

func (g *Gateway) connectWithSigner(label string, d Dispatch, conn net.Conn, rescue bool, signer ssh.Signer, pinned bool) (*route, error) {
	return g.connectWithSignerForRescue(label, d, conn, rescue, signer, pinned, nil, nil)
}

func (g *Gateway) connectWithSignerForRescue(label string, d Dispatch, conn net.Conn, rescue bool, signer ssh.Signer, pinned bool,
	publication *rescueRoutePublication, onActivity func()) (*route, error) {
	if g == nil || conn == nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, ErrConfig
	}
	launch := d.Binding.Grant.Identity.LaunchID
	key := launchGeneration{launchID: launch, generation: d.Binding.ConnectionGeneration}
	g.mu.Lock()
	p := g.byLabel[label]
	if signer == nil && p != nil {
		signer = p.signer
	}
	_, reused := g.launches[key]
	var refusal error
	switch {
	case g.closed:
		refusal = ErrClosed
	case g.discoveryOnly:
		refusal = ErrRefused
	case p == nil || signer == nil || d.Binding.Profile != agentexec.ProfileMacCurrentUser || launch == "" || key.generation == 0:
		refusal = ErrConfig
	case rescue && !p.rescue:
		refusal = ErrRefused
	case !pinned && !g.dispatchHostAllowedLocked(p, d.HostKey, rescue):
		refusal = ErrRefused
	case reused || p.route != nil || p.connecting || len(g.launches) >= MaxAttachments:
		refusal = ErrRefused
	default:
		p.connecting = true
		g.launches[key] = struct{}{}
		g.watchers.Add(1) // own acquisition before Close can begin waiting
	}
	g.mu.Unlock()
	if refusal != nil {
		_ = conn.Close()
		return nil, refusal
	}
	defer g.watchers.Done() // includes failed/late acquisition and retire
	r, err := g.openWithSigner(p, d, conn, signer, onActivity)
	if publication != nil {
		g.rescue.mu.Lock()
	}
	g.mu.Lock()
	p.connecting = false
	if err != nil {
		g.mu.Unlock()
		if publication != nil {
			g.rescue.mu.Unlock()
		}
		return nil, err
	}
	if g.closed {
		g.mu.Unlock()
		if publication != nil {
			g.rescue.mu.Unlock()
		}
		g.retire(r)
		return r, ErrClosed
	}
	if !pinned && !g.dispatchHostAllowedLocked(p, d.HostKey, rescue) {
		g.mu.Unlock()
		if publication != nil {
			g.rescue.mu.Unlock()
		}
		g.retire(r)
		return nil, ErrRefused
	}
	if publication != nil {
		record := publication.record
		if g.rescue.records[publication.id] != record || record.attemptID != publication.attemptID || !record.opening ||
			(record.resuming && (record.lastFrame.IsZero() || g.clock().Sub(record.lastFrame) >= RescueReconnectGrace)) {
			g.mu.Unlock()
			g.rescue.mu.Unlock()
			g.retire(r)
			return nil, ErrRefused
		}
	}
	r.rescue = rescue
	p.route = r
	g.watchers.Add(1)
	g.mu.Unlock()
	if publication != nil {
		g.rescue.mu.Unlock()
	}
	go g.watch(r)
	return r, nil
}

// dispatchHostAllowedLocked never admits a resident host for a rescue
// connection: its key and grants belong to the resident owner's own use.
func (g *Gateway) dispatchHostAllowedLocked(p *principal, key []byte, rescue bool) bool {
	if g.hosts == nil {
		return true
	}
	for name, slot := range g.hosts {
		if rescue && g.residentHostLocked(name, slot) {
			continue
		}
		if slot.principals[p.label] && !slot.replacing && len(slot.key) != 0 && bytes.Equal(slot.key, key) {
			return true
		}
	}
	return false
}

func (g *Gateway) residentHostLocked(name string, slot *hostSlot) bool {
	if slot.paired != nil {
		return true
	}
	if g.resident != nil {
		for _, e := range g.resident.entries {
			if e.enrollment.HostLabel == name {
				return true
			}
		}
	}
	return false
}

func (g *Gateway) open(p *principal, d Dispatch, conn net.Conn) (*route, error) {
	return g.openWithSigner(p, d, conn, p.signer, nil)
}

func (g *Gateway) openWithSigner(p *principal, d Dispatch, conn net.Conn, signer ssh.Signer, onActivity func()) (*route, error) {
	host, err := ssh.ParsePublicKey(d.HostKey)
	if err != nil {
		_ = conn.Close()
		return nil, ErrConfig
	}
	// The channel lives until the gateway closes or the channel ends; its own
	// deadline is the dispatched grant deadline.
	ch, err := agentchannel.Open(g.ctx, agentchannel.ClientConfig{
		Binding: d.Binding, HostKey: host, Signer: signer, Deadline: d.Deadline, Budgets: g.budgets, OnActivity: onActivity,
	}, conn)
	if err != nil {
		if ch != nil {
			g.mu.Lock()
			g.unconfirmed = append(g.unconfirmed, ch)
			g.mu.Unlock()
			return nil, errors.Join(err, agentchannel.ErrCleanupUnknown)
		}
		return nil, err
	}
	if ch.Binding() != d.Binding {
		return nil, errors.Join(ErrRefused, g.closeChannel(ch))
	}
	client, err := agentwire.NewClient(ch)
	if err != nil {
		return nil, errors.Join(err, g.closeChannel(ch))
	}
	return &route{
		owner: p, binding: d.Binding, channel: ch, client: client, slots: make(chan struct{}, mcpadapter.MaxConcurrent),
		ended: make(chan struct{}), cleanupDone: make(chan struct{}),
	}, nil
}

// closeChannel retains a channel whose cleanup stays unconfirmed for Close.
func (g *Gateway) closeChannel(ch *agentchannel.Channel) error {
	err := ch.Close()
	if errors.Is(err, agentchannel.ErrCleanupUnknown) {
		g.mu.Lock()
		g.unconfirmed = append(g.unconfirmed, ch)
		g.mu.Unlock()
	}
	return err
}

// watch ends the route when its channel or wire client ends (heartbeat loss,
// stall, hard deadline, peer close) or the gateway closes. Sessions bound to
// it become unknown; their in-flight calls still report the transport error.
func (g *Gateway) watch(r *route) {
	defer g.watchers.Done()
	select {
	case <-r.channel.Done():
	case <-r.client.Done():
	case <-g.ctx.Done():
	}
	g.retire(r)
}

func (g *Gateway) retire(r *route) {
	g.mu.Lock()
	if r.owner.route == r {
		r.owner.route = nil
	}
	for id, s := range g.sessions {
		if s.route == r {
			delete(g.sessions, id)
			s.owner.sessions--
		}
	}
	g.mu.Unlock()
	close(r.ended)
	_ = r.client.Close()
	r.cleanupErr = g.closeChannel(r.channel)
	close(r.cleanupDone)
}

// Close refuses further work, ends every session and route (the remote hosts
// revoke their grants on transport loss; nothing resumes) and waits within
// ctx for the channel joins. Unconfirmed channel cleanup is reported and
// retried by a later Close of the same owners.
func (g *Gateway) Close(ctx context.Context) error {
	if g == nil || ctx == nil {
		return ErrConfig
	}
	g.mu.Lock()
	g.closed = true
	for id, s := range g.sessions {
		s.mcp.Close()
		delete(g.sessions, id)
	}
	g.mu.Unlock()
	g.rescue.mu.Lock()
	for id, record := range g.rescue.records {
		g.endRescueLocked([]byte(id), record)
	}
	g.rescue.mu.Unlock()
	g.cancel()
	g.joinOnce.Do(func() {
		go func() {
			g.watchers.Wait()
			close(g.joined)
		}()
	})
	select {
	case <-g.joined:
	case <-ctx.Done():
		return errors.Join(agentchannel.ErrCleanupUnknown, ctx.Err())
	}
	a := g.retryCleanup()
	if a == nil {
		return nil
	}
	select {
	case <-a.done:
		return a.err
	case <-ctx.Done():
		return errors.Join(agentchannel.ErrCleanupUnknown, ctx.Err())
	}
}

type cleanupAttempt struct {
	done chan struct{}
	err  error // immutable after done closes
}

// Concurrent Close calls share one bounded retry of the same held channels.
// A caller timeout leaves the attempt owned; another Close cannot observe an
// empty list and mistake an in-flight retry for confirmed cleanup.
func (g *Gateway) retryCleanup() *cleanupAttempt {
	g.mu.Lock()
	if a := g.retry; a != nil {
		select {
		case <-a.done:
		default:
			g.mu.Unlock()
			return a
		}
	}
	if len(g.unconfirmed) == 0 {
		g.mu.Unlock()
		return nil
	}
	held := g.unconfirmed
	g.unconfirmed = nil
	a := &cleanupAttempt{done: make(chan struct{})}
	g.retry = a
	g.mu.Unlock()
	go func() {
		var errs []error
		for _, ch := range held {
			if err := ch.Close(); errors.Is(err, agentchannel.ErrCleanupUnknown) {
				errs = append(errs, err)
				g.mu.Lock()
				g.unconfirmed = append(g.unconfirmed, ch)
				g.mu.Unlock()
			}
		}
		a.err = errors.Join(errs...)
		close(a.done)
	}()
	return a
}
