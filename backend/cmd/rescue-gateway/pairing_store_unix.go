//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"golang.org/x/sys/unix"
)

const registryLockContents = "rescue-gateway pairing registry lock\n"

func openNativeRegistry(ctx context.Context, path string, diagnostic io.Writer) (registry, error) {
	return openRegistryWith(ctx, path, registryOps{diagnostic: diagnostic})
}

func openRegistryWith(ctx context.Context, path string, ops registryOps) (registry, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errRegistry
	}
	if ops.readTrust == nil {
		ops.readTrust = agentkeys.ReadTrustConfig
	}
	if ops.rename == nil {
		ops.rename = os.Rename
	}
	if ops.syncDir == nil {
		ops.syncDir = syncRegistryDir
	}

	lockPath := path + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	locked := false
	keepLock := false
	defer func() {
		if !keepLock {
			if locked {
				_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
			}
			_ = lockFile.Close()
		}
	}()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	locked = true

	info, err := lockFile.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		if _, err := lockFile.WriteString(registryLockContents); err != nil {
			return nil, err
		}
		if err := lockFile.Sync(); err != nil {
			return nil, err
		}
	}
	protected, err := readProtectedBytes(ctx, ops.readTrust, lockPath)
	if err != nil {
		return nil, err
	}
	wipe(protected)

	var raw []byte
	if _, err := os.Lstat(path); err == nil {
		raw, err = readProtectedBytes(ctx, ops.readTrust, path)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	store := &nativeRegistry{path: path, lockPath: lockPath, lockFile: lockFile, ops: ops, raw: raw}
	keepLock = true
	return store, nil
}

func readProtectedBytes(ctx context.Context, readTrust func(context.Context, string) ([]byte, error), path string) ([]byte, error) {
	data, err := readTrust(ctx, path)
	if err != nil {
		wipe(data)
		return nil, err
	}
	return data, nil
}

type nativeRegistry struct {
	path     string
	lockPath string
	lockFile *os.File
	ops      registryOps
	raw      []byte
	closed   bool
}

func (r *nativeRegistry) Load() []byte {
	return append([]byte(nil), r.raw...)
}

func (r *nativeRegistry) Save(ctx context.Context, raw []byte) error {
	if r.closed {
		return errRegistry
	}
	protected, err := readProtectedBytes(ctx, r.ops.readTrust, r.lockPath)
	if err != nil {
		return err
	}
	wipe(protected)
	if _, err := os.Lstat(r.path); err == nil {
		current, err := readProtectedBytes(ctx, r.ops.readTrust, r.path)
		if err != nil {
			return err
		}
		wipe(current)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".pairs-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := r.ops.rename(tmpName, r.path); err != nil {
		return err
	}
	committed = true
	wipe(r.raw)
	r.raw = append(r.raw[:0], raw...)
	if err := r.ops.syncDir(dir); err != nil {
		report(r.ops.diagnostic, "登録簿の親ディレクトリを同期できませんでした。登録は確定済みです")
	}
	return nil
}

func (r *nativeRegistry) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	unlockErr := unix.Flock(int(r.lockFile.Fd()), unix.LOCK_UN)
	closeErr := r.lockFile.Close()
	wipe(r.raw)
	r.raw = nil
	return errors.Join(unlockErr, closeErr)
}

func syncRegistryDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(syncErr, closeErr)
}
