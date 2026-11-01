package agentkeys

import "context"

// MaxTrustConfigBytes is the bounded public trust document size supported by
// the same protected-file reader. Public values such as issuer/sub mappings
// and host pins still require integrity protection.
const MaxTrustConfigBytes = maxKeyBytes

// ReadTrustConfig reads an explicitly selected local trust document with the
// same parent, owner, ACL, same-handle and cleanup checks as LoadCurrentUser.
// It does not parse JSON or treat the bytes as a key; the caller must strictly
// validate its schema and clear the returned buffer after use. It does not
// discover paths or accept remote-controlled configuration.
// This deliberately requires private-file protection even though the expected
// contents are public, avoiding a second weaker filesystem trust policy.
func ReadTrustConfig(ctx context.Context, path string) ([]byte, error) {
	return readTrustConfig(ctx, nativeStyle, nativeKernel, path)
}

func readTrustConfig(ctx context.Context, style pathStyle, factory func() (kernel, error), path string) ([]byte, error) {
	if ctx == nil {
		return nil, ErrConfig
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, ErrConfig
	}
	nodes, ok := style.nodes(path)
	if !ok {
		return nil, ErrConfig
	}
	if ctx.Err() != nil {
		return nil, ErrDenied
	}
	data, err := readProtected(ctx, style, factory, nodes)
	if err != nil {
		return nil, err
	}
	// The deadline may pass while handles are closed; an expired caller never
	// receives the bytes.
	if ctx.Err() != nil {
		wipe(data)
		return nil, ErrDenied
	}
	return data, nil
}
