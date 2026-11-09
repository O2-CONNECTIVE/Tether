package permissionmodel

import (
	"strings"
	"testing"
)

func fixture(mode Mode) (Request, HostPolicy, AccountPolicy) {
	b := Binding{Host: "host-1", User: "user-1", Client: "client-1"}
	r := Request{Binding: b, Mode: mode, Operation: Execute, HostRevision: 7}
	l := Limits{Binding: b, Mode: mode, Revision: 7, Actions: Actions(Execute | ReadResult), ValidFromMillis: 900, ValidUntilMillis: 1100}
	h := HostPolicy{Limits: l}
	var a AccountPolicy
	if mode == Persistent {
		r.AccountRevision = 9
		l.Revision = 9
		a = AccountPolicy{Limits: l}
	}
	return r, h, a
}

func TestModeIntersection(t *testing.T) {
	for _, mode := range []Mode{Temporary, Persistent} {
		r, h, a := fixture(mode)
		if got := Assess(1000, r, h, a); got != (Decision{WithinBounds, BoundsSatisfied}) {
			t.Fatalf("mode %v: %+v", mode, got)
		}
		h.Actions = Actions(ReadResult)
		if got := Assess(1000, r, h, a); got.Outcome != Denied || got.Reason != OperationExcluded {
			t.Fatalf("host exclusion: %+v", got)
		}
	}
	r, h, a := fixture(Persistent)
	a.Actions = Actions(ReadResult)
	if got := Assess(1000, r, h, a); got != (Decision{Denied, OperationExcluded}) {
		t.Fatalf("account exclusion: %+v", got)
	}
}

func TestRejectMalformedInput(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Request, *HostPolicy, *AccountPolicy)
		now    int64
	}{
		{"zero request", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { *r = Request{} }, 1000},
		{"missing host", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { *h = HostPolicy{} }, 1000},
		{"missing account", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { *a = AccountPolicy{} }, 1000},
		{"zero clock", func(_ *Request, _ *HostPolicy, _ *AccountPolicy) {}, 0},
		{"negative clock", func(_ *Request, _ *HostPolicy, _ *AccountPolicy) {}, -1},
		{"unknown mode", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.Mode = Mode(255) }, 1000},
		{"empty operation", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.Operation = 0 }, 1000},
		{"multiple operations", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.Operation = Execute | ReadResult }, 1000},
		{"unknown operation", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.Operation = Operation(1 << 15) }, 1000},
		{"zero host revision", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.HostRevision = 0 }, 1000},
		{"zero account revision", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.AccountRevision = 0 }, 1000},
		{"unknown host action", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Actions |= Actions(1 << 15) }, 1000},
		{"unknown account action", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.Actions |= Actions(1 << 15) }, 1000},
		{"empty host actions", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Actions = 0 }, 1000},
		{"host empty window", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.ValidUntilMillis = h.ValidFromMillis }, 1000},
		{"account reversed window", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.ValidUntilMillis = 800 }, 1000},
		{"host missing window", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.ValidFromMillis = 0 }, 1000},
		{"host zero revision", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Revision = 0 }, 1000},
		{"account zero revision", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.Revision = 0 }, 1000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, h, a := fixture(Persistent)
			c.change(&r, &h, &a)
			if got := Assess(c.now, r, h, a); got != (Decision{Denied, InvalidInput}) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestBindingRevisionAndWindowDenials(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Request, *HostPolicy, *AccountPolicy)
		reason Reason
	}{
		{"host identity", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Binding.Host = "host-2" }, BindingMismatch},
		{"user identity", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Binding.User = "user-2" }, BindingMismatch},
		{"client identity", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Binding.Client = "client-2" }, BindingMismatch},
		{"account host", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.Binding.Host = "host-2" }, BindingMismatch},
		{"account user", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.Binding.User = "user-2" }, BindingMismatch},
		{"account client", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.Binding.Client = "client-2" }, BindingMismatch},
		{"host stale", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.HostRevision-- }, RevisionMismatch},
		{"host future", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.HostRevision++ }, RevisionMismatch},
		{"account stale", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.AccountRevision-- }, RevisionMismatch},
		{"account future", func(r *Request, _ *HostPolicy, _ *AccountPolicy) { r.AccountRevision++ }, RevisionMismatch},
		{"host revoked", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Revoked = true }, Revoked},
		{"account revoked", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.Revoked = true }, Revoked},
		{"host future window", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.ValidFromMillis = 1001 }, OutsideWindow},
		{"account future window", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.ValidFromMillis = 1001 }, OutsideWindow},
		{"host expired", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.ValidUntilMillis = 1000 }, OutsideWindow},
		{"account expired", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.ValidUntilMillis = 1000 }, OutsideWindow},
		{"host mode", func(_ *Request, h *HostPolicy, _ *AccountPolicy) { h.Mode = Temporary }, ModeMismatch},
		{"account mode", func(_ *Request, _ *HostPolicy, a *AccountPolicy) { a.Mode = Temporary }, ModeMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, h, a := fixture(Persistent)
			c.change(&r, &h, &a)
			if got := Assess(1000, r, h, a); got != (Decision{Denied, c.reason}) {
				t.Fatalf("%+v", got)
			}
		})
	}
	for _, mode := range []Mode{Temporary, Persistent} {
		r, h, a := fixture(mode)
		if got := Assess(900, r, h, a); got.Outcome != WithinBounds {
			t.Fatal(got)
		}
		if got := Assess(1100, r, h, a); got.Outcome != Denied {
			t.Fatal(got)
		}
	}
}

func TestNoModeDowngradeOrImplicitPromotion(t *testing.T) {
	r, h, a := fixture(Persistent)
	r.Mode = Temporary
	r.AccountRevision = 0
	a = AccountPolicy{}
	if got := Assess(1000, r, h, a); got != (Decision{Denied, ModeMismatch}) {
		t.Fatal(got)
	}
	r, h, a = fixture(Temporary)
	_, _, account := fixture(Persistent)
	if got := Assess(1000, r, h, account); got != (Decision{Denied, InvalidInput}) {
		t.Fatal(got)
	}
	r.AccountRevision = 1
	if got := Assess(1000, r, h, a); got != (Decision{Denied, InvalidInput}) {
		t.Fatal(got)
	}
	r, h, a = fixture(Temporary)
	r.Operation = ManageSharing
	h.Actions = Actions(ManageSharing)
	if got := Assess(1000, r, h, a); got != (Decision{Denied, OperationExcluded}) {
		t.Fatal(got)
	}
}

func TestIDsAreExactBoundedOpaqueValues(t *testing.T) {
	for _, id := range []string{"", " ", "user@example.com", "a/b", "a\x00b", "日本語", strings.Repeat("a", 129)} {
		r, h, a := fixture(Temporary)
		r.Binding.User = UserID(id)
		h.Binding.User = UserID(id)
		if got := Assess(1000, r, h, a); got != (Decision{Denied, InvalidInput}) {
			t.Fatalf("invalid ID accepted: %+v", got)
		}
	}
	r, h, a := fixture(Temporary)
	r.Binding.User = UserID(strings.Repeat("a", 128))
	h.Binding.User = r.Binding.User
	if got := Assess(1000, r, h, a); got.Outcome != WithinBounds {
		t.Fatal(got)
	}
	r, h, a = fixture(Temporary)
	r.Binding.User = "User-1"
	if got := Assess(1000, r, h, a); got != (Decision{Denied, BindingMismatch}) {
		t.Fatal(got)
	}
}

func TestPermissionIntersectionExhaustiveAndValueOnly(t *testing.T) {
	operations := []Operation{DiscoverHost, ReadStatus, ReadResult, Execute, Cancel, ManageSharing}
	for host := Actions(1); host <= allActions; host++ {
		for account := Actions(1); account <= allActions; account++ {
			for _, op := range operations {
				r, h, a := fixture(Persistent)
				r.Operation = op
				h.Actions = host
				a.Actions = account
				beforeR, beforeH, beforeA := r, h, a
				got := Assess(1000, r, h, a)
				want := host&Actions(op) != 0 && account&Actions(op) != 0
				if (got.Outcome == WithinBounds) != want {
					t.Fatalf("host=%v account=%v op=%v: %+v", host, account, op, got)
				}
				if r != beforeR || h != beforeH || a != beforeA {
					t.Fatal("input mutated")
				}
			}
		}
	}
}
