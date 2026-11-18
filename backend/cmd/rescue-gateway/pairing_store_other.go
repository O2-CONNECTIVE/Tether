//go:build !unix

package main

import (
	"context"
	"errors"
	"io"
)

func openNativeRegistry(ctx context.Context, path string, diagnostic io.Writer) (registry, error) {
	return openRegistryWith(ctx, path, registryOps{diagnostic: diagnostic})
}

func openRegistryWith(context.Context, string, registryOps) (registry, error) {
	return nil, errors.New("rescue-gateway: native pairing registry is unsupported on this platform")
}
