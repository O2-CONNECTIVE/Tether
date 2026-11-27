package grantstore_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/grantstore"
)

// 合成 tempdir adapter。実 crash、fsync、OS 間 atomic replace、ACL/真正性の証明ではない。
// production driver は存在しない。writer marker をエラー時に残す契約だけを観測する。
type tempStore struct {
	dir                             string
	failPhase                       grantstore.Phase
	after, half                     bool
	commits                         []grantstore.Phase
	beforeRelease, beforeQuarantine func()
	afterCommit                     func(grantstore.Phase)
}

func newTempStore(t *testing.T, phase grantstore.Phase) *tempStore {
	t.Helper()
	s := &tempStore{dir: t.TempDir()}
	r, err := grantstore.InitialRecord(grant())
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = phase
	r.Revision = uint64(phase)
	data, err := grantstore.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "record"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

type tempLease struct{ owner *tempStore }

func (s *tempStore) Acquire(ctx context.Context) (grantstore.Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "writer-claim"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &tempLease{s}, nil
}

func (l *tempLease) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(l.owner.dir, "record"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, grantstore.RecordSize+1))
}

func (l *tempLease) Commit(ctx context.Context, expected uint64, data []byte) error {
	old, err := l.Read(ctx)
	if err != nil {
		return err
	}
	previous, err := grantstore.DecodeRecord(old)
	if err != nil {
		return err
	}
	next, err := grantstore.DecodeRecord(data)
	if err != nil {
		return err
	}
	if previous.Revision != expected || next.Revision != expected+1 {
		return errors.New("synthetic CAS conflict")
	}
	s := l.owner
	s.commits = append(s.commits, next.Phase)
	fail := next.Phase == s.failPhase
	if fail && !s.after && !s.half {
		return errors.New("synthetic interruption before write")
	}
	if fail && s.half {
		data = data[:len(data)/2]
	}
	if err := os.WriteFile(filepath.Join(s.dir, "record"), data, 0600); err != nil {
		return err
	}
	if s.afterCommit != nil {
		s.afterCommit(next.Phase)
	}
	if fail {
		return errors.New("synthetic interruption after write")
	}
	return nil
}

func (l *tempLease) Release() error {
	if l.owner.beforeRelease != nil {
		l.owner.beforeRelease()
	}
	return os.Remove(filepath.Join(l.owner.dir, "writer-claim"))
}

func (l *tempLease) Quarantine() error {
	if l.owner.beforeQuarantine != nil {
		l.owner.beforeQuarantine()
	}
	// marker は消さない。実装資源は Acquire 内で既に閉じた test adapter。
	return nil
}
