//go:build darwin && cgo

package residentpipe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	ownerRecordName     = "resident-owner.json"
	ownerRecordLockName = "resident-owner.lock"
	ownerRecordMaxBytes = 4 * 1024
)

type recordPlace struct {
	dir         string
	bootSession func() (string, error)
}

type ownerRecord struct {
	lock            *os.File
	dir             string
	boot            string
	unresolvedBoot  string
	previousCleanup string
}

type ownerRecordData struct {
	Version        int    `json:"version"`
	State          string `json:"state"`
	Boot           string `json:"boot"`
	UnresolvedBoot string `json:"unresolvedBoot"`
}

type ownerRecordWire struct {
	Version        *int    `json:"version"`
	State          *string `json:"state"`
	Boot           *string `json:"boot"`
	UnresolvedBoot *string `json:"unresolvedBoot"`
}

func currentBootSession() (string, error) {
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(boot) == "" {
		return "", errors.New("empty boot session id")
	}
	return boot, nil
}

func openOwnerRecord(place recordPlace) (*ownerRecord, error) {
	if place.dir == "" {
		return nil, errors.New("empty resident owner record directory")
	}
	lockPath := filepath.Join(place.dir, ownerRecordLockName)
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open resident owner lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	closeLock := func(cause error) (*ownerRecord, error) {
		return nil, errors.Join(cause, lock.Close())
	}
	info, err := lock.Stat()
	if err != nil {
		return closeLock(fmt.Errorf("stat resident owner lock: %w", err))
	}
	if !info.Mode().IsRegular() {
		return closeLock(errors.New("resident owner lock is not a regular file"))
	}
	if err := lock.Chmod(0o600); err != nil {
		return closeLock(fmt.Errorf("protect resident owner lock: %w", err))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return closeLock(fmt.Errorf("lock resident owner record: %w", err))
	}
	if place.bootSession == nil {
		return closeLock(errors.New("missing boot session provider"))
	}
	boot, err := place.bootSession()
	if err != nil || strings.TrimSpace(boot) == "" {
		if err == nil {
			err = errors.New("empty boot session id")
		}
		return closeLock(fmt.Errorf("read boot session id: %w", err))
	}

	previousUnknown := false
	previous, readErr := readOwnerRecord(filepath.Join(place.dir, ownerRecordName))
	if errors.Is(readErr, os.ErrNotExist) {
		// No previous owner record means a first start.
	} else if readErr != nil {
		previousUnknown = true
	} else {
		previousUnknown = previous.UnresolvedBoot == boot || (previous.State == "open" && previous.Boot == boot)
	}
	unresolvedBoot := ""
	previousCleanup := "confirmed"
	if previousUnknown {
		unresolvedBoot = boot
		previousCleanup = "unknown"
	}
	record := &ownerRecord{
		lock:            lock,
		dir:             place.dir,
		boot:            boot,
		unresolvedBoot:  unresolvedBoot,
		previousCleanup: previousCleanup,
	}
	if err := record.write("open"); err != nil {
		return nil, errors.Join(fmt.Errorf("persist open resident owner record: %w", err), record.release())
	}
	return record, nil
}

func readOwnerRecord(path string) (ownerRecordData, error) {
	var empty ownerRecordData
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return empty, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		return empty, errors.Join(err, f.Close())
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return empty, errors.Join(errors.New("resident owner record is not a private regular file"), f.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(f, ownerRecordMaxBytes+1))
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return empty, err
	}
	if len(data) == 0 || len(data) > ownerRecordMaxBytes {
		return empty, errors.New("resident owner record has invalid size")
	}
	if err := rejectDuplicateRecordFields(data); err != nil {
		return empty, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire ownerRecordWire
	if err := decoder.Decode(&wire); err != nil {
		return empty, fmt.Errorf("decode resident owner record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return empty, errors.New("resident owner record has trailing data")
	}
	if wire.Version == nil || wire.State == nil || wire.Boot == nil || wire.UnresolvedBoot == nil {
		return empty, errors.New("resident owner record is missing a field")
	}
	record := ownerRecordData{Version: *wire.Version, State: *wire.State, Boot: *wire.Boot, UnresolvedBoot: *wire.UnresolvedBoot}
	if record.Version != 1 || strings.TrimSpace(record.Boot) == "" || (record.State != "open" && record.State != "clean") || (record.UnresolvedBoot != "" && strings.TrimSpace(record.UnresolvedBoot) == "") {
		return empty, errors.New("resident owner record has invalid fields")
	}
	return record, nil
}

func rejectDuplicateRecordFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("resident owner record is not an object")
	}
	seen := make(map[string]struct{}, 4)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("resident owner record has a non-string key")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("resident owner record repeats field %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for _, required := range []string{"version", "state", "boot", "unresolvedBoot"} {
		if _, ok := seen[required]; !ok {
			return fmt.Errorf("resident owner record is missing field %q", required)
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("resident owner record has trailing data")
	}
	return nil
}

func (record *ownerRecord) write(state string) error {
	data, err := json.Marshal(ownerRecordData{
		Version:        1,
		State:          state,
		Boot:           record.boot,
		UnresolvedBoot: record.unresolvedBoot,
	})
	if err != nil {
		return err
	}
	return writeOwnerRecord(record.dir, data)
}

func writeOwnerRecord(dir string, data []byte) error {
	if len(data) == 0 || len(data) > ownerRecordMaxBytes {
		return errors.New("resident owner record has invalid size")
	}
	temp, err := os.CreateTemp(dir, ".resident-owner-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		_ = os.Remove(tempName)
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if err := writeAll(temp, data); err != nil {
		return err
	}
	if _, err := unix.FcntlInt(temp.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		return fmt.Errorf("full sync resident owner record: %w", err)
	}
	if err := temp.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	path := filepath.Join(dir, ownerRecordName)
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := syncOwnerDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	readback, err := readOwnerRecordBytes(path)
	if err != nil {
		return fmt.Errorf("read back resident owner record: %w", err)
	}
	if !bytes.Equal(readback, data) {
		return errors.New("resident owner record readback differs")
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readOwnerRecordBytes(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.Join(errors.New("resident owner record is not a private regular file"), f.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(f, ownerRecordMaxBytes+1))
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > ownerRecordMaxBytes {
		return nil, errors.New("resident owner record has invalid size")
	}
	return data, nil
}

func syncOwnerDirectory(directory *os.File) error {
	_, fullSyncErr := unix.FcntlInt(directory.Fd(), unix.F_FULLFSYNC, 0)
	if fullSyncErr == nil {
		return nil
	}
	if !errors.Is(fullSyncErr, unix.EINVAL) && !errors.Is(fullSyncErr, unix.ENOTSUP) && !errors.Is(fullSyncErr, unix.ENOTTY) {
		return fullSyncErr
	}
	return directory.Sync()
}

func (record *ownerRecord) previousCleanupValue() string {
	if record != nil && record.previousCleanup == "unknown" {
		return "unknown"
	}
	return "confirmed"
}

func (record *ownerRecord) markClean() error {
	if record == nil {
		return errors.New("missing resident owner record")
	}
	return record.write("clean")
}

func (record *ownerRecord) release() error {
	if record == nil || record.lock == nil {
		return nil
	}
	lock := record.lock
	record.lock = nil
	return lock.Close()
}
