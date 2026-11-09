package residentmac

import (
	"context"
	"errors"
	"testing"
	"time"
)

func enrollmentFixture() enrollmentAccount {
	return enrollmentAccount{uid: 501, euid: 501, gid: 20, egid: 20, recordUID: "501", home: "/Users/fixture"}
}

func TestEnrolledPolicySelectsOnlyFixedCurrentAccountLocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, home := range []string{"/Users/fixture", "/Users/fixture space", "/Volumes/Accounts/試験"} {
		a := enrollmentFixture()
		a.home = home
		got, err := selectEnrolledPolicy(ctx, a)
		if err != nil || got != home+"/Library/Application Support/com.o2connective.rescue-host.shell/resident-policy.json" {
			t.Fatalf("fixed account-relative selection: %q %v", got, err)
		}
	}
}

func TestEnrolledPolicyRequiresLiveFiniteContext(t *testing.T) {
	canceled, cancel := context.WithTimeout(context.Background(), time.Minute)
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, ctx := range []context.Context{nil, context.Background(), canceled, expired} {
		if got, err := selectEnrolledPolicy(ctx, enrollmentFixture()); got != "" || !errors.Is(err, ErrStandbyUnavailable) {
			t.Fatalf("invalid context selected policy: %q %v", got, err)
		}
	}
}

func TestEnrolledPolicyRefusesUnknownRootOrChangedAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, kind := range []string{"unknown", "root", "euid", "gid", "egid", "record", "empty-record", "noncanonical-record"} {
		t.Run(kind, func(t *testing.T) {
			a := enrollmentFixture()
			switch kind {
			case "unknown":
				a.uid, a.euid = -1, -1
			case "root":
				a.uid, a.euid, a.recordUID = 0, 0, "0"
			case "euid":
				a.euid = 0
			case "gid":
				a.gid, a.egid = -1, -1
			case "egid":
				a.egid++
			case "record":
				a.recordUID = "502"
			case "empty-record":
				a.recordUID = ""
			case "noncanonical-record":
				a.recordUID = "0501"
			}
			if got, err := selectEnrolledPolicy(ctx, a); got != "" || !errors.Is(err, ErrStandbyUnavailable) {
				t.Fatalf("unbound account selected policy: %q %v", got, err)
			}
		})
	}
}

func TestEnrolledPolicyRefusesInvalidHomeWithoutFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, home := range []string{"", "/", "relative", "~/fixture", "/Users/fixture/", "/Users/../fixture", "//Users/fixture", "/Users/fixture\x00other", "/Users/fixture\nother", "/Users/fixture\rother"} {
		a := enrollmentFixture()
		a.home = home
		if got, err := selectEnrolledPolicy(ctx, a); got != "" || !errors.Is(err, ErrStandbyUnavailable) {
			t.Fatalf("invalid home selected policy: %q %v", got, err)
		}
	}
}

func TestEnrolledPolicyNeverUsesHomeOrConfigEnvironment(t *testing.T) {
	t.Setenv("HOME", "/untrusted/home")
	t.Setenv("XDG_CONFIG_HOME", "/untrusted/config")
	t.Setenv("USER", "untrusted")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	got, err := selectEnrolledPolicy(ctx, enrollmentFixture())
	if err != nil || got != "/Users/fixture/Library/Application Support/com.o2connective.rescue-host.shell/resident-policy.json" {
		t.Fatalf("environment replaced account selection: %q %v", got, err)
	}
}

func TestEnrolledStandbyPreflightNeverReadsWithoutFiniteContext(t *testing.T) {
	// This rejects before account lookup, policy/key reads or any owner exists.
	for _, ctx := range []context.Context{nil, context.Background()} {
		if got, err := LoadEnrolledStandby(ctx); got != nil || !errors.Is(err, ErrStandbyUnavailable) {
			t.Fatal("non-finite preparation admitted")
		}
	}
}

// Invalid contexts are rejected before native account lookup or protected reads.
func TestEnrolledStandbyRejectsInvalidContextBeforeLookup(t *testing.T) {
	canceled, cancel := context.WithTimeout(context.Background(), time.Minute)
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, ctx := range []context.Context{nil, context.Background(), canceled, expired} {
		owner, err := LoadEnrolledStandby(ctx)
		if owner != nil || !errors.Is(err, ErrStandbyUnavailable) {
			t.Fatal("invalid context reached enrolled standby", err)
		}
	}
}
