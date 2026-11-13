package agentgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"golang.org/x/crypto/ssh"
)

const ResidentFixedID = "tether-fixed-command-v1"

// ResidentRunID names the command-run operation in every signed transcript.
const ResidentRunID = "tether-run-command-v1"

// ResidentRunScope is what the gateway signed for a command run. The zero value
// means fixed work. It is part of the work binding so the Mac's registration and
// collection proofs name the command, directory and timeout it acted on.
type ResidentRunScope struct {
	CommandSHA256, Cwd string
	TimeoutMillis      uint32
}

const ResidentRunMaxTimeoutMillis = 240000

// ResidentRunMinTimeoutMillis is the shortest timeout this gateway accepts for a
// command run: the fixed routine's whole budget. The timeout is also the work's
// deadline, and work that expires before the Mac answers is never collected and
// keeps its session busy.
const ResidentRunMinTimeoutMillis = 15000

// WellFormed holds every bound on a scope that needs no clock or link state.
// The gateway checks it before any work exists and before signing, and the Mac
// before accepting. Format controls and line separators are refused with
// control characters because the Mac shows cwd to the person who allows the
// command.
func (s ResidentRunScope) WellFormed() bool {
	c := s.Cwd
	_, err := hex.DecodeString(s.CommandSHA256)
	return s.TimeoutMillis >= 1 && s.TimeoutMillis <= ResidentRunMaxTimeoutMillis && len(s.CommandSHA256) == 64 && err == nil && strings.ToLower(s.CommandSHA256) == s.CommandSHA256 && c != "" && len(c) <= 1024 && utf8.ValidString(c) && strings.IndexFunc(c, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
	}) < 0 && path.Clean(c) == c && c[0] != '/' && c != ".." && !strings.HasPrefix(c, "../")
}

const ResidentFixedCommand = "/usr/bin/printf 'TETHER_MCP_GUI_BEGIN\\n'; /bin/sleep 5; /usr/bin/printf 'TETHER_MCP_GUI_END\\n'"

var ErrResidentWorkUnconfigured = errors.New("agentgateway: host refused without work acquisition")
var ErrResidentPending = errors.New("agentgateway: fixed work admitted, result pending")

// ResidentControl is a trusted normal-gateway composition port, never an HTTP
// approval API. Every HTTP request still authenticates and owns its MCP session.
type ResidentControl interface {
	ResidentGeneration(host, principal string) string
	BeginResidentFixed(context.Context, string, string, string, string, time.Time) error
	BeginResidentRun(context.Context, string, string, string, string, time.Time, ResidentRunScope) error
}
type ResidentEnrollment struct {
	HostLabel, PrincipalLabel string
	HostKey                   []byte
}
type ResidentWorkBinding struct {
	Host, Principal, Generation, RequestID string
	Sequence                               uint64
	DeadlineMillis                         int64
	Run                                    ResidentRunScope
}
type residentSetup struct {
	endpoint string
	control  ResidentControl
	entries  map[*principal]*residentEntry
}
type residentEntry struct {
	enrollment ResidentEnrollment
	works      map[string]*residentWork
	tombstones map[string]residentTombstone
	current    *residentWork
}
type residentWork struct {
	binding            ResidentWorkBinding
	route              *route
	ready              chan struct{}
	collected          chan struct{}
	collectionSeen     bool
	ownerCleanup       string
	gatewayCleanup     string
	phase              string
	status             agentwire.Status
	output             []map[string]any
	cursor             uint64
	outputBytes        int
	failure            string
	cancelled          bool
	wake               chan struct{}
	done               bool
	completed          chan struct{} // original runResident completion, separate from status
	registering        bool
	registrationSealed bool
	registrationDone   chan struct{}
	registrationErr    error
	registrationConn   *residentRegistrationConn
	command            []byte
}
type residentSession struct {
	gateway    *Gateway
	principal  *principal
	entry      *residentEntry
	generation string
}

// ConfigureResident is one-shot, before any MCP session or work. A principal
// has exactly one explicitly enrolled host here; ambiguous mappings refuse.
func (g *Gateway) ConfigureResident(endpoint string, control ResidentControl, entries []ResidentEnrollment) error {
	if g == nil || g.discoveryOnly || control == nil || len(entries) == 0 || len(entries) > MaxPrincipals || len(endpoint) > 512 {
		return ErrConfig
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || u.String() != endpoint {
		return ErrConfig
	}
	setup := &residentSetup{endpoint: endpoint, control: control, entries: map[*principal]*residentEntry{}}
	for _, e := range entries {
		p := g.byLabel[e.PrincipalLabel]
		key, err := ssh.ParsePublicKey(e.HostKey)
		if p == nil || p.signer == nil || !validLabel(e.HostLabel) || err != nil || key.Type() != ssh.KeyAlgoED25519 || setup.entries[p] != nil {
			return ErrConfig
		}
		e.HostKey = append([]byte(nil), e.HostKey...)
		setup.entries[p] = &residentEntry{enrollment: e, works: map[string]*residentWork{}}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.resident != nil || len(g.sessions) != 0 {
		return ErrRefused
	}
	g.resident = setup
	return nil
}
func (s *residentSession) FixedStart(ctx context.Context, id string) map[string]any {
	if ctx == nil || ctx.Err() != nil {
		return map[string]any{"error": "not_sent"}
	}
	g := s.gateway
	g.mu.Lock()
	if w := s.entry.works[id]; w != nil {
		if w.binding.Generation != s.generation {
			g.mu.Unlock()
			return map[string]any{"error": "request_generation_mismatch"}
		}
		v := residentValue(w)
		g.mu.Unlock()
		return v
	}
	if t, ok := s.entry.tombstones[id]; ok {
		g.mu.Unlock()
		return tombstoneValue(t, s.generation)
	}
	if g.residentRecordCountLocked() >= MaxResidentReplayRecords {
		g.mu.Unlock()
		return map[string]any{"error": "replay_ledger_full"}
	}
	if g.closed || !agentwire.ValidCommandID(id) || g.residentResultCountLocked() >= MaxResidentResults || s.entry.current != nil || s.principal.route != nil || s.principal.connecting {
		g.mu.Unlock()
		return map[string]any{"error": "busy_or_refused", "owner_cleanup": "unknown"}
	}
	if g.resident.control.ResidentGeneration(s.entry.enrollment.HostLabel, s.principal.label) != s.generation {
		g.mu.Unlock()
		return map[string]any{"error": "control_ended", "owner_cleanup": "unknown", "reinitialize_required": true}
	}
	deadline := time.Now().Add(15 * time.Second).Truncate(time.Millisecond)
	w := &residentWork{binding: ResidentWorkBinding{Host: s.entry.enrollment.HostLabel, Principal: s.principal.label, Generation: s.generation, RequestID: id, DeadlineMillis: deadline.UnixMilli()}, completed: make(chan struct{}), ready: make(chan struct{}), collected: make(chan struct{}), wake: make(chan struct{}, 1), phase: "pending", ownerCleanup: "pending", gatewayCleanup: "pending"}
	s.entry.works[id] = w
	s.entry.current = w
	g.watchers.Add(1)
	v := residentValue(w)
	g.mu.Unlock()
	go g.runResident(s.entry, w)
	return v
}

// RunCommand requests one command run. The scope it signs is derived only from
// its arguments; a request id names one scope for as long as its record lives.
func (s *residentSession) RunCommand(ctx context.Context, id, command, cwd string, timeoutMillis uint32) map[string]any {
	commandHash := sha256.Sum256([]byte(command))
	scope := ResidentRunScope{CommandSHA256: hex.EncodeToString(commandHash[:]), Cwd: cwd, TimeoutMillis: timeoutMillis}
	if ctx == nil || ctx.Err() != nil {
		return map[string]any{"error": "not_sent"}
	}
	if !agentwire.ValidCommandID(id) || len(command) == 0 || len(command) > agentwire.MaxCommandBytes || strings.ContainsRune(command, 0) || timeoutMillis < ResidentRunMinTimeoutMillis || !scope.WellFormed() {
		return map[string]any{"error": "invalid_run_scope"}
	}
	g := s.gateway
	g.mu.Lock()
	if w := s.entry.works[id]; w != nil {
		if w.binding.Generation != s.generation {
			g.mu.Unlock()
			return map[string]any{"error": "request_generation_mismatch"}
		}
		if w.binding.Run != scope {
			g.mu.Unlock()
			return map[string]any{"error": "request_scope_mismatch"}
		}
		v := residentValue(w)
		g.mu.Unlock()
		return v
	}
	if t, ok := s.entry.tombstones[id]; ok {
		g.mu.Unlock()
		if t.generation == s.generation && t.run != scope {
			return map[string]any{"error": "request_scope_mismatch"}
		}
		return tombstoneValue(t, s.generation)
	}
	if g.residentRecordCountLocked() >= MaxResidentReplayRecords {
		g.mu.Unlock()
		return map[string]any{"error": "replay_ledger_full"}
	}
	if g.closed || g.residentResultCountLocked() >= MaxResidentResults || s.entry.current != nil || s.principal.route != nil || s.principal.connecting {
		g.mu.Unlock()
		return map[string]any{"error": "busy_or_refused", "owner_cleanup": "unknown"}
	}
	if g.resident.control.ResidentGeneration(s.entry.enrollment.HostLabel, s.principal.label) != s.generation {
		g.mu.Unlock()
		return map[string]any{"error": "control_ended", "owner_cleanup": "unknown", "reinitialize_required": true}
	}
	deadline := time.Now().Add(time.Duration(timeoutMillis) * time.Millisecond).Truncate(time.Millisecond)
	w := &residentWork{binding: ResidentWorkBinding{Host: s.entry.enrollment.HostLabel, Principal: s.principal.label, Generation: s.generation, RequestID: id, DeadlineMillis: deadline.UnixMilli(), Run: scope}, command: []byte(command), completed: make(chan struct{}), ready: make(chan struct{}), collected: make(chan struct{}), wake: make(chan struct{}, 1), phase: "pending", ownerCleanup: "pending", gatewayCleanup: "pending"}
	s.entry.works[id] = w
	s.entry.current = w
	g.watchers.Add(1)
	v := residentValue(w)
	g.mu.Unlock()
	go g.runResident(s.entry, w)
	return v
}
func (s *residentSession) FixedStatus(_ context.Context, id string) map[string]any {
	g := s.gateway
	g.mu.Lock()
	defer g.mu.Unlock()
	if w := s.entry.works[id]; w != nil && w.binding.Generation == s.generation {
		return residentValue(w)
	}
	if t, ok := s.entry.tombstones[id]; ok && t.generation == s.generation {
		return tombstoneValue(t, s.generation)
	}
	return map[string]any{"error": "not_found"}
}
func (s *residentSession) FixedCancel(ctx context.Context, id string) map[string]any {
	g := s.gateway
	g.mu.Lock()
	w := s.entry.works[id]
	if w == nil || w.binding.Generation != s.generation {
		if t, ok := s.entry.tombstones[id]; ok && t.generation == s.generation {
			g.mu.Unlock()
			return tombstoneValue(t, s.generation)
		}
		g.mu.Unlock()
		return map[string]any{"error": "not_found"}
	}
	w.cancelled = true
	select {
	case w.wake <- struct{}{}:
	default:
	}
	v := residentValue(w)
	g.mu.Unlock()
	return v
}
func residentValue(w *residentWork) map[string]any {
	chunks := make([]map[string]any, len(w.output))
	for n, c := range w.output {
		copy := map[string]any{}
		for k, v := range c {
			copy[k] = v
		}
		chunks[n] = copy
	}
	exit := map[string]any{"known": w.status.Exit.Known}
	if w.status.Exit.Known {
		exit["code"] = w.status.Exit.Code
	}
	return map[string]any{"request_id": w.binding.RequestID, "phase": w.phase, "profile": agentwire.ProfileName(w.status.Profile), "command_id": w.status.CommandID, "command_phase": agentwire.PhaseName(w.status.Phase), "exit": exit, "stop": agentwire.StopName(w.status.Stop), "terminal": w.status.Terminal, "command_cleanup": agentwire.CleanupName(w.status.Cleanup), "owner_cleanup": w.ownerCleanup, "gateway_cleanup": w.gatewayCleanup, "chunks": chunks, "next_cursor": w.cursor, "error": w.failure}
}

// ConnectResident is called only after the normal registration's enrolled-host
// proof verified all correlation fields. It still opens the genuine SSH channel
// and Session; a matching outer ID alone cannot supply a grant.
func (g *Gateway) ConnectResident(label string, d Dispatch, b ResidentWorkBinding, conn net.Conn) error {
	if conn == nil {
		return ErrConfig
	}
	g.mu.Lock()
	var entry *residentEntry
	if g.resident != nil {
		entry = g.resident.entries[g.byLabel[label]]
	}
	var w *residentWork
	if entry != nil {
		w = entry.works[b.RequestID]
	}
	valid := !g.discoveryOnly && !g.closed && w != nil && !w.done && !w.registrationSealed && !w.registering && w.route == nil && (w.binding.Sequence == 0 || w.binding.Sequence == b.Sequence) && b.Principal == label && b.Host == entry.enrollment.HostLabel && b.Generation == w.binding.Generation && b.DeadlineMillis == w.binding.DeadlineMillis && b.Run == w.binding.Run && b.Sequence > 0 && b.Sequence <= 32 && bytes.Equal(d.HostKey, entry.enrollment.HostKey) && !d.Deadline.After(time.UnixMilli(b.DeadlineMillis)) && time.Now().UnixMilli() < b.DeadlineMillis
	if valid {
		w.binding.Sequence = b.Sequence
		w.registering = true
		w.registrationDone = make(chan struct{})
		w.registrationConn = &residentRegistrationConn{Conn: conn, done: make(chan struct{})}
		g.watchers.Add(1)
	}
	g.mu.Unlock()
	if !valid {
		_ = conn.Close()
		return ErrRefused
	}
	defer close(w.registrationDone)
	defer g.watchers.Done()
	rt, err := g.connect(label, d, w.registrationConn)
	if rt == nil && err != nil {
		if closed, closeErr := w.registrationConn.result(); !closed || closeErr != nil {
			err = errors.Join(err, agentchannel.ErrCleanupUnknown, closeErr)
		}
	}
	g.mu.Lock()
	w.registrationErr = err
	w.route = rt
	sealed := w.registrationSealed
	close(w.ready)
	g.mu.Unlock()
	if sealed && rt != nil {
		_ = rt.client.Close()
	}
	return err
}

// ResidentCollectionProof is signed evidence, never a caller-supplied cleanup
// bool. Gateway independently verifies the pinned host signature and complete
// original operation binding before publishing native-owner collection.
type ResidentCollectionProof struct {
	Binding                                                    ResidentWorkBinding
	Endpoint                                                   string
	ClientNonce, ServerNonce, HostKey, PrincipalKey, Signature []byte
	Cleanup                                                    string
}

func ResidentCollectionTranscript(p ResidentCollectionProof) []byte {
	var out []byte
	field := func(b []byte) { out = binary.BigEndian.AppendUint32(out, uint32(len(b))); out = append(out, b...) }
	b := p.Binding
	for _, v := range []string{"rescue-host.agenttransport.resident\x00v1\x00", "host-collected", p.Endpoint, b.Host, b.Principal, b.Generation} {
		field([]byte(v))
	}
	for _, v := range [][]byte{p.ClientNonce, p.ServerNonce, p.HostKey, p.PrincipalKey} {
		field(v)
	}
	// Fixed work keeps its original bytes; a command run names its own scope.
	operation, timeout, scope := ResidentFixedID, "15000", []string(nil)
	if b.Run != (ResidentRunScope{}) {
		operation, timeout, scope = ResidentRunID, strconv.FormatUint(uint64(b.Run.TimeoutMillis), 10), []string{b.Run.CommandSHA256, b.Run.Cwd}
	}
	for _, v := range append(append([]string{b.Generation, strconv.FormatUint(b.Sequence, 10), b.RequestID, operation, strconv.FormatInt(b.DeadlineMillis, 10), timeout}, scope...), p.Cleanup) {
		field([]byte(v))
	}
	return out
}
func (g *Gateway) ResidentCollected(proof ResidentCollectionProof) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.resident == nil {
		return ErrRefused
	}
	p := g.byLabel[proof.Binding.Principal]
	e := g.resident.entries[p]
	if e == nil {
		return ErrRefused
	}
	w := e.works[proof.Binding.RequestID]
	key, err := ssh.ParsePublicKey(e.enrollment.HostKey)
	b := proof.Binding
	if w == nil || w.collectionSeen || proof.Endpoint != g.resident.endpoint || b.Host != e.enrollment.HostLabel || b.Generation != w.binding.Generation || b.DeadlineMillis != w.binding.DeadlineMillis || b.Run != w.binding.Run || b.Sequence == 0 || b.Sequence > 32 || (w.binding.Sequence != 0 && b.Sequence != w.binding.Sequence) || len(proof.ClientNonce) != 32 || len(proof.ServerNonce) != 32 || !bytes.Equal(proof.HostKey, e.enrollment.HostKey) || !bytes.Equal(proof.PrincipalKey, p.signer.PublicKey().Marshal()) || err != nil || (proof.Cleanup != "confirmed" && proof.Cleanup != "unknown") || !validCollectionSignature(key, proof) {
		return ErrRefused
	}
	w.binding.Sequence = b.Sequence
	w.collectionSeen = true
	w.ownerCleanup = proof.Cleanup
	close(w.collected)
	// A proof that arrives after runResident stopped waiting settles the work
	// on the same condition. Its connection is gone, so nothing is rotated.
	if w.done && w.ownerCleanup == "confirmed" && w.gatewayCleanup == "confirmed" {
		w.phase = "finished"
		if e.current == w {
			e.current = nil
		}
	}
	return nil
}
func validCollectionSignature(key ssh.PublicKey, proof ResidentCollectionProof) bool {
	var sig ssh.Signature
	return ssh.Unmarshal(proof.Signature, &sig) == nil && sig.Format == ssh.KeyAlgoED25519 && len(sig.Blob) == 64 && len(sig.Rest) == 0 && key.Verify(ResidentCollectionTranscript(proof), &sig) == nil
}

// Scope/result limits apply independently of command timeout and grant expiry.
func (g *Gateway) runResident(entry *residentEntry, w *residentWork) {
	defer func() {
		// The command text is needed only to start the command once.
		g.mu.Lock()
		w.command = nil
		g.mu.Unlock()
		g.watchers.Done()
		close(w.completed)
	}()
	deadline := time.UnixMilli(w.binding.DeadlineMillis)
	operation, stop := context.WithDeadline(g.ctx, deadline)
	defer stop()
	var e error
	if w.binding.Run != (ResidentRunScope{}) {
		e = g.resident.control.BeginResidentRun(operation, w.binding.Host, w.binding.Principal, w.binding.Generation, w.binding.RequestID, deadline, w.binding.Run)
	} else {
		e = g.resident.control.BeginResidentFixed(operation, w.binding.Host, w.binding.Principal, w.binding.Generation, w.binding.RequestID, deadline)
	}
	if !errors.Is(e, ErrResidentPending) {
		g.mu.Lock()
		w.failure = "admission_refused_or_uncertain"
		if errors.Is(e, ErrResidentWorkUnconfigured) {
			if !w.collectionSeen {
				w.collectionSeen = true
				w.ownerCleanup = "confirmed"
				close(w.collected)
			}
		}
		g.mu.Unlock()
	}
	if errors.Is(e, ErrResidentPending) {
		select {
		case <-w.ready:
			g.executeResident(operation, w)
		case <-operation.Done():
			g.mu.Lock()
			w.failure = "registration_unobserved"
			g.mu.Unlock()
		}
	}
	g.mu.Lock()
	w.registrationSealed = true
	registrationDone := w.registrationDone
	g.mu.Unlock()
	gatewayCleanup := "confirmed"
	if registrationDone != nil {
		join, release := context.WithTimeout(context.Background(), 10*time.Second)
		select {
		case <-registrationDone:
		case <-join.Done():
			gatewayCleanup = "unknown"
		}
		release()
	}
	g.mu.Lock()
	rt := w.route
	registrationErr := w.registrationErr
	g.mu.Unlock()
	if errors.Is(registrationErr, agentchannel.ErrCleanupUnknown) {
		gatewayCleanup = "unknown"
	}
	if rt != nil {
		_ = rt.client.Close()
		cleanup, release := context.WithTimeout(context.Background(), 10*time.Second)
		select {
		case <-rt.cleanupDone:
			if rt.cleanupErr != nil {
				gatewayCleanup = "unknown"
			}
		case <-cleanup.Done():
			gatewayCleanup = "unknown"
		}
		release()
	}
	// Cleanup observation may arrive after finite operation expiry; it never
	// extends work authority or reconstructs/starts a command.
	collected, closeWait := context.WithTimeout(g.ctx, 20*time.Second)
	defer closeWait()
	select {
	case <-w.collected:
	case <-collected.Done():
	}
	g.mu.Lock()
	w.gatewayCleanup = gatewayCleanup
	if !w.collectionSeen {
		w.ownerCleanup = "unknown"
	}
	w.done = true
	if w.ownerCleanup == "confirmed" && gatewayCleanup == "confirmed" {
		w.phase = "finished"
		if entry.current == w {
			entry.current = nil
		}
	} else {
		w.phase = "unknown"
	}
	rotate := g.residentGenerationCollectedLocked(entry, w.binding.Generation)
	g.mu.Unlock()
	if rotate {
		if control, ok := g.resident.control.(interface {
			RotateResident(string, string, string, string)
		}); ok {
			control.RotateResident(w.binding.Host, w.binding.Principal, w.binding.Generation, w.binding.RequestID)
		}
	}
}
func (g *Gateway) executeResident(ctx context.Context, w *residentWork) {
	g.mu.Lock()
	rt := w.route
	cancelled := w.cancelled
	g.mu.Unlock()
	if rt == nil {
		return
	}
	if cancelled {
		return
	}
	command, commandID, timeout := []byte(ResidentFixedCommand), ResidentFixedID, uint32(15000)
	if w.binding.Run != (ResidentRunScope{}) {
		command, commandID, timeout = w.command, ResidentRunID, w.binding.Run.TimeoutMillis
	}
	status, err := rt.client.Start(ctx, commandID, command, timeout)
	if err != nil {
		g.mu.Lock()
		w.failure = "start_uncertain"
		g.mu.Unlock()
		return
	}
	g.mu.Lock()
	w.status = status
	w.phase = "running"
	g.mu.Unlock()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	cancelSent := false
	for {
		g.mu.Lock()
		cancel := w.cancelled
		cursor := w.cursor
		g.mu.Unlock()
		if cancel && !cancelSent {
			_, err = rt.client.Cancel(ctx, status.CommandID)
			cancelSent = true
			if err != nil {
				break
			}
		}
		output, e := rt.client.Output(ctx, status.CommandID, cursor, 4096)
		if e != nil {
			err = e
			break
		}
		g.mu.Lock()
		for _, c := range output.Chunks {
			if w.outputBytes+len(c.Data) > 16384 || len(w.output) >= 128 {
				w.failure = "output_limit"
				break
			}
			w.outputBytes += len(c.Data)
			w.output = append(w.output, map[string]any{"stream": agentwire.StreamName(c.Stream), "offset": c.Offset, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(c.Data)})
		}
		w.cursor = output.Next
		g.mu.Unlock()
		status, err = rt.client.Status(ctx, status.CommandID)
		if err != nil {
			break
		}
		g.mu.Lock()
		w.status = status
		g.mu.Unlock()
		if status.Cleanup == agentexec.CleanupUnknown {
			err = agentexec.ErrCleanupUnknown
			break
		}
		if status.Terminal && status.Cleanup == agentexec.CleanupConfirmed && output.Complete {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-tick.C:
		case <-w.wake:
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		g.mu.Lock()
		w.failure = "result_unobserved_or_uncertain"
		g.mu.Unlock()
	}
}

// residentRegistrationConn observes ONE original raw close, including pre-open
// refusals. It never retries a failed Close or turns an absent route into proof.
type residentRegistrationConn struct {
	net.Conn
	once     sync.Once
	done     chan struct{}
	closeErr error
}

func (c *residentRegistrationConn) Close() error {
	c.once.Do(func() { c.closeErr = c.Conn.Close(); close(c.done) })
	<-c.done
	return c.closeErr
}
func (c *residentRegistrationConn) result() (bool, error) {
	select {
	case <-c.done:
		return true, c.closeErr
	default:
		return false, nil
	}
}
