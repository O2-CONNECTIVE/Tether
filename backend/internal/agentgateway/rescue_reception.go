package agentgateway

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// The rescue reception (docs/rescue-connection-start.md, 受付の登録 and
// 回数の制限): a host without an account registers a reception with its
// per-reception key K and gets a code that the gateway signs with the rescue
// identity key.
const (
	RescuePath          = "/tether-agent/v1/rescue"
	RescuePurposeNew    = "new"
	RescuePurposeResume = "resume" // the wire only; #425 resumes
	RescueNonceSize     = 32
	RescueReceptionSize = 16 // 128 bit id

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

type rescueReceptionRecord struct{ key []byte }

type rescueReceptionState struct {
	mu       sync.Mutex
	endpoint string
	records  map[string]rescueReceptionRecord
	admits   []time.Time
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
	if r.Purpose == RescuePurposeResume {
		return RescueReception{}, ErrRefused
	}

	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
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
	g.rescue.records[string(id[:])] = rescueReceptionRecord{key: append([]byte(nil), r.Key...)}
	sig, err := g.rescueSigner.Sign(rand.Reader, RescueResultTranscript(g.rescue.endpoint, r.HostNonce, r.Key, id[:], code))
	if err != nil || sig == nil || sig.Format != ssh.KeyAlgoED25519 {
		delete(g.rescue.records, string(id[:]))
		g.rescueCodes.end(idHex)
		return RescueReception{}, ErrRefused
	}
	return RescueReception{ID: append([]byte(nil), id[:]...), Code: code, Signature: append([]byte(nil), sig.Blob...)}, nil
}

// EndRescue drops the reception and its code at once.
func (g *Gateway) EndRescue(id []byte) {
	if g == nil || g.rescue == nil {
		return
	}
	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
	if _, ok := g.rescue.records[string(id)]; !ok {
		return
	}
	delete(g.rescue.records, string(id))
	g.rescueCodes.end(hex.EncodeToString(id))
}

func (g *Gateway) rescueCapacityLocked() error {
	if len(g.rescue.records) >= maxRescueReceptions || g.rescueCodes.liveCount() >= maxRescueCodes {
		return ErrRescueBusy
	}
	return nil
}
