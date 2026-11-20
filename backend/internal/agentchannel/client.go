package agentchannel

import (
	"context"
	"errors"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// ClientConfig is the explicit dispatch on the agent side. HostKey is the
// pinned raw Ed25519 host key and Signer the already enrolled raw Ed25519
// signer; nothing is learned on first use or generated here. Binding is what
// the dispatch approved; the LaunchID in it is only the SSH user selector.
// Deadline is the local hard deadline and should not exceed the grant's.
type ClientConfig struct {
	Binding              Binding
	HostKey              ssh.PublicKey
	Signer               ssh.Signer
	Deadline             time.Time
	AllowRescueClockSkew bool             // uses the fixed rescue allowance without changing Deadline
	Clock                func() time.Time // admission-time clock; nil uses time.Now
	Budgets              Budgets
	OnActivity           func() // called after authenticated channel data or a valid heartbeat reply
}

// Open takes ownership of conn on every call with a non-nil conn, performs the
// handshake with only public key authentication (no password, keyboard,
// certificate, agent forwarding or auth-none acceptance), opens exactly one
// agent channel bound to the dispatch and returns it. ctx bounds the whole
// channel. A non-nil Channel with an error is the same owner whose cleanup is
// unconfirmed; call its Close to retry. There is no reconnect or retry.
func Open(ctx context.Context, cfg ClientConfig, conn net.Conn) (*Channel, error) {
	if conn == nil {
		return nil, ErrConfig
	}
	binding := cfg.Binding
	if binding.ConnectionGeneration == 0 {
		binding.ConnectionGeneration = 1
	}
	host, hostErr := rawEd25519(cfg.HostKey)
	signer, signerErr := newFixedSigner(cfg.Signer)
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	maxLifetime := MaxClientLifetime
	if cfg.AllowRescueClockSkew {
		maxLifetime += RescueClockSkewAllowance
	}
	if ctx == nil || hostErr != nil || signerErr != nil || !binding.valid() || !cfg.Budgets.valid() ||
		!now.Before(cfg.Deadline) || cfg.Deadline.After(now.Add(maxLifetime)) {
		_ = conn.Close()
		return nil, ErrConfig
	}
	hostKey := host.Marshal()
	c := newChannel(conn, cfg.Budgets, cfg.Deadline, binding, nil)
	c.onActivity = cfg.OnActivity
	admitted := make(chan struct{})
	c.spawn(func() { c.watchAdmission(ctx, nil, admitted) })
	config := &ssh.ClientConfig{
		Config:            algorithms(),
		User:              binding.Grant.Identity.LaunchID,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   pinned(hostKey),
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
	}
	sc, chans, reqs, err := ssh.NewClientConn(c.raw, hostAlias, config)
	if err != nil {
		return c.fail(ReasonHandshake, ErrRejected)
	}
	transport := c.attach(sc, reqs)
	c.spawn(func() { c.serveChannels(chans, nil) })
	expect := commitment(binding, hostKey)
	ch, chReqs, err := sc.OpenChannel(ChannelType, expect[:])
	if err != nil {
		var refused *ssh.OpenChannelError
		if errors.As(err, &refused) {
			return c.fail(ReasonRejected, ErrRejected)
		}
		return c.fail(ReasonTransport, ErrRejected)
	}
	return c.admit(ctx, nil, admitted, transport, ch, chReqs, newPeer(hostKey))
}
