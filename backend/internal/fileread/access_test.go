package fileread

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func selectedFixtureRequest() selectedRequest {
	return selectedRequest{binding: readBinding{principal: [32]byte{1}, host: [32]byte{2}, connection: [32]byte{3}, selection: [32]byte{4}},
		path: "documents/report.txt", maxBytes: maxBytes}
}

func permissionFixture(req selectedRequest) readPermission {
	return readPermission{allowed: req, rootIdentity: [32]byte{5}, until: time.Now().Add(time.Minute),
		recheck: func(context.Context, readAccess) error { return nil }}
}

func TestSelectedPathDefaultDeny(t *testing.T) {
	for _, path := range []string{"", "/file", "../file", "a/../file", "a/./file", "a//file", "a/", ".git/config", "a/.env", "a\\file", "a:file", "a\x00file", "日本語.txt", "with space.txt", "SECRETS.txt", "credential.json", "passwords", "Token.txt", "id_rsa", "KEYCHAIN.db", "a/server.PEM", "cert.key", strings.Repeat("a", 256), strings.Repeat("a/", maxComponents) + "b", strings.Repeat("abcdefghi/", 103) + "b"} {
		t.Run(path, func(t *testing.T) {
			if permittedPath(path) {
				t.Fatal("unsafe or unsupported path accepted")
			}
		})
	}
	for _, path := range []string{"file", "documents/report.txt", "src/read.go", "a-b_C.2"} {
		if !permittedPath(path) {
			t.Fatalf("ordinary fixture path refused: %q", path)
		}
	}
}

func TestReadPermissionChecksExactIndependentBinding(t *testing.T) {
	req := selectedFixtureRequest()
	p := permissionFixture(req)
	calls := 0
	p.recheck = func(_ context.Context, got readAccess) error {
		calls++
		if got.request != req || got.rootIdentity != p.rootIdentity || got.resolved {
			t.Fatal("provider received a different access")
		}
		return nil
	}
	access := readAccess{request: req, rootIdentity: p.rootIdentity}
	if err := p.check(deadline(t), access); err != nil || calls != 1 {
		t.Fatalf("valid check: %v, calls %d", err, calls)
	}
	for _, change := range []func(*readAccess){
		func(a *readAccess) { a.request.binding.principal[0]++ },
		func(a *readAccess) { a.request.binding.host[0]++ },
		func(a *readAccess) { a.request.binding.connection[0]++ },
		func(a *readAccess) { a.request.binding.selection[0]++ },
		func(a *readAccess) { a.rootIdentity[0]++ },
		func(a *readAccess) { a.request.path = "other.txt" },
		func(a *readAccess) { a.request.offset++ },
		func(a *readAccess) { a.request.maxBytes-- },
		func(a *readAccess) { a.request.expectedVersion[0]++ },
	} {
		wrong := access
		change(&wrong)
		if err := p.check(deadline(t), wrong); !errors.Is(err, errDenied) || calls != 1 {
			t.Fatalf("mismatch reached provider or was allowed: %v, calls %d", err, calls)
		}
	}
}

func TestReadPermissionRefusesMissingExpiryDeadlineAndProvider(t *testing.T) {
	req := selectedFixtureRequest()
	for _, change := range []func(*readPermission){
		func(p *readPermission) { p.recheck = nil },
		func(p *readPermission) { p.until = time.Time{} },
		func(p *readPermission) { p.until = time.Now().Add(-time.Second) },
		func(p *readPermission) { p.rootIdentity = [32]byte{} },
		func(p *readPermission) { p.allowed.binding.principal = [32]byte{} },
		func(p *readPermission) { p.allowed.binding.host = [32]byte{} },
		func(p *readPermission) { p.allowed.binding.connection = [32]byte{} },
		func(p *readPermission) { p.allowed.binding.selection = [32]byte{} },
		func(p *readPermission) { p.allowed.offset = -1 },
		func(p *readPermission) { p.allowed.maxBytes = 0 },
		func(p *readPermission) { p.allowed.maxBytes = maxBytes + 1 },
		func(p *readPermission) { p.allowed.path = "../file" },
	} {
		p := permissionFixture(req)
		change(&p)
		if err := p.check(deadline(t), readAccess{request: p.allowed, rootIdentity: p.rootIdentity}); !errors.Is(err, errDenied) {
			t.Fatalf("invalid permission: %v", err)
		}
	}
	p := permissionFixture(req)
	access := readAccess{request: req, rootIdentity: p.rootIdentity}
	for _, ctx := range []context.Context{nil, context.Background()} {
		if err := p.check(ctx, access); !errors.Is(err, errInvalid) {
			t.Fatalf("no deadline: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	p.recheck = func(context.Context, readAccess) error { cancel(); return nil }
	if err := p.check(ctx, access); !errors.Is(err, context.Canceled) {
		t.Fatalf("provider cancellation: %v", err)
	}
	p.recheck = func(context.Context, readAccess) error { return errors.New("private-provider-detail") }
	if err := p.check(deadline(t), access); err != errDenied {
		t.Fatalf("provider error exposed: %v", err)
	}
}

func TestReadPermissionResolvedTargetMustBeValid(t *testing.T) {
	req := selectedFixtureRequest()
	p := permissionFixture(req)
	h, r := fixture([]byte("fixture"))
	_ = h
	access := readAccess{request: req, rootIdentity: p.rootIdentity, resolved: true, target: r.expected}
	if err := p.check(deadline(t), access); err != nil {
		t.Fatal(err)
	}
	access.target = observation{}
	if err := p.check(deadline(t), access); err != errDenied {
		t.Fatalf("empty resolved target: %v", err)
	}
	access.target, access.resolved = r.expected, false
	if err := p.check(deadline(t), access); err != errDenied {
		t.Fatalf("unresolved target contradiction: %v", err)
	}
}
