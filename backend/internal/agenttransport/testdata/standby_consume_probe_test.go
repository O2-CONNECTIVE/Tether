//go:build darwin && cgo

package agenttransport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/crypto/ssh"
)

func TestStandbyConsumeEndsOriginalSource(t *testing.T) {
	for _, end := range []string{"close", "policy-drift"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
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
				load, stopLoad := context.WithTimeout(context.Background(), 15*time.Second)
				defer stopLoad()
				transport := &neverCreateClient{}
				factory, err := maclaunch.LoadStandbyResident(load, path, transport, agentgateway.ChannelBudgets())
				if err != nil {
					t.Fatal(err)
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
				s := residentmac.ReplayStandby(factory, client)
				defer func() {
					cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					if err := s.Close(cleanup); err != nil {
						t.Error("same Standby Close", err)
					}
					if err := handler.Close(cleanup); err != nil {
						t.Error("same handler Close", err)
					}
					memory.CheckStopped(t)
					if registrar.calls.Load() != 0 || transport.calls.Load() != 0 {
						t.Error("work transport was created")
					}
				}()
				if receipt := s.RequestOn(1, s.Snapshot().Revision); receipt.Disposition != "accepted" {
					t.Fatal(receipt)
				}
				synctest.Wait()
				source, lease, owner, done := residentmac.ReplayStandbyOriginal(s)
				if source == nil || lease == nil || owner == nil || done == nil {
					t.Fatal("real consume did not acquire its original source")
				}
				if source.Context().Err() != nil || lease.Context().Err() != nil || !agenttransport.ExportStandbyLeaseReading(lease) {
					t.Fatal("fixture did not hold actual Next on healthy authenticated control")
				}
				if end == "close" {
					source.Close()
				} else {
					drift()
					if _, err := factory.NewWireConnector(load); !errors.Is(err, maclaunch.ErrResident) {
						t.Fatal("policy drift did not block original factory", err)
					}
				}
				synctest.Wait()
				if source.Context().Err() == nil {
					t.Fatal("original native source remained live")
				}
				if !agenttransport.ExportStandbyLeaseFinished(lease) {
					t.Error("native source ended but consume retained original lease until OFF/control traffic")
				}
				if o := owner.Snapshot(); o.Owned != 0 || o.Unknown {
					t.Error("idle original owner lost known collection", o)
				}
			})
		})
	}
}
