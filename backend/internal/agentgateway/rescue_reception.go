package agentgateway

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// The rescue reception (docs/rescue-connection-start.md, 受付の登録 and
// 回数の制限): a host without an account registers a reception with its
// per-reception key K and gets a code that the gateway signs with the rescue
// identity key.
const (
	RescuePath           = "/tether-agent/v1/rescue"
	RescuePurposeNew     = "new"
	RescuePurposeResume  = "resume"
	RescueNonceSize      = 32
	RescueReceptionSize  = 16 // 128 bit id
	RescueReconnectGrace = 5 * time.Minute

	// RescueCodeLifetime is how long a code is valid; the host ends its
	// reception when it passes.
	RescueCodeLifetime = rescueCodeLifetime

	maxRescueReceptions = 1000 // records, codes or not
	rescueAdmitLimit    = 30   // admitted reception upgrades per window, gateway wide
	rescueAdmitWindow   = time.Minute
)

var (
	// ErrRescueBusy is answered as 503: maxRescueCodes valid codes or
	// maxRescueReceptions records.
	ErrRescueBusy = errors.New("agentgateway: rescue reception is full")
	// ErrRescueRate is answered as 429: rescueAdmitLimit admissions in the
	// last rescueAdmitWindow.
	ErrRescueRate = errors.New("agentgateway: too many new rescue receptions")
)

// RescueRequest is one host's registration as the reception route read it.
// GatewayNonce is the nonce this connection sent; the rest come from the host.
type RescueRequest struct {
	Key          []byte // K, SSH wire form of a raw Ed25519 key
	HostNonce    []byte // RescueNonceSize
	GatewayNonce []byte // RescueNonceSize
	Purpose      string // RescuePurposeNew or RescuePurposeResume
	Reception    []byte // RescueReceptionSize, only for RescuePurposeResume
	Proof        []byte // raw ssh-ed25519 signature by K over RescueTranscript
}

// RescueReception is the signed result the route returns to the host.
type RescueReception struct {
	ID        []byte // RescueReceptionSize
	Code      string
	Signature []byte // raw ssh-ed25519 signature by the identity key over RescueResultTranscript
	Request   <-chan RescueIdentity
	Done      <-chan struct{}
}

// RescueTranscript is what K signs: the domain, the public reception URL, K,
// the gateway nonce, the purpose and, for resume only, the reception id, each
// length-prefixed as in PairTranscript.
func RescueTranscript(endpoint string, key, gatewayNonce []byte, purpose string, reception []byte) []byte {
	parts := [][]byte{[]byte("rescue-host.agentgateway.rescue-reception\x00v1\x00"), []byte(endpoint), key, gatewayNonce, []byte(purpose)}
	if purpose == RescuePurposeResume {
		parts = append(parts, reception)
	}
	return rescueTranscript(parts)
}

// RescueResultTranscript is what the identity key signs: the domain, the
// public reception URL, the host nonce, K, the reception id and the code.
func RescueResultTranscript(endpoint string, hostNonce, key, reception []byte, code string) []byte {
	return rescueTranscript([][]byte{[]byte("rescue-host.agentgateway.rescue-reception-result\x00v1\x00"), []byte(endpoint), hostNonce, key, reception, []byte(code)})
}

func rescueTranscript(parts [][]byte) []byte {
	var out bytes.Buffer
	for _, part := range parts {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(part)))
		_, _ = out.Write(part)
	}
	return out.Bytes()
}

type rescueReceptionRecord struct {
	key                  []byte
	opened               time.Time
	requested            time.Time
	lastFrame            time.Time
	principal            *principal
	clientKey            ed25519.PrivateKey
	nonce                []byte
	request              chan RescueIdentity
	done                 chan struct{}
	digest               []byte
	dispatch             Dispatch
	connectionGeneration uint64
	route                *route
	answered             bool
	awaitingAnswer       bool
	resuming             bool
	opening              bool
	disconnected         bool
}

type rescueReceptionState struct {
	mu       sync.Mutex
	endpoint string
	records  map[string]*rescueReceptionRecord
	admits   []time.Time
}

func rescueClockTime(t time.Time) time.Time {
	return time.Unix(0, t.UnixNano())
}

// RescueEndpoint is the public reception URL, built from the configured
// resource origin and never from a request.
func (g *Gateway) RescueEndpoint() string {
	if g == nil || g.rescue == nil {
		return ""
	}
	return g.rescue.endpoint
}

// AdmitRescue runs before the upgrade.
func (g *Gateway) AdmitRescue() error {
	if g == nil || g.rescueSigner == nil || g.rescue == nil {
		return ErrRefused
	}
	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
	if err := g.rescueCapacityLocked(); err != nil {
		return err
	}
	now := g.clock()
	kept := g.rescue.admits[:0]
	for _, at := range g.rescue.admits {
		if now.Sub(at) < rescueAdmitWindow {
			kept = append(kept, at)
		}
	}
	g.rescue.admits = kept
	if len(kept) >= rescueAdmitLimit {
		return ErrRescueRate
	}
	g.rescue.admits = append(g.rescue.admits, now)
	return nil
}

// OpenRescue verifies the proof, records the reception and issues its code.
func (g *Gateway) OpenRescue(r RescueRequest) (RescueReception, error) {
	if g == nil || g.rescueSigner == nil || g.rescue == nil {
		return RescueReception{}, ErrRefused
	}
	key, err := ssh.ParsePublicKey(r.Key)
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(key.Marshal(), r.Key) || len(r.HostNonce) != RescueNonceSize || len(r.GatewayNonce) != RescueNonceSize || len(r.Proof) != 64 {
		return RescueReception{}, ErrRefused
	}
	if hasSmallOrderRescueKey(key) {
		return RescueReception{}, ErrRefused
	}
	switch r.Purpose {
	case RescuePurposeNew:
		if len(r.Reception) != 0 {
			return RescueReception{}, ErrRefused
		}
	case RescuePurposeResume:
		if len(r.Reception) != RescueReceptionSize {
			return RescueReception{}, ErrRefused
		}
	default:
		return RescueReception{}, ErrRefused
	}
	if key.Verify(RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, r.Reception), &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: r.Proof}) != nil {
		return RescueReception{}, ErrRefused
	}
	resumeAt := time.Time{}
	if r.Purpose == RescuePurposeResume {
		resumeAt = g.clock()
		if g.waitRescueRouteCleanup(r.Reception, r.Key, resumeAt) != nil {
			return RescueReception{}, ErrRefused
		}
	}
	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
	if r.Purpose == RescuePurposeResume {
		return g.resumeRescueLocked(r, resumeAt)
	}
	if err := g.rescueCapacityLocked(); err != nil {
		return RescueReception{}, err
	}
	var id [RescueReceptionSize]byte
	for {
		if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
			return RescueReception{}, ErrRefused
		}
		if _, exists := g.rescue.records[string(id[:])]; !exists {
			break
		}
	}
	idHex := hex.EncodeToString(id[:])
	code, err := g.rescueCodes.issue(idHex)
	if errors.Is(err, errRescueCodesFull) {
		return RescueReception{}, ErrRescueBusy
	}
	if err != nil {
		return RescueReception{}, ErrRefused
	}
	now := g.clock()
	record := &rescueReceptionRecord{key: append([]byte(nil), r.Key...), opened: rescueClockTime(now), lastFrame: now, request: make(chan RescueIdentity, 1), done: make(chan struct{})}
	g.rescue.records[string(id[:])] = record
	sig, err := g.rescueSigner.Sign(rand.Reader, RescueResultTranscript(g.rescue.endpoint, r.HostNonce, r.Key, id[:], code))
	if err != nil || sig == nil || sig.Format != ssh.KeyAlgoED25519 {
		delete(g.rescue.records, string(id[:]))
		g.rescueCodes.end(idHex)
		return RescueReception{}, ErrRefused
	}
	return RescueReception{ID: append([]byte(nil), id[:]...), Code: code, Signature: append([]byte(nil), sig.Blob...), Request: record.request, Done: record.done}, nil
}

// resumeRescueLocked reuses the reception's C and owner after a valid K proof.
// g.rescue.mu is held for the whole transition, including the new request.
func (g *Gateway) waitRescueRouteCleanup(id, hostKey []byte, now time.Time) error {
	g.rescue.mu.Lock()
	record := g.rescue.records[string(id)]
	if record == nil || !bytes.Equal(record.key, hostKey) || record.principal == nil || !record.answered ||
		!record.disconnected || record.awaitingAnswer || record.opening || record.clientKey == nil {
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	if record.lastFrame.IsZero() || now.Sub(record.lastFrame) >= RescueReconnectGrace {
		g.endRescueLocked(id, record)
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	if record.route == nil {
		g.rescue.mu.Unlock()
		return nil
	}
	done := record.route.cleanupDone
	select {
	case <-done:
		g.rescue.mu.Unlock()
		return nil
	default:
	}
	record.lastFrame = now
	record.disconnected = false
	record.resuming = true
	g.rescue.mu.Unlock()
	timeout := g.budgets.Cleanup
	if timeout <= 0 {
		timeout = time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	case <-g.ctx.Done():
	}
	g.rescue.mu.Lock()
	if g.rescue.records[string(id)] == record && record.resuming && !record.awaitingAnswer {
		record.resuming = false
		record.disconnected = true
	}
	g.rescue.mu.Unlock()
	return ErrRefused
}

func (g *Gateway) resumeRescueLocked(r RescueRequest, now time.Time) (RescueReception, error) {
	key := string(r.Reception)
	record := g.rescue.records[key]
	reserved := record != nil && record.resuming && !record.disconnected && !record.awaitingAnswer
	if record == nil || !bytes.Equal(record.key, r.Key) || record.principal == nil || !record.answered ||
		(!record.disconnected && !reserved) || record.awaitingAnswer || record.opening || record.clientKey == nil {
		return RescueReception{}, ErrRefused
	}
	if record.lastFrame.IsZero() || now.Sub(record.lastFrame) >= RescueReconnectGrace {
		g.endRescueLocked(r.Reception, record)
		return RescueReception{}, ErrRefused
	}
	record.lastFrame = now
	clientSigner, clientKey, err := newRescueClientSigner(record.clientKey)
	if err != nil {
		return RescueReception{}, ErrRefused
	}
	defer clearRescueClientKey(clientKey)
	nonce, err := randomRescueNonce()
	if err != nil {
		return RescueReception{}, ErrRefused
	}
	client := clientSigner.PublicKey().Marshal()
	transcript := RescueRequestTranscript(g.RescueEndpoint(), r.Reception, record.key, nonce, client,
		record.principal.displayName, g.organization, RescueProfile)
	signature, err := g.rescueSigner.Sign(rand.Reader, transcript)
	if err != nil || signature == nil || signature.Format != ssh.KeyAlgoED25519 {
		clear(transcript)
		clear(nonce)
		return RescueReception{}, ErrRefused
	}
	digest := sha256.Sum256(transcript)
	clear(transcript)
	payload := RescueIdentity{Nonce: append([]byte(nil), nonce...), Client: append([]byte(nil), client...),
		DisplayName: record.principal.displayName, Organization: g.organization, Profile: RescueProfile,
		Signature: append([]byte(nil), signature.Blob...)}
	result, err := g.rescueSigner.Sign(rand.Reader, RescueResultTranscript(g.rescue.endpoint, r.HostNonce, r.Key, r.Reception, ""))
	if err != nil || result == nil || result.Format != ssh.KeyAlgoED25519 {
		clear(nonce)
		return RescueReception{}, ErrRefused
	}
	select {
	case record.request <- payload:
	default:
		clear(nonce)
		return RescueReception{}, ErrRefused
	}
	record.nonce = append(record.nonce[:0], nonce...)
	record.digest = append(record.digest[:0], digest[:]...)
	record.requested = rescueClockTime(g.clock())
	record.awaitingAnswer = true
	record.resuming = true
	record.disconnected = false
	clear(nonce)
	return RescueReception{ID: append([]byte(nil), r.Reception...), Signature: append([]byte(nil), result.Blob...), Request: record.request, Done: record.done}, nil
}

func randomRescueNonce() ([]byte, error) {
	nonce := make([]byte, RescueNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		clear(nonce)
		return nil, ErrRefused
	}
	return nonce, nil
}

// hasSmallOrderRescueKey reports whether K is one of the eight torsion points
// of edwards25519, for which anyone can forge a proof that crypto/ed25519
// accepts. They are exactly the points with y in {0, 1, p-1, y8, p-y8}; y is
// reduced mod p and x's sign bit is ignored, so the non-canonical encodings Go
// decodes are caught too. A key that is not a raw Ed25519 key counts as small
// order. See docs/rescue-connection-start.md, 受付の登録 (#425).
func hasSmallOrderRescueKey(key ssh.PublicKey) bool {
	ck, ok := key.(ssh.CryptoPublicKey)
	if !ok {
		return true
	}
	raw, ok := ck.CryptoPublicKey().(ed25519.PublicKey)
	if !ok || len(raw) != ed25519.PublicKeySize {
		return true
	}
	be := slices.Clone(raw)
	be[len(be)-1] &= 0x7f
	slices.Reverse(be)
	p := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	y := new(big.Int).Mod(new(big.Int).SetBytes(be), p)
	y8, _ := new(big.Int).SetString("2707385501144840649318225287225658788936804267575313519463743609750303402022", 10)
	for _, small := range []*big.Int{big.NewInt(0), big.NewInt(1), new(big.Int).Sub(p, big.NewInt(1)), y8, new(big.Int).Sub(p, y8)} {
		if y.Cmp(small) == 0 {
			return true
		}
	}
	return false
}

// EndRescue drops the reception and its code at once.
func (g *Gateway) EndRescue(id []byte) {
	if g == nil || g.rescue == nil {
		return
	}
	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
	if record := g.rescue.records[string(id)]; record == nil {
		return
	} else {
		g.endRescueLocked(id, record)
	}
}

// TouchRescue records a valid frame from the host reception's monotonic clock.
func (g *Gateway) TouchRescue(id []byte) {
	if g == nil || g.rescue == nil {
		return
	}
	g.rescue.mu.Lock()
	if record := g.rescue.records[string(id)]; record != nil {
		record.lastFrame = g.clock()
	}
	g.rescue.mu.Unlock()
}

// DisconnectRescue preserves an answered reception for its reconnect grace.
// Before the first allow, a transport loss still ends the reception at once.
func (g *Gateway) DisconnectRescue(id []byte) {
	if g == nil || g.rescue == nil {
		return
	}
	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
	record := g.rescue.records[string(id)]
	if record == nil {
		return
	}
	if !record.answered || record.opening {
		g.endRescueLocked(id, record)
		return
	}
	record.disconnected = true
	if record.resuming {
		record.awaitingAnswer = false
		record.resuming = false
		select {
		case <-record.request:
		default:
		}
	}
}

func (g *Gateway) endRescueLocked(id []byte, record *rescueReceptionRecord) {
	key := string(id)
	if g.rescue.records[key] != record {
		return
	}
	delete(g.rescue.records, key)
	g.rescueCodes.end(hex.EncodeToString(id))
	if record.principal != nil {
		if record.principal.rescueState == "pending" {
			g.setRescueStateLocked(record.principal, "ended")
		}
		g.mu.Lock()
		record.principal.rescueBusy = false
		g.mu.Unlock()
	}
	clearRescueClientKey(record.clientKey)
	record.clientKey = nil
	close(record.done)
}

func clearRescueClientKey(key ed25519.PrivateKey) {
	for i := range key {
		key[i] = 0
	}
}

func (g *Gateway) setRescueStateLocked(p *principal, state string) {
	if p.rescueState == state {
		return
	}
	p.rescueState = state
	close(p.rescueChanged)
	p.rescueChanged = make(chan struct{})
}

func (g *Gateway) sweepRescue() {
	defer g.watchers.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
			g.sweepRescueAt(g.clock())
		}
	}
}

func (g *Gateway) sweepRescueAt(now time.Time) {
	if g == nil || g.rescue == nil {
		return
	}
	wallNow := rescueClockTime(now)
	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
	for id, record := range g.rescue.records {
		switch {
		case record.principal == nil && wallNow.UnixNano() >= record.opened.Add(RescueCodeLifetime).UnixNano():
			g.endRescueLocked([]byte(id), record)
		case record.principal != nil && record.awaitingAnswer && wallNow.UnixNano() >= record.requested.Add(RescueRequestLifetime).UnixNano():
			if record.resuming {
				record.awaitingAnswer = false
				record.resuming = false
				record.disconnected = true
				select {
				case <-record.request:
				default:
				}
			} else {
				g.setRescueStateLocked(record.principal, RescueTimeout)
				g.endRescueLocked([]byte(id), record)
			}
		case record.answered && record.disconnected && !record.awaitingAnswer && !record.lastFrame.IsZero() && now.Sub(record.lastFrame) >= RescueReconnectGrace:
			g.endRescueLocked([]byte(id), record)
		}
	}
}

func (g *Gateway) rescueCapacityLocked() error {
	g.mu.Lock() // order: rescue.mu, then g.mu; nothing takes them the other way
	closed := g.closed
	g.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if len(g.rescue.records) >= maxRescueReceptions || g.rescueCodes.liveCount() >= maxRescueCodes {
		return ErrRescueBusy
	}
	return nil
}
