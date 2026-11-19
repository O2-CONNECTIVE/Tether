package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnswerRescueSignerDoesNotRaceWithEndRescue(t *testing.T) {
	now := time.Now()
	g := rescueReceptionGateway(t, &now)
	defer g.Close(context.Background())

	oldProcs := runtime.GOMAXPROCS(32)
	defer runtime.GOMAXPROCS(oldProcs)
	message := bytes.Repeat([]byte{0x72}, 1<<16)
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x71}, ed25519.SeedSize))
	id := []byte("race-reception")
	record := &rescueReceptionRecord{clientKey: private, done: make(chan struct{})}
	g.rescue.mu.Lock()
	g.rescue.records[string(id)] = record
	g.rescue.mu.Unlock()
	signer, ownedKey, err := newRescueClientSigner(record.clientKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clearRescueClientKey(ownedKey)
	start := make(chan struct{})
	firstSign := make(chan struct{})
	stop := make(chan struct{})
	var done sync.WaitGroup
	var invalid atomic.Int64
	done.Add(1)
	go func() {
		defer done.Done()
		<-start
		first := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			sig, signErr := signer.Sign(rand.Reader, message)
			if first {
				close(firstSign)
				first = false
			}
			if signErr != nil || signer.PublicKey().Verify(message, sig) != nil {
				invalid.Add(1)
			}
		}
	}()
	close(start)
	<-firstSign
	for i := 0; i < 1000; i++ {
		record = &rescueReceptionRecord{clientKey: private, done: make(chan struct{})}
		g.rescue.mu.Lock()
		g.rescue.records[string(id)] = record
		g.rescue.mu.Unlock()
		g.EndRescue(id)
	}
	close(stop)
	done.Wait()
	if invalid.Load() != 0 {
		t.Fatalf("%d signatures failed after EndRescue; signer must own C until handshake completes", invalid.Load())
	}
}
