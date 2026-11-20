//go:build darwin && cgo

package agenttransport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/crypto/ssh"
)

type standbySleepAssertion struct {
	mu      sync.Mutex
	takeErr error
	takes   int
	release int
	held    int
}

func (s *standbySleepAssertion) Take() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.takes++
	if s.takeErr != nil {
		return s.takeErr
	}
	s.held++
	return nil
}

func (s *standbySleepAssertion) Release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.release++
	if s.held > 0 {
		s.held--
	}
	return nil
}

func (s *standbySleepAssertion) counts() (takes, releases, held int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takes, s.release, s.held
}

type standbyWorkClient struct {
	started chan struct{}
	finish  chan struct{}
	once    sync.Once
}

func (c *standbyWorkClient) Run(ctx context.Context) error {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.finish:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*standbyWorkClient) Close(context.Context) error  { return nil }
func (*standbyWorkClient) CleanupResult() (error, bool) { return nil, true }

type standbyWorkTransport struct {
	calls   atomic.Int32
	clients chan *standbyWorkClient
}

func (t *standbyWorkTransport) NewClient(maclaunch.ResidentClientConfig, *maclaunch.Approved) (maclaunch.ResidentClient, error) {
	c := &standbyWorkClient{started: make(chan struct{}), finish: make(chan struct{})}
	t.calls.Add(1)
	t.clients <- c
	return c, nil
}

type standbyCollectionRegistrar struct {
	*noGrantRegistrar
	proofs chan agentgateway.ResidentCollectionProof
}

func (r *standbyCollectionRegistrar) ResidentCollected(p agentgateway.ResidentCollectionProof) error {
	r.proofs <- p
	return nil
}

func TestStandbyConsumeSleepFailureRefusesAndMonitorCollects(t *testing.T) {
	hostBytes := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x45}, ed25519.SeedSize))
	principalBytes := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x46}, ed25519.SeedSize))
	host, err := ssh.NewSignerFromKey(hostBytes)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := ssh.NewSignerFromKey(principalBytes)
	if err != nil {
		t.Fatal(err)
	}
	const endpoint = "wss://resident.example.test/control"
	path, _ := protectedFixture(t, hostBytes, principal.PublicKey(), endpoint)
	load, stopLoad := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopLoad()
	transport := &standbyWorkTransport{clients: make(chan *standbyWorkClient, 1)}
	factory, err := maclaunch.LoadStandbyResident(load, path, transport, agentgateway.ChannelBudgets())
	if err != nil {
		t.Fatal(err)
	}
	registrar := &standbyCollectionRegistrar{noGrantRegistrar: &noGrantRegistrar{}, proofs: make(chan agentgateway.ResidentCollectionProof, 2)}
	budgets := agenttransport.ExportResidentTestBudgets()
	handler, err := agenttransport.NewHandler(agenttransport.HandlerConfig{
		Endpoint: endpoint, Registrar: registrar, Budgets: budgets,
		Hosts:              []agenttransport.EnrolledHost{{Label: "host-one", PublicKey: host.PublicKey(), Principals: []string{"member-one"}}},
		ResidentPrincipals: []agenttransport.ResidentPrincipal{{Label: "member-one", Signer: principal}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := agenttransport.NewResidentConnector(agenttransport.ResidentClientConfig{
		Endpoint: endpoint, HostLabel: "host-one", PrincipalLabel: "member-one",
		HostKey: host, PrincipalKey: principal.PublicKey(), Budgets: budgets,
	}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	memory := agenttransport.ExportBindResidentMemory(client, handler)
	sleep := &standbySleepAssertion{takeErr: errors.New("synthetic assertion failure")}
	standby := residentmac.ReplayStandbyWithSleep(factory, client, sleep)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := standby.Close(cleanup); err != nil {
			t.Error("standby close", err)
		}
		if err := handler.Close(cleanup); err != nil {
			t.Error("handler close", err)
		}
		memory.CheckStopped(t)
	})
	if receipt := standby.RequestOn(1, standby.Snapshot().Revision, 2*time.Hour); receipt.Disposition != "accepted" {
		t.Fatal(receipt)
	}
	waitStandbySleepCondition(t, func() bool { return handler.ResidentConnected("host-one", "member-one") })

	propose := func(n int) error {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		id := fmt.Sprintf("%032x", n)
		return handler.ProposeResidentFixed(ctx, "host-one", "member-one", id, time.Now().Add(7*time.Second).Truncate(time.Millisecond))
	}
	if err := propose(1); !errors.Is(err, agenttransport.ErrWorkUnconfigured) || errors.Is(err, agentgateway.ErrResidentPending) {
		t.Fatalf("failed Take did not produce a signed refusal: %v", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("work client was created after assertion acquisition failed")
	}
	if takes, releases, held := sleep.counts(); takes != 1 || releases != 0 || held != 0 {
		t.Fatalf("failed Take changed assertion state: takes=%d releases=%d held=%d", takes, releases, held)
	}

	sleep.mu.Lock()
	sleep.takeErr = nil
	sleep.mu.Unlock()
	if err := propose(2); !errors.Is(err, agenttransport.ErrWorkPending) {
		t.Fatalf("next work did not receive the signed pending response: %v", err)
	}
	var workClient *standbyWorkClient
	select {
	case workClient = <-transport.clients:
	case <-time.After(2 * time.Second):
		t.Fatal("admitted work did not reach the resident client")
	}
	select {
	case <-workClient.started:
	case <-time.After(2 * time.Second):
		t.Fatal("admitted work did not start")
	}
	if takes, releases, held := sleep.counts(); takes != 2 || releases != 0 || held != 1 {
		t.Fatalf("active work assertion mismatch: takes=%d releases=%d held=%d", takes, releases, held)
	}
	if err := propose(3); !errors.Is(err, agenttransport.ErrWorkUnconfigured) || errors.Is(err, agentgateway.ErrResidentPending) {
		t.Fatalf("second work did not receive the signed refusal: %v", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatalf("busy work created another client: %d", transport.calls.Load())
	}
	close(workClient.finish)
	select {
	case proof := <-registrar.proofs:
		if proof.Cleanup != "confirmed" || proof.Binding.RequestID != fmt.Sprintf("%032x", 2) {
			t.Fatalf("monitor sent unexpected collection proof: %+v", proof)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not send the real collection proof")
	}
	waitStandbySleepCondition(t, func() bool {
		_, releases, held := sleep.counts()
		return releases == 1 && held == 0
	})
}

func waitStandbySleepCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("standby consume condition did not become true")
}
