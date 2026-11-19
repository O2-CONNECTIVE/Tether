package agentgateway

import (
	"errors"
	"time"
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
	return nil
}

// RescueResultTranscript is what the identity key signs: the domain, the
// public reception URL, the host nonce, K, the reception id and the code.
func RescueResultTranscript(endpoint string, hostNonce, key, reception []byte, code string) []byte {
	return nil
}

// RescueEndpoint is the public reception URL, built from the configured
// resource origin and never from a request.
func (g *Gateway) RescueEndpoint() string { return "" }

// AdmitRescue runs before the upgrade.
func (g *Gateway) AdmitRescue() error { return ErrRefused }

// OpenRescue verifies the proof, records the reception and issues its code.
func (g *Gateway) OpenRescue(r RescueRequest) (RescueReception, error) {
	return RescueReception{}, ErrRefused
}

// EndRescue drops the reception and its code at once.
func (g *Gateway) EndRescue(id []byte) {}
