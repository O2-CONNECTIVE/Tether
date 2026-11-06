package agentlink_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"golang.org/x/crypto/ssh"
)

func TestCanonicalHostDoesNotReplacePlatformTransportOwner(t *testing.T) {
	// A Windows owner must still supply issuer/native preparation cleanup.
	// Public channel metadata alone must not make Host satisfy that whole port.
	if _, ok := any(&agentlink.Host{}).(agenttransport.Owner); ok {
		t.Fatal("canonical Host was accepted as the complete platform transport owner")
	}
}

func TestHostDispatchComesFromSameCanonicalChannelAndIsNotLiveAuthority(t *testing.T) {
	hostKey, peer := fixtureSigner(t, 101), fixtureSigner(t, 102)
	_, authority, session := grant(t, 10*time.Second, []ssh.PublicKey{peer.PublicKey()}, &macRuntime{procs: make(chan *fakeProc, 1)})
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{peer.PublicKey()}, HostKey: hostKey, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	got := host.ChannelDispatch()
	grant := authority.Grant()
	want := agentchannel.Binding{Grant: grant.Binding, KeySetDigest: grant.KeySetDigest, Profile: session.Profile()}
	if got.Binding != want || got.Deadline != grant.HardDeadline || !bytes.Equal(got.HostKey, hostKey.PublicKey().Marshal()) {
		t.Fatal("dispatch did not come from canonical channel")
	}
	got.HostKey[0] ^= 1
	got.Binding.Profile = agentexec.ProfileWindowsGuest
	got.Deadline = time.Now().Add(time.Hour)
	fresh := host.ChannelDispatch()
	if fresh.Binding != want || fresh.Deadline != grant.HardDeadline || !bytes.Equal(fresh.HostKey, hostKey.PublicKey().Marshal()) {
		t.Fatal("caller mutated held dispatch")
	}
	if authenticated, observed := host.LocallyAuthenticated(); !observed || authenticated {
		t.Fatal("dispatch created a connection observation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := host.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Retain public identifiers for display/audit after closure; they are never
	// proof a grant is still live and do not refresh the deadline or authority.
	closed := host.ChannelDispatch()
	if closed.Binding != fresh.Binding || closed.Deadline != fresh.Deadline || !bytes.Equal(closed.HostKey, fresh.HostKey) {
		t.Fatal("closure replaced historical dispatch")
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("dispatch getter revived session")
	}
	if _, ok := host.CleanupResult(); ok {
		t.Fatal("dispatch getter invented Serve cleanup")
	}
}

func TestZeroHostHasNoDispatch(t *testing.T) {
	for _, host := range []*agentlink.Host{nil, {}} {
		d := host.ChannelDispatch()
		if d.Binding != (agentchannel.Binding{}) || !d.Deadline.IsZero() || len(d.HostKey) != 0 {
			t.Fatal("zero host supplied metadata")
		}
	}
}
