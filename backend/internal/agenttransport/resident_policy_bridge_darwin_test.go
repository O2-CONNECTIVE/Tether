//go:build darwin && cgo

package agenttransport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

type noGrantRegistrar struct{ calls atomic.Int32 }

func (r *noGrantRegistrar) Connect(_ string, _ agentgateway.Dispatch, conn net.Conn) error {
	r.calls.Add(1)
	_ = conn.Close()
	return agenttransport.ErrRefused
}

type neverCreateClient struct{ calls atomic.Int32 }

func (c *neverCreateClient) NewClient(maclaunch.ResidentClientConfig, *maclaunch.Approved) (maclaunch.ResidentClient, error) {
	c.calls.Add(1)
	return nil, errors.New("this input-boundary test must not approve or run work")
}

// Read only files this test creates, with the real protected reader. The root is
// under this checkout (not a normal key/policy location). No ACL, trust store,
// real credential, or existing configuration is changed. The reader still
// verifies every ancestor and same opened handle; failures are never skipped.
func protectedFixture(t *testing.T, hostKey ed25519.PrivateKey, principal ssh.PublicKey, endpoint string) (string, func()) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cwd, ".resident-policy-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error("owned synthetic policy fixture cleanup failed", err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Error("owned synthetic policy fixture remains")
		}
	})
	block, err := ssh.MarshalPrivateKey(hostKey, "public fixed-seed test vector")
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := pem.EncodeToMemory(block)
	defer clear(keyBytes)
	keyPath, policyPath := filepath.Join(root, "fixture-key"), filepath.Join(root, "fixture-policy.json")
	if err := os.WriteFile(keyPath, keyBytes, 0600); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	policy := map[string]any{
		"protocol": "rescue-mac-resident-policy/1", "policy_digest": agentexec.MacCurrentUserPolicyDigest(),
		"registration_endpoint": endpoint, "host_label": "host-one", "principal_label": "member-one",
		"principal_public_key": base64.StdEncoding.EncodeToString(principal.Marshal()),
		"host_key_path":        keyPath, "host_key_fingerprint": ssh.FingerprintSHA256(signer.PublicKey()),
		"work_dir": root, "home": root, "cleanup_seconds": 10,
		"operations": []any{map[string]any{"id": maclaunch.ResidentFixedRequestID, "command": maclaunch.ResidentFixedCommand, "timeout_ms": 15000}},
	}
	write := func() {
		t.Helper()
		data, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(data)
		if err := os.WriteFile(policyPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	return policyPath, func() { policy["home"] = filepath.Join(root, "drifted"); write() }
}

func TestProtectedPolicyAcceptsActualReceiverBeforeWorkApproval(t *testing.T) {
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
	path, drift := protectedFixture(t, hostBytes, principal.PublicKey(), endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	transport := &neverCreateClient{}
	factory, err := maclaunch.LoadStandbyResident(ctx, path, transport, agentgateway.ChannelBudgets())
	if err != nil {
		t.Fatal("self-created protected fixture refused", err)
	}
	registrar := &noGrantRegistrar{}
	budgets := agenttransport.ExportResidentTestBudgets()
	handler, err := agenttransport.NewHandler(agenttransport.HandlerConfig{Endpoint: endpoint, Registrar: registrar, Budgets: budgets,
		Hosts:              []agenttransport.EnrolledHost{{Label: "host-one", PublicKey: host.PublicKey(), Principals: []string{"member-one"}}},
		ResidentPrincipals: []agenttransport.ResidentPrincipal{{Label: "member-one", Signer: principal}}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := agenttransport.NewResidentConnector(agenttransport.ResidentClientConfig{Endpoint: endpoint, HostLabel: "host-one", PrincipalLabel: "member-one", HostKey: host, PrincipalKey: principal.PublicKey(), Budgets: budgets}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	memory := agenttransport.ExportBindResidentMemory(client, handler)
	done := make(chan error, 1)
	var lease *agenttransport.ResidentSourceLease
	go func() { done <- client.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 4*time.Second)
		defer stop()
		if lease != nil {
			if err := lease.Finish(cleanup, nil); err != nil {
				t.Error("same source Finish", err)
			}
		}
		if err := client.Close(cleanup); err != nil {
			t.Error("same control Close", err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Error("same control Run result", err)
			}
		case <-cleanup.Done():
			t.Error("same control Run did not finish")
		}
		if err := handler.Close(cleanup); err != nil {
			t.Error("same handler Close", err)
		}
		memory.CheckStopped(t)
	})
	lease, err = client.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for !handler.ResidentConnected("host-one", "member-one") {
		select {
		case <-ctx.Done():
			t.Fatal("actual authenticated source not ready")
		case <-time.After(time.Millisecond):
		}
	}
	cloned := *lease.Receiver()
	if err := cloned.ClaimNativeSource(); !errors.Is(err, agenttransport.ErrClosed) {
		t.Fatal("copied receiver consumed the original claim", err)
	}
	if _, err := factory.OpenWireSource(&cloned); !errors.Is(err, maclaunch.ErrResident) {
		t.Fatal("copied receiver minted authentication", err)
	}
	type sourceResult struct {
		source *maclaunch.ResidentSource
		err    error
	}
	results := make(chan sourceResult, 4)
	for range 4 {
		go func() {
			source, err := factory.OpenWireSource(lease.Receiver())
			results <- sourceResult{source, err}
		}()
	}
	var source *maclaunch.ResidentSource
	for range 4 {
		result := <-results
		if result.source != nil {
			t.Cleanup(result.source.Close)
			if source != nil || result.err != nil {
				t.Error("concurrent consumers acquired multiple native sources", result.err)
			}
			source = result.source
		} else if !errors.Is(result.err, maclaunch.ErrResident) {
			t.Error("unexpected source claim result", result.err)
		}
	}
	if source == nil {
		t.Fatal("no consumer acquired the original native source")
	}
	proposed := make(chan error, 1)
	go func() {
		proposed <- handler.ProposeResidentFixed(ctx, "host-one", "member-one", strings.Repeat("a", 32), time.Now().Add(10*time.Second).Truncate(time.Millisecond))
	}()
	intent, err := lease.Next()
	if err != nil {
		t.Fatal(err)
	}
	request, err := source.NewWireRequest(intent)
	if err != nil || request == nil || request.SourceContext != intent.OriginalContext() || !request.Deadline.Equal(intent.Deadline()) {
		t.Fatal("actual received scope was not retained", err)
	}
	if repeated, err := source.NewWireRequest(intent); repeated != nil || !errors.Is(err, maclaunch.ErrResident) {
		t.Fatal("same source repeated its native request", err)
	}
	secondFactory, err := maclaunch.LoadStandbyResident(ctx, path, transport, agentgateway.ChannelBudgets())
	if err != nil {
		t.Fatal(err)
	}
	for index, candidate := range []*maclaunch.ResidentFactory{factory, secondFactory} {
		duplicate, err := candidate.OpenWireSource(lease.Receiver())
		if duplicate != nil {
			t.Cleanup(duplicate.Close)
			_, repeated := duplicate.NewWireRequest(intent)
			t.Errorf("same receiver created a replacement source: factory=%d repeated_intent_accepted=%t", index, repeated == nil)
		}
		if duplicate != nil || !errors.Is(err, maclaunch.ErrResident) {
			t.Errorf("same authenticated receiver was not single-use: factory=%d err=%v", index, err)
		}
	}
	// Stop at input acceptance. Approve and Run are deliberately never called;
	// there is no native grant/session/command or work-registration success.
	if err := lease.Refuse(intent); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-proposed:
		if !errors.Is(err, agenttransport.ErrWorkUnconfigured) {
			t.Fatal("refusal became work success", err)
		}
	case <-ctx.Done():
		t.Fatal("original proposal did not finish")
	}
	if _, err := source.NewWireRequest(intent); !errors.Is(err, maclaunch.ErrResident) {
		t.Fatal("released original scope revived", err)
	}
	source.Close()
	if replacement, err := secondFactory.OpenWireSource(lease.Receiver()); replacement != nil || !errors.Is(err, maclaunch.ErrResident) {
		if replacement != nil {
			t.Cleanup(replacement.Close)
		}
		t.Fatal("closing the original source reset the receiver claim", err)
	}
	drift()
	if _, err := factory.OpenWireSource(lease.Receiver()); !errors.Is(err, maclaunch.ErrResident) {
		t.Fatal("changed protected policy was accepted", err)
	}
	if registrar.calls.Load() != 0 || transport.calls.Load() != 0 {
		t.Fatal("input acceptance created a work transport")
	}
}
