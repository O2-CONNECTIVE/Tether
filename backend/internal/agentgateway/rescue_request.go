package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"golang.org/x/crypto/ssh"
)

// See docs/rescue-connection-start.md, 2. 身元の渡し方 and 3. 許可と拒否.
const (
	// RescueRequestLifetime is how long the gateway waits for the host's
	// answer: the 60 second popup and 15 seconds for the network.
	RescueRequestLifetime = 75 * time.Second
	// RescueProfile names the operation scope in the identity proof.
	RescueProfile = "mac_current_user"
	RescueAllow   = "allow"
	RescueDeny    = "deny"
	RescueTimeout = "timeout"
)

// RescueIdentity is the identity proof the gateway sends on the reception.
// The host already knows the endpoint, the reception id and K.
type RescueIdentity struct {
	Nonce        []byte // RescueNonceSize, fresh for this request
	Client       []byte // C, SSH wire form of a raw Ed25519 key
	DisplayName  string // from the gateway configuration, never a token claim
	Organization string
	Profile      string // RescueProfile
	Signature    []byte // raw ssh-ed25519 signature by the identity key over RescueRequestTranscript
}

// RescueAnswer is the host's answer as the reception route read it. Dispatch
// is read only for RescueAllow.
type RescueAnswer struct {
	Answer               string
	Dispatch             Dispatch
	MaxLifetimeAllowance time.Duration // trusted transport policy; not encoded or signed
	Signature            []byte        // raw ssh-ed25519 signature by K over RescueAnswerTranscript
}

// RescueRequestTranscript is what the identity key signs for one request:
// the domain, the public reception URL, the reception id, K, the request
// nonce, C, the display name, the organization and the profile, each
// length-prefixed as in RescueTranscript. Its SHA-256 is the digest the
// answer carries.
func RescueRequestTranscript(endpoint string, reception, hostKey, nonce, client []byte, displayName, organization, profile string) []byte {
	return rescueTranscript([][]byte{[]byte("rescue-host.agentgateway.rescue-request\x00v1\x00"), []byte(endpoint), reception, hostKey, nonce, client,
		[]byte(displayName), []byte(organization), []byte(profile)})
}

// RescueAnswerTranscript is what K signs: the domain, the reception id, the
// request nonce, the identity proof digest and the answer, and for
// RescueAllow only the dispatch fields in the order and encoding of the
// registration transcript.
func RescueAnswerTranscript(reception, nonce, digest []byte, answer string, d Dispatch) []byte {
	parts := [][]byte{[]byte("rescue-host.agentgateway.rescue-answer\x00v1\x00"), reception, nonce, digest, []byte(answer)}
	if answer == RescueAllow {
		b := d.Binding
		parts = append(parts, binary.BigEndian.AppendUint64(nil, b.Grant.Generation), binary.BigEndian.AppendUint64(nil, b.ConnectionGeneration), []byte(b.Grant.Identity.LaunchID),
			[]byte(b.Grant.Identity.Epoch), []byte(b.Grant.Identity.PolicyDigest), b.KeySetDigest[:], []byte{byte(b.Profile)},
			binary.BigEndian.AppendUint64(nil, uint64(d.Deadline.UnixNano())), d.HostKey)
	}
	return rescueTranscript(parts)
}

// AnswerRescue verifies a host answer and opens its approved channel; see
// docs/rescue-connection-start.md, 3. 許可と拒否.
func (g *Gateway) AnswerRescue(id []byte, attemptID uint64, a RescueAnswer, conn net.Conn, onActivity func()) error {
	if conn == nil {
		return ErrRefused
	}
	taken := false
	defer func() {
		if !taken {
			_ = conn.Close()
		}
	}()
	if g == nil || g.rescue == nil {
		return ErrRefused
	}
	g.rescue.mu.Lock()
	record := g.rescue.records[string(id)]
	if record == nil || record.attemptID != attemptID || record.principal == nil || !record.awaitingAnswer {
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	if record.resuming && rescueGraceExpired(record, g.clock()) {
		g.endRescueLocked(id, record)
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	// A transport-rejected answer is empty. Expiry takes precedence and records
	// timeout; while live, signature verification below records failed at once.
	if rescueClockTime(g.clock()).UnixNano() >= record.requested.Add(RescueRequestLifetime).UnixNano() {
		if record.resuming {
			record.awaitingAnswer = false
			record.resuming = false
			record.disconnected = true
			closeRescueAttemptLocked(record)
			select {
			case <-record.request:
			default:
			}
		} else {
			g.setRescueStateLocked(record.principal, RescueTimeout)
			g.endRescueLocked(id, record)
		}
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	key, err := ssh.ParsePublicKey(record.key)
	if err != nil || key.Verify(RescueAnswerTranscript(id, record.nonce, record.digest, a.Answer, a.Dispatch), &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: a.Signature}) != nil {
		g.setRescueStateLocked(record.principal, "failed")
		g.endRescueLocked(id, record)
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	if a.Answer != RescueAllow && a.Answer != RescueDeny && a.Answer != RescueTimeout {
		g.setRescueStateLocked(record.principal, "failed")
		g.endRescueLocked(id, record)
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	if a.Answer == RescueDeny || a.Answer == RescueTimeout {
		g.setRescueStateLocked(record.principal, a.Answer)
		g.endRescueLocked(id, record)
		g.rescue.mu.Unlock()
		return ErrRefused // nil only when a channel opens
	}
	clientSigner, clientKey, err := newRescueClientSigner(record.clientKey)
	defer clearRescueClientKey(clientKey)
	var keyDigest [32]byte
	if err == nil {
		keyDigest, err = runbackend.KeySetDigest([]ssh.PublicKey{clientSigner.PublicKey()})
	}
	if err != nil || !bytes.Equal(a.Dispatch.HostKey, record.key) || a.Dispatch.Binding.KeySetDigest != keyDigest {
		g.setRescueStateLocked(record.principal, "failed")
		g.endRescueLocked(id, record)
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	wantGeneration := uint64(1)
	if record.answered {
		if record.connectionGeneration == ^uint64(0) {
			g.setRescueStateLocked(record.principal, "failed")
			g.endRescueLocked(id, record)
			g.rescue.mu.Unlock()
			return ErrRefused
		}
		wantGeneration = record.connectionGeneration + 1
		if !sameRescueDispatch(record.dispatch, a.Dispatch) {
			g.setRescueStateLocked(record.principal, "failed")
			g.endRescueLocked(id, record)
			g.rescue.mu.Unlock()
			return ErrRefused
		}
	}
	if a.Dispatch.Binding.ConnectionGeneration != wantGeneration {
		g.setRescueStateLocked(record.principal, "failed")
		g.endRescueLocked(id, record)
		g.rescue.mu.Unlock()
		return ErrRefused
	}
	record.dispatch = cloneRescueDispatchValue(a.Dispatch)
	record.awaitingAnswer = false
	record.opening = true
	principal := record.principal
	g.rescue.mu.Unlock()
	taken = true
	newRoute, err := g.connectWithSignerForRescue(principal.label, a.Dispatch, conn, true, clientSigner, true,
		&rescueRoutePublication{id: string(id), attemptID: attemptID, record: record}, onActivity, a.MaxLifetimeAllowance)
	g.rescue.mu.Lock()
	if g.rescue.records[string(id)] == record {
		if err != nil {
			g.setRescueStateLocked(principal, "failed")
			g.endRescueLocked(id, record)
		}
	} else if newRoute != nil {
		go g.closeChannel(newRoute.channel)
	}
	g.rescue.mu.Unlock()
	return err
}

func sameRescueDispatch(previous, next Dispatch) bool {
	return previous.Binding.Grant == next.Binding.Grant && previous.Binding.KeySetDigest == next.Binding.KeySetDigest &&
		previous.Binding.Profile == next.Binding.Profile && previous.Deadline.Equal(next.Deadline) &&
		bytes.Equal(previous.HostKey, next.HostKey)
}

func cloneRescueDispatchValue(d Dispatch) Dispatch {
	d.HostKey = append([]byte(nil), d.HostKey...)
	return d
}

func newRescueClientSigner(key ed25519.PrivateKey) (ssh.Signer, ed25519.PrivateKey, error) {
	owned := append(ed25519.PrivateKey(nil), key...)
	signer, err := ssh.NewSignerFromKey(owned)
	if err != nil {
		clearRescueClientKey(owned)
		return nil, nil, err
	}
	return signer, owned, nil
}

func (g *Gateway) rescueRequest(ctx context.Context, p *principal, code string) map[string]any {
	const (
		invalidCode     = "the rescue code is not valid"
		busyMessage     = "a rescue request or connection is already active for this principal"
		roleMessage     = "this principal may not request a rescue"
		identityMessage = "the gateway has no display_name or organization for this principal"
		unavailable     = "rescue is not available"
	)
	if ctx.Err() != nil || g == nil || p == nil || g.rescue == nil {
		return map[string]any{"error": unavailable}
	}
	g.rescue.mu.Lock()
	g.mu.Lock()
	switch {
	case g.closed || g.rescueSigner == nil || g.discoveryOnly || g.residentOnly:
		g.mu.Unlock()
		g.rescue.mu.Unlock()
		return map[string]any{"error": unavailable}
	case !p.rescue:
		g.mu.Unlock()
		g.rescue.mu.Unlock()
		return map[string]any{"error": roleMessage}
	case p.displayName == "" || g.organization == "":
		g.mu.Unlock()
		g.rescue.mu.Unlock()
		return map[string]any{"error": identityMessage}
	case p.rescueBusy || p.route != nil || p.connecting:
		g.mu.Unlock()
		g.rescue.mu.Unlock()
		return map[string]any{"error": busyMessage}
	default:
		p.rescueBusy = true
	}
	g.mu.Unlock()
	receptionHex, err := g.rescueCodes.redeem(p, code)
	if err != nil {
		g.clearRescueBusyLocked(p)
		g.rescue.mu.Unlock()
		if errors.Is(err, errRescueLocked) {
			return map[string]any{"error": "too many invalid rescue codes; try again in 10 minutes"}
		}
		return map[string]any{"error": invalidCode}
	}
	id, err := hex.DecodeString(receptionHex)
	record := g.rescue.records[string(id)]
	if err != nil || record == nil {
		g.clearRescueBusyLocked(p)
		g.rescue.mu.Unlock()
		return map[string]any{"error": invalidCode}
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err == nil {
		var nonce [RescueNonceSize]byte
		_, err = rand.Read(nonce[:])
		if err == nil {
			signer, signerErr := ssh.NewSignerFromKey(private)
			err = signerErr
			if err == nil {
				client := signer.PublicKey().Marshal()
				transcript := RescueRequestTranscript(g.RescueEndpoint(), id, record.key, nonce[:], client, p.displayName, g.organization, RescueProfile)
				signature, signErr := g.rescueSigner.Sign(rand.Reader, transcript)
				err = signErr
				if err == nil && signature != nil && signature.Format == ssh.KeyAlgoED25519 {
					digest := sha256.Sum256(transcript)
					record.principal, record.clientKey, record.nonce = p, private, append([]byte(nil), nonce[:]...)
					record.digest, record.requested = append([]byte(nil), digest[:]...), rescueClockTime(g.clock())
					payload := RescueIdentity{Nonce: append([]byte(nil), nonce[:]...), Client: append([]byte(nil), client...), DisplayName: p.displayName,
						Organization: g.organization, Profile: RescueProfile, Signature: append([]byte(nil), signature.Blob...)}
					select {
					case record.request <- payload:
						record.awaitingAnswer = true
						record.resuming = false
						record.disconnected = false
						g.setRescueStateLocked(p, "pending")
						g.rescue.mu.Unlock()
						return map[string]any{"state": "pending"}
					default:
						err = ErrRefused
					}
				} else if err == nil {
					err = ErrRefused
				}
			}
		}
	}
	clear(private)
	g.clearRescueBusyLocked(p)
	g.setRescueStateLocked(p, "failed")
	g.endRescueLocked(id, record)
	g.rescue.mu.Unlock()
	return map[string]any{"error": unavailable}
}

func (g *Gateway) clearRescueBusyLocked(p *principal) {
	g.mu.Lock()
	p.rescueBusy = false
	g.mu.Unlock()
}

func (g *Gateway) rescueRequestStatus(ctx context.Context, p *principal) map[string]any {
	if g == nil || p == nil || g.rescue == nil {
		return map[string]any{"state": "none"}
	}
	for {
		g.rescue.mu.Lock()
		state, changed := p.rescueState, p.rescueChanged
		g.rescue.mu.Unlock()
		if state != "pending" {
			return map[string]any{"state": state}
		}
		select {
		case <-ctx.Done():
			return map[string]any{"state": "pending"}
		case <-changed:
		}
	}
}

type rescueMCPBackend struct {
	gateway   *Gateway
	principal *principal
}

func (b rescueMCPBackend) RescueRequest(ctx context.Context, code string) map[string]any {
	return b.gateway.rescueRequest(ctx, b.principal, code)
}

func (b rescueMCPBackend) RescueRequestStatus(ctx context.Context) map[string]any {
	return b.gateway.rescueRequestStatus(ctx, b.principal)
}
