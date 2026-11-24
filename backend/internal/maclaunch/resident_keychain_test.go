//go:build darwin && cgo

package maclaunch

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostkeychain"
	"golang.org/x/crypto/ssh"
)

type noKeychainClient struct{}

func (noKeychainClient) NewClient(ResidentClientConfig, *Approved) (ResidentClient, error) {
	return nil, errors.New("test client is not part of startup loading")
}

func residentKeychainPolicyFile(t *testing.T, fingerprint string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cwd, ".resident-keychain-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Error("owned resident policy fixture remains")
		}
	})
	policyPath := filepath.Join(root, "resident-policy.json")
	raw := keychainPolicyBytes(t, policyPath, "", fingerprint)
	defer clear(raw)
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	policy["work_dir"], policy["home"] = root, root
	raw, err = json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	if err := os.WriteFile(policyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return policyPath
}

func residentTestKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seed := append([]byte(nil), private.Seed()...)
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(seed) })
	return signer, seed
}

func withLoginKeychainForTest(t *testing.T) {
	t.Helper()
	old := hostkeychain.UseDataProtectionKeychain
	hostkeychain.UseDataProtectionKeychain = false
	t.Cleanup(func() { hostkeychain.UseDataProtectionKeychain = old })
}

// This proves the policy-selected Keychain signer reaches the normal startup factory and can authenticate to a gateway.
func TestKeychainResidentStartupAuthenticatesToSyntheticGateway(t *testing.T) {
	withLoginKeychainForTest(t)
	hostSigner, seed := residentTestKey(t)
	fingerprint := ssh.FingerprintSHA256(hostSigner.PublicKey())
	if err := hostkeychain.Store(seed, fingerprint); err != nil {
		t.Fatalf("store test host key: %v", err)
	}
	t.Cleanup(func() {
		if err := hostkeychain.Delete(fingerprint); err != nil {
			t.Error(err)
		}
	})
	policyPath := residentKeychainPolicyFile(t, fingerprint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	factory, err := LoadStandbyResident(ctx, policyPath, noKeychainClient{}, agentgateway.ChannelBudgets())
	if err != nil || factory == nil {
		t.Fatalf("normal resident startup rejected Keychain policy: %v", err)
	}
	// The normal loader's Keychain signer must complete public-key authentication against the enrolled synthetic gateway.
	runSyntheticGatewaySSHHandshake(t, factory.signer, hostSigner.PublicKey())
}

// A valid policy with a missing or unreadable key must keep a distinct result from policy rejection.
func TestMissingResidentKeychainItemIsNotPolicyRejection(t *testing.T) {
	withLoginKeychainForTest(t)
	missing, _ := residentTestKey(t)
	fingerprint := ssh.FingerprintSHA256(missing.PublicKey())
	policyPath := residentKeychainPolicyFile(t, fingerprint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	factory, err := LoadStandbyResident(ctx, policyPath, noKeychainClient{}, agentgateway.ChannelBudgets())
	if factory != nil || !errors.Is(err, ErrResidentKeyUnavailable) || IsStartupPolicyRejected(err) {
		t.Fatalf("missing key result: factory=%v err=%v policyRejected=%v", factory, err, IsStartupPolicyRejected(err))
	}
}

func runSyntheticGatewaySSHHandshake(t *testing.T, signer ssh.Signer, enrolled ssh.PublicKey) {
	t.Helper()
	if signer == nil {
		t.Fatal("resident startup returned no signer")
	}
	gateway, _ := residentTestKey(t)
	var authenticated atomic.Bool
	server := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(enrolled.Marshal()) {
			return nil, errors.New("host key does not match enrollment")
		}
		authenticated.Store(true)
		return &ssh.Permissions{}, nil
	}}
	server.AddHostKey(gateway)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		serverPipe, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		_ = serverPipe.SetDeadline(time.Now().Add(5 * time.Second))
		conn, _, _, err := ssh.NewServerConn(serverPipe, server)
		if err == nil {
			err = conn.Close()
		} else {
			_ = serverPipe.Close()
		}
		serverDone <- err
	}()
	clientPipe, err := net.DialTimeout("tcp4", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = clientPipe.SetDeadline(time.Now().Add(5 * time.Second))
	client, _, _, err := ssh.NewClientConn(clientPipe, "synthetic-gateway", &ssh.ClientConfig{
		User: "resident-host", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(gateway.PublicKey()), Timeout: 5 * time.Second,
	})
	if err != nil {
		_ = clientPipe.Close()
		<-serverDone
		t.Fatalf("synthetic gateway handshake failed: %v", err)
	}
	_ = client.Close()
	if err := <-serverDone; err != nil {
		t.Fatalf("synthetic gateway closed with error: %v", err)
	}
	if !authenticated.Load() {
		t.Fatal("synthetic gateway did not authenticate the enrolled host key")
	}
}
