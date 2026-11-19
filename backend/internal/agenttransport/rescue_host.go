package agenttransport

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"time"

	"golang.org/x/crypto/ssh"
)

// RescueClientConfig is chosen by the host composition. Endpoint is the exact
// public wss reception URL; Pins are the bundled identity keys, the current
// one and at most one next.
type RescueClientConfig struct {
	Endpoint string
	Pins     []ssh.PublicKey
}

// RescueClient is one reception, from 受付 ON to its end. It makes K in
// memory, never writes it anywhere and drops it when the reception ends. It
// never reconnects or takes a new code: a new reception needs a new client.
type RescueClient struct {
	transport func(acquirer) http.RoundTripper
	after     func(time.Duration) <-chan time.Time // the code expiry; nil is time.After
	key       ed25519.PrivateKey                  // K
	done      chan struct{}
}

func NewRescueClient(cfg RescueClientConfig) (*RescueClient, error) {
	return &RescueClient{done: make(chan struct{})}, nil
}

// Open registers the reception and returns its code once the signed result
// verifies for this host's nonce and K.
func (c *RescueClient) Open(ctx context.Context) (string, error) { return "", ErrRefused }

// Done is closed once the reception ended and K is dropped.
func (c *RescueClient) Done() <-chan struct{} { return c.done }

// Close is 受付 OFF.
func (c *RescueClient) Close() error { return nil }
