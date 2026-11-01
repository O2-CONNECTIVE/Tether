package agentchannel

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net"

	"golang.org/x/crypto/ssh"
)

const (
	commitmentDomain = "rescue-host.agentchannel.binding\x00v1\x00"
	// peerExtension carries the verified key from the post-signature callback
	// to Accept. Only this package's callback writes Permissions.
	peerExtension = "rescue-host.agentchannel.peer-key"
	// hostAlias is passed where the SSH client wants an address. It selects
	// nothing: the host key is pinned and there is no routing.
	hostAlias = "rescue-host-agent"
)

// algorithms fixes the SSH primitives on both ends instead of the library
// defaults; the user and host key algorithm is raw ssh-ed25519 only.
func algorithms() ssh.Config {
	return ssh.Config{
		KeyExchanges: []string{"curve25519-sha256", "curve25519-sha256@libssh.org"},
		Ciphers:      []string{"chacha20-poly1305@openssh.com", "aes256-gcm@openssh.com"},
		MACs:         []string{"hmac-sha2-256-etm@openssh.com"},
	}
}

// rawEd25519 re-parses the wire bytes once, so a later change in the caller's
// key object cannot alter what this package pins or matches. Certificates and
// every other key type are refused.
func rawEd25519(key ssh.PublicKey) (ssh.PublicKey, error) {
	if key == nil || key.Type() != ssh.KeyAlgoED25519 {
		return nil, ErrConfig
	}
	return rawEd25519Bytes(key.Marshal())
}

// rawEd25519Bytes parses a private copy of SSH wire bytes and requires the
// re-marshaled form to be byte-identical, so only the canonical encoding of a
// raw Ed25519 key is accepted.
func rawEd25519Bytes(wire []byte) (ssh.PublicKey, error) {
	wire = append([]byte(nil), wire...)
	parsed, err := ssh.ParsePublicKey(wire)
	if err != nil || parsed.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(parsed.Marshal(), wire) {
		return nil, ErrConfig
	}
	return parsed, nil
}

// fixedSigner advertises the copied public key and checks every signature it
// returns against that copy, so the signer's key cannot silently change. It
// wraps an existing signer and never creates key material.
type fixedSigner struct {
	signer ssh.Signer
	public ssh.PublicKey
}

func newFixedSigner(s ssh.Signer) (*fixedSigner, error) {
	if s == nil {
		return nil, ErrConfig
	}
	public, err := rawEd25519(s.PublicKey())
	if err != nil {
		return nil, err
	}
	return &fixedSigner{signer: s, public: public}, nil
}

func (f *fixedSigner) PublicKey() ssh.PublicKey { return f.public }

func (f *fixedSigner) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	return f.SignWithAlgorithm(rand, data, ssh.KeyAlgoED25519)
}

func (f *fixedSigner) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	if algorithm != "" && algorithm != ssh.KeyAlgoED25519 {
		return nil, ErrConfig
	}
	var sig *ssh.Signature
	var err error
	if as, ok := f.signer.(ssh.AlgorithmSigner); ok {
		sig, err = as.SignWithAlgorithm(rand, data, ssh.KeyAlgoED25519)
	} else {
		sig, err = f.signer.Sign(rand, data)
	}
	if err != nil || sig == nil || sig.Format != ssh.KeyAlgoED25519 || f.public.Verify(data, sig) != nil {
		return nil, ErrConfig
	}
	return sig, nil
}

// commitment binds one agent channel to the exact grant, approved key set,
// profile and pinned host key. Each end computes it from its own trusted
// inputs; the server only compares the received bytes and derives nothing.
func commitment(b Binding, hostKey []byte) [32]byte {
	h := sha256.New()
	field := func(v []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(v)))
		h.Write(n[:])
		h.Write(v)
	}
	var generation [8]byte
	binary.BigEndian.PutUint64(generation[:], b.Grant.Generation)
	field([]byte(commitmentDomain))
	field([]byte(ChannelType))
	field(generation[:])
	field([]byte(b.Grant.Identity.LaunchID))
	field([]byte(b.Grant.Identity.Epoch))
	field([]byte(b.Grant.Identity.PolicyDigest))
	field(b.KeySetDigest[:])
	field([]byte{byte(b.Profile)})
	field(hostKey)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// pinned accepts only the exact raw Ed25519 host key from the dispatch. There
// is no known_hosts lookup, trust on first use or insecure fallback.
func pinned(want []byte) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if key == nil || key.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(key.Marshal(), want) {
			return ErrRejected
		}
		return nil
	}
}
