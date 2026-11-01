// Package agentkeys reads one existing, operator-placed OpenSSH Ed25519 private
// key for the current (non-root, non-elevated) account and returns it as an
// ssh.Signer. It never generates, writes, moves, re-protects or discovers keys,
// and it never logs key bytes, paths or identities.
//
// See docs/agent-key-loader.md for the caller contract and the exact threat
// boundary. Source and fake-kernel tests are not real-key or real-OS evidence.
package agentkeys

import (
	"context"
	"errors"
	"runtime"

	"golang.org/x/crypto/ssh"
)

var (
	// ErrConfig: the trusted caller supplied an unusable Config or context.
	// Nothing was opened.
	ErrConfig = errors.New("agentkeys: invalid trusted key configuration")
	// ErrUnsupported: this build cannot inspect the platform's effective
	// protection, so it refuses instead of trusting mode bits or paths.
	ErrUnsupported = errors.New("agentkeys: no native protection inspection for this platform build")
	// ErrDenied: identity, protection, stability or key-format checks failed.
	ErrDenied = errors.New("agentkeys: protected key refused")
	// ErrCleanup: at least one held handle could not be confirmed closed. No
	// signer is returned and the caller should treat the process state as
	// uncertain rather than retrying in a loop.
	ErrCleanup = errors.New("agentkeys: key handle cleanup unconfirmed")
)

// Config is chosen by the local trusted composition (the process owner's own
// launcher). It must not be derived from remote commands, HTTP headers,
// environment variables, configuration documents received over a network, or
// default home-directory discovery.
type Config struct {
	// Path is the canonical absolute path of an existing private key file:
	// "/Users/<name>/..." on Darwin, "C:\Users\<name>\..." on Windows. Every
	// component is opened without following links, so it must already be the
	// real path. Windows also requires the on-disk case (final path match);
	// Darwin does not check case.
	Path string
	// Fingerprint is the independently recorded "SHA256:..." public-key
	// fingerprint (ssh.FingerprintSHA256), not a hash of the file.
	Fingerprint string
}

// LoadCurrentUser opens cfg.Path once for the current account, verifies the
// account, the complete path and the opened file's protection and identity,
// reads at most 16 KiB, re-validates the same handles, closes them, and only
// then parses and returns the signer. ctx must carry a deadline; native file
// I/O is synchronous, so the deadline is checked between steps, not enforced
// inside a blocked system call.
//
// The returned signer holds private key material in Go memory for the
// caller's lifetime; Go cannot guarantee that every copy is erased.
func LoadCurrentUser(ctx context.Context, cfg Config) (ssh.Signer, error) {
	return load(ctx, nativeStyle, nativeKernel, cfg)
}

type readOutcome struct {
	data []byte
	err  error
}

func load(ctx context.Context, style pathStyle, newKernel func() (kernel, error), cfg Config) (ssh.Signer, error) {
	if ctx == nil {
		return nil, ErrConfig
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, ErrConfig
	}
	nodes, ok := style.nodes(cfg.Path)
	if !ok || !validFingerprint(cfg.Fingerprint) {
		return nil, ErrConfig
	}
	if ctx.Err() != nil {
		return nil, ErrDenied
	}
	data, err := readProtected(ctx, style, newKernel, nodes)
	if err != nil {
		return nil, err
	}
	defer wipe(data)
	signer, err := parseSigner(data, cfg.Fingerprint, func() bool { return ctx.Err() == nil })
	if err != nil {
		return nil, ErrDenied
	}
	return signer, nil
}

func readProtected(ctx context.Context, style pathStyle, newKernel func() (kernel, error), nodes []node) ([]byte, error) {
	k, err := newKernel()
	if err != nil || k == nil {
		return nil, ErrUnsupported
	}
	// Identity checks and every open/read/close run on one locked OS thread.
	// If that thread's identity is found changed, the goroutine exits still
	// locked so the runtime discards the thread instead of reusing it.
	done := make(chan readOutcome, 1)
	go func() {
		runtime.LockOSThread()
		data, keepLocked, err := readKey(ctx, k, style, nodes)
		if !keepLocked {
			runtime.UnlockOSThread()
		}
		done <- readOutcome{data: data, err: err}
	}()
	out := <-done
	return out.data, out.err
}

// wipe is best effort: it clears this slice's backing array, not copies made
// by the Go runtime or by library code.
func wipe(b []byte) {
	clear(b[:cap(b)])
}
