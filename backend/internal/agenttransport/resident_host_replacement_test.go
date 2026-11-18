package agenttransport

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

type rotatingHostRegistrar struct {
	*recordingRegistrar
	mu  sync.Mutex
	key []byte
}

func (r *rotatingHostRegistrar) HostKey(string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.key...)
}

func (r *rotatingHostRegistrar) replace(key []byte) {
	r.mu.Lock()
	r.key = append([]byte(nil), key...)
	r.mu.Unlock()
}

func TestResidentReplacementAfterReserveRejectsOldControl_A9(t *testing.T) {
	rig := newResidentRig(t, nil)
	oldKey := fixtureSigner(t, 0x11)
	registrar := &rotatingHostRegistrar{recordingRegistrar: rig.rec, key: oldKey.PublicKey().Marshal()}
	rig.h.registrar = registrar
	reserved := make(chan struct{})
	oldHook := testHookAfterResidentReserved
	testHookAfterResidentReserved = func() {
		registrar.replace(fixtureSigner(t, 0x12).PublicKey().Marshal())
		close(reserved)
	}
	t.Cleanup(func() { testHookAfterResidentReserved = oldHook })

	done := residentRun(rig.c)
	select {
	case <-reserved:
	case <-time.After(3 * time.Second):
		t.Fatal("resident slot was not reserved")
	}
	// A replacement between reservation and acceptance must close the old identity.
	time.Sleep(150 * time.Millisecond)
	if rig.c.Connected() || rig.h.ResidentConnected(hostLabel, member1) {
		t.Fatal("old resident control remained connected after host replacement")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("resident client unexpectedly completed")
		}
	default:
	}
}

func TestResidentReplacementAfterReserveReleasesSlot_A9(t *testing.T) {
	// Refusing the old key after it reserved the slot must give the slot back,
	// or the newly paired host could not connect until the gateway restarts.
	rig := newResidentRig(t, nil)
	registrar := &rotatingHostRegistrar{recordingRegistrar: rig.rec, key: fixtureSigner(t, 0x11).PublicKey().Marshal()}
	rig.h.registrar = registrar
	reserved := make(chan struct{})
	oldHook := testHookAfterResidentReserved
	testHookAfterResidentReserved = func() {
		registrar.replace(fixtureSigner(t, 0x12).PublicKey().Marshal())
		close(reserved)
	}
	t.Cleanup(func() { testHookAfterResidentReserved = oldHook })
	residentRun(rig.c)
	select {
	case <-reserved:
	case <-time.After(3 * time.Second):
		t.Fatal("resident slot was not reserved")
	}
	k := residentMapKey(hostLabel, member1)
	deadline := time.Now().Add(3 * time.Second)
	for {
		rig.h.mu.Lock()
		held := rig.h.residents[k] != nil
		rig.h.mu.Unlock()
		if !held {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("refused old control still holds the resident slot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var _ interface {
	Connect(string, agentgateway.Dispatch, net.Conn) error
	HostKey(string) []byte
} = (*rotatingHostRegistrar)(nil)
