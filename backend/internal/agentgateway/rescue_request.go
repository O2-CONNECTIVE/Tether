package agentgateway

import (
	"encoding/binary"
	"net"
	"time"
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
	Answer    string
	Dispatch  Dispatch
	Signature []byte // raw ssh-ed25519 signature by K over RescueAnswerTranscript
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
		parts = append(parts, binary.BigEndian.AppendUint64(nil, b.Grant.Generation), []byte(b.Grant.Identity.LaunchID),
			[]byte(b.Grant.Identity.Epoch), []byte(b.Grant.Identity.PolicyDigest), b.KeySetDigest[:], []byte{byte(b.Profile)},
			binary.BigEndian.AppendUint64(nil, uint64(d.Deadline.UnixNano())), d.HostKey)
	}
	return rescueTranscript(parts)
}

func (g *Gateway) AnswerRescue(id []byte, a RescueAnswer, conn net.Conn) error {
	if conn != nil {
		_ = conn.Close()
	}
	return ErrRefused
}
