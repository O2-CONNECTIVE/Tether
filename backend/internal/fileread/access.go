package fileread

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	maxPathBytes  = 1024
	maxComponents = 16
)

var errUnsupported = errors.New("fileread: selected-folder reader unavailable")

// These opaque references describe a transient comparison boundary, not a
// credential, wire format, issuer, or persistent grant. The trusted composition
// must bind them to the authenticated principal/host/connection/root selection.
// Neither shell authority nor knowledge of these values grants read access.
type readBinding struct {
	principal, host, connection, selection [32]byte
}

type selectedRequest struct {
	binding         readBinding
	path            string
	offset          int64
	maxBytes        int
	expectedVersion [32]byte // zero means first observation, not a snapshot
}

type readAccess struct {
	request      selectedRequest
	rootIdentity [32]byte
	resolved     bool
	target       observation // present only after opening and checking the target
}

// readPermission is an injected, exact one-request allowlist and live recheck
// port. Constructing this value does not authorize anything. No real provider
// exists here. Unknown paths/ranges/bindings are denied before child lookup.
// The provider must independently approve file content policy; a benign name
// or this allowlist is NOT proof that content contains no secrets.
type readPermission struct {
	allowed      selectedRequest
	rootIdentity [32]byte
	until        time.Time
	recheck      func(context.Context, readAccess) error
}

func (p readPermission) check(ctx context.Context, access readAccess) error {
	if ctx == nil {
		return errInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return errInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.recheck == nil || p.rootIdentity == [32]byte{} || !validBinding(p.allowed.binding) ||
		p.until.IsZero() || !time.Now().Before(p.until) || access.request != p.allowed ||
		access.rootIdentity != p.rootIdentity || !validSelectedRequest(access.request) {
		return errDenied
	}
	if access.resolved && !valid(request{expected: access.target, offset: access.request.offset, maxBytes: access.request.maxBytes}) {
		return errDenied
	}
	if !access.resolved && access.target != (observation{}) {
		return errDenied
	}
	err := p.recheck(ctx, access)
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil || !time.Now().Before(p.until) {
		return errDenied
	}
	return nil
}

func validBinding(b readBinding) bool {
	zero := [32]byte{}
	return b.principal != zero && b.host != zero && b.connection != zero && b.selection != zero
}

func validSelectedRequest(req selectedRequest) bool {
	return req.offset >= 0 && req.maxBytes > 0 && req.maxBytes <= maxBytes && permittedPath(req.path)
}

func permittedPath(path string) bool {
	if len(path) == 0 || len(path) > maxPathBytes {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > maxComponents {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 255 || part[0] == '.' {
			return false
		}
		for _, c := range []byte(part) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
				return false
			}
		}
		name := strings.ToLower(part)
		for _, sensitive := range []string{"secret", "credential", "password", "token", "keychain", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"} {
			if strings.Contains(name, sensitive) {
				return false
			}
		}
		for _, suffix := range []string{".key", ".pem", ".p12", ".pfx", ".jks", ".keystore"} {
			if strings.HasSuffix(name, suffix) {
				return false
			}
		}
	}
	return true
}
