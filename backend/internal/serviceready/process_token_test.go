package serviceready

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

func protectedProcessToken() processTokenState {
	return processTokenState{id: 17, modified: 29, currentID: 17, currentModified: 29, borrowedID: 17, borrowedModified: 29,
		security: SecurityPolicy{OwnerSID: user, Protected: true, ACL: []protectedkey.ACE{{SID: service, Mask: 0xf01ff}, {SID: guest, Mask: 0x20008}}}}
}

func queryTokenFixture(t *testing.T, sid string, mask uint32) (*Factory, *fakeKernel) {
	t.Helper()
	c := config()
	c.ProcessSecurity.ACL = append(c.ProcessSecurity.ACL, protectedkey.ACE{SID: sid, Mask: mask})
	f, err := NewNative(c)
	if err != nil {
		t.Fatal(err)
	}
	k := fixture(c)
	k.proc.token = protectedProcessToken()
	f.api = k
	return f, k
}

func TestProcessQueryNeedsIndependentTokenObjectEvidence(t *testing.T) {
	c := config()
	c.ProcessSecurity.ACL = append(c.ProcessSecurity.ACL, protectedkey.ACE{SID: guest, Mask: 0x121400})
	f, err := NewNative(c)
	if err != nil {
		t.Fatalf("query-only process policy cannot reach independent token validation: %v", err)
	}
	k := fixture(c)
	f.api = k
	p, err := f.PreparePreflight(testContext(t))
	if err == nil || p == nil {
		t.Fatal("missing token-object evidence was accepted")
	}
	if k.name != "" {
		t.Fatal("listener created before token-object protection was checked")
	}
	if err := p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProcessQueryWithProtectedTokenRetainsSamePreflightOwner(t *testing.T) {
	for _, sid := range []string{guest, "S-1-5-21-9"} {
		for _, mask := range []uint32{0x1000, 0x400, 0x121400} {
			t.Run(fmt.Sprintf("%s/%x", sid, mask), func(t *testing.T) {
				f, k := queryTokenFixture(t, sid, mask)
				p, err := f.PreparePreflight(testContext(t))
				if err != nil || p == nil {
					t.Fatalf("paired policy rejected: %v", err)
				}
				if err := p.Recheck(testContext(t)); err != nil {
					t.Fatal(err)
				}
				if k.proc.tokenInspects != 2 {
					t.Fatal("same token was not rechecked")
				}
				if err := p.Close(testContext(t)); err != nil {
					t.Fatal(err)
				}
				if k.proc.closes != 1 {
					t.Fatal("process/token owner not closed once")
				}
			})
		}
	}
}

func TestTokenObjectRightsRejectCapabilityAndMalformedEvidenceBeforeListener(t *testing.T) {
	for _, sid := range []string{guest, "S-1-5-21-9"} {
		for _, mask := range []uint32{1, 2, 4, 0x10, 0x20, 0x40, 0x80, 0x100, 0x10000, 0x40000, 0x80000, 0x100000, 0x1000000, 0x2000000, 0x10000000, 0x80000000} {
			t.Run(fmt.Sprintf("%s/%x", sid, mask), func(t *testing.T) {
				f, k := queryTokenFixture(t, guest, 0x1000)
				k.proc.token.security.ACL = append(k.proc.token.security.ACL,
					protectedkey.ACE{Type: 1, SID: sid, Mask: mask}, protectedkey.ACE{SID: sid, Mask: mask})
				p, err := f.Prepare(testContext(t))
				if err == nil || p == nil || k.name != "" {
					t.Fatal("unsafe token accepted before listener")
				}
				if err := p.Close(testContext(t)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	for _, change := range []func(*processTokenState){
		func(s *processTokenState) { s.id = 0 }, func(s *processTokenState) { s.modified = 0 },
		func(s *processTokenState) { s.currentID++ }, func(s *processTokenState) { s.borrowedID++ },
		func(s *processTokenState) { s.currentModified++ }, func(s *processTokenState) { s.borrowedModified++ },
		func(s *processTokenState) { s.security.OwnerSID = guest }, func(s *processTokenState) { s.security.OwnerSID = "S-1-5-21-9" },
		func(s *processTokenState) { s.security.ACL = nil },
		func(s *processTokenState) { s.security.ACL[0].Flags = 1 }, func(s *processTokenState) { s.security.ACL[0].Type = 5 },
		func(s *processTokenState) { s.security.ACL[0].Mask = 0 }, func(s *processTokenState) { s.security.ACL[0].SID = "bad" },
	} {
		f, k := queryTokenFixture(t, guest, 0x1000)
		change(&k.proc.token)
		p, err := f.Prepare(testContext(t))
		if err == nil || p == nil || k.name != "" {
			t.Fatal("malformed or mismatched token accepted")
		}
		if err := p.Close(testContext(t)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTokenObjectIdentityAndSecurityDriftPermanentlyRevoke(t *testing.T) {
	for _, change := range []func(*processTokenState){
		func(s *processTokenState) { s.id++; s.currentID++; s.borrowedID++ },
		func(s *processTokenState) { s.modified++; s.currentModified++; s.borrowedModified++ },
		func(s *processTokenState) { s.security.OwnerSID = operator },
		func(s *processTokenState) { s.security.ACL[1].Mask = 8 },
		func(s *processTokenState) { s.security.Protected = !s.security.Protected },
	} {
		f, k := queryTokenFixture(t, guest, 0x1000)
		p, err := f.PreparePreflight(testContext(t))
		if err != nil {
			t.Fatal(err)
		}
		change(&k.proc.token)
		if binding, err := p.Binding(testContext(t)); err == nil || binding != (ProcessBinding{}) {
			t.Fatal("token drift accepted")
		}
		k.proc.token = protectedProcessToken()
		if err := p.Recheck(testContext(t)); err == nil {
			t.Fatal("revoked token evidence revived")
		}
		if err := p.Close(testContext(t)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTokenObjectPinsDescriptorWithoutRequiringInheritanceProtection(t *testing.T) {
	f, k := queryTokenFixture(t, guest, 0x1000)
	k.proc.token.security.Protected = false
	p, err := f.PreparePreflight(testContext(t))
	if err != nil || p == nil {
		t.Fatalf("safe token-object ACL rejected solely for inheritance flag: %v", err)
	}
	if err := p.Recheck(testContext(t)); err != nil {
		t.Fatal(err)
	}
	k.proc.token.security.Protected = true
	if err := p.Recheck(testContext(t)); err == nil {
		t.Fatal("token descriptor flag changed without revocation")
	}
	if err := p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestTokenObjectEmptyDACLRemainsDistinctFromNullDACL(t *testing.T) {
	f, k := queryTokenFixture(t, guest, 0x1000)
	k.proc.token.security.ACL = []protectedkey.ACE{}
	p, err := f.PreparePreflight(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Recheck(testContext(t)); err != nil {
		t.Fatalf("deny-all ACL lost during evidence copy: %v", err)
	}
	k.proc.token.security.ACL = nil
	if err := p.Recheck(testContext(t)); err == nil {
		t.Fatal("null ACL accepted as deny-all")
	}
	if err := p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestTokenInspectionFailureCancellationAndCleanupKeepOwner(t *testing.T) {
	for _, mode := range []string{"partial", "cancelled", "fatal"} {
		t.Run(mode, func(t *testing.T) {
			f, k := queryTokenFixture(t, guest, 0x1000)
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			if mode == "cancelled" {
				k.proc.onTokenInspect = cancel
			} else if mode == "fatal" {
				k.proc.tokenErr = ErrFatal
			} else {
				k.proc.tokenErr = ErrDenied
			}
			p, err := f.PreparePreflight(ctx)
			if err == nil || p == nil || (mode == "fatal" && !errors.Is(err, ErrFatal)) {
				t.Fatal("failed inspection lost owner or fatal classification")
			}
			k.proc.closeErr = ErrUnconfirmed
			if err := p.Close(testContext(t)); !errors.Is(err, ErrUnconfirmed) {
				t.Fatal("token close failure was swallowed")
			}
			k.proc.closeErr = nil
			err = p.Close(testContext(t))
			if errors.Is(err, ErrUnconfirmed) || mode != "fatal" && err != nil || mode == "fatal" && !errors.Is(err, ErrFatal) {
				t.Fatal(err)
			}
			if k.proc.closes != 2 || k.scope.closes != 1 || k.svc.closes != 1 {
				t.Fatal("partial owner lost or unrelated refs closed twice")
			}
		})
	}
}

func TestNoProcessQueryDoesNotAcquireTokenObjectHandle(t *testing.T) {
	f, k := preflightFixture(t)
	p, err := f.PreparePreflight(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if k.proc.tokenInspects != 0 {
		t.Fatal("non-query policy unnecessarily acquired token")
	}
	if err := p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProcessTokenInspectionCannotOutliveConcurrentRevocation(t *testing.T) {
	f, k := queryTokenFixture(t, guest, 0x1000)
	p, err := f.PreparePreflight(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	k.proc.onTokenInspect = func() { close(entered); <-release }
	finished := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		binding, err := p.Binding(ctx)
		if binding != (ProcessBinding{}) {
			finished <- errors.New("revoked binding returned")
			return
		}
		finished <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("token inspection did not start")
	}
	cleanup, end := context.WithTimeout(ctx, 5*time.Millisecond)
	defer end()
	if err := p.Close(cleanup); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("inflight query treated as cleanup complete")
	}
	unblock()
	if err := <-finished; err == nil {
		t.Fatal("inflight token inspection revived after Close")
	}
	k.proc.onTokenInspect = nil
	if err := p.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.proc.closes != 1 {
		t.Fatal("same token/process owner was not retained")
	}
}
