package agentlink_test

import (
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"golang.org/x/crypto/ssh"
	"testing"
	"time"
)

func TestLocalAuthenticatedMeansProvenPeerNotStart(t *testing.T) {
	signer, peer := fixtureSigner(t, 61), fixtureSigner(t, 62)
	_, authority, session := grant(t, 5*time.Second, []ssh.PublicKey{peer.PublicKey()}, &macRuntime{procs: make(chan *fakeProc, 1)})
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{peer.PublicKey()}, HostKey: signer, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	if connected, ok := host.LocallyAuthenticated(); !ok || connected {
		t.Fatal("connected before handshake")
	}
	a, b := memPair()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx, a) }()
	if connected, _ := host.LocallyAuthenticated(); connected {
		t.Fatal("connected without peer")
	}
	ch, err := agentchannel.Open(ctx, agentchannel.ClientConfig{Binding: agentchannel.Binding{Grant: session.Binding(), KeySetDigest: session.KeySetDigest(), Profile: agentexec.ProfileMacCurrentUser}, HostKey: signer.PublicKey(), Signer: peer, Deadline: authority.Grant().HardDeadline, Budgets: budgets()}, b)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for {
		connected, ok := host.LocallyAuthenticated()
		if ok && connected {
			break
		}
		if time.Now().After(until) {
			t.Fatal("no authenticated observation")
		}
		time.Sleep(time.Millisecond)
	}
	ch.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("host not joined")
	}
	if connected, ok := host.LocallyAuthenticated(); !ok || connected {
		t.Fatal("connected after end")
	}
}
