package fileread

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Every filesystem target is created by this test. No user-selected or existing
// personal folder is opened; ordinary directory handles are only fixture input.
func folderFixture(t *testing.T, data []byte) (string, *os.File, selectedRequest, readPermission) {
	t.Helper()
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, "documents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "documents/report.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Guard resources if setup or an assertion fails before ownership transfer.
	t.Cleanup(func() {
		if _, err := root.Stat(); err == nil {
			_ = root.Close()
		}
	})
	state, err := inspectNative(int(root.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	req := selectedFixtureRequest()
	permission := permissionFixture(req)
	permission.rootIdentity = state.identity()
	return path, root, req, permission
}

func requireConsumed(t *testing.T, root *os.File) {
	t.Helper()
	if _, err := root.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("root ownership not consumed: %v", err)
	}
}

func requireSelectedRefusal(t *testing.T, root *os.File, out selectedResult, err error) {
	t.Helper()
	if err == nil || !reflect.DeepEqual(out, selectedResult{}) {
		t.Fatalf("failure leaked result: %+v %v", out, err)
	}
	requireConsumed(t, root)
}

func TestSelectedOwnedReadsBoundedAnonymousBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []byte
		offset int64
		limit  int
		want   []byte
		eof    bool
	}{
		{"binary", []byte{1, 0, 0xff, 2}, 1, 2, []byte{0, 0xff}, false},
		{"empty", nil, 0, 1, nil, true},
		{"end", []byte("abc"), 3, 10, nil, true},
		{"partial", []byte("abc"), 1, 10, []byte("bc"), true},
		{"bounded", bytes.Repeat([]byte{7}, maxBytes+1), 0, maxBytes, bytes.Repeat([]byte{7}, maxBytes), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, root, req, p := folderFixture(t, tc.data)
			req.offset, req.maxBytes = tc.offset, tc.limit
			p.allowed = req
			unresolved, resolved := 0, 0
			p.recheck = func(_ context.Context, a readAccess) error {
				if a.request != req || a.rootIdentity != p.rootIdentity {
					t.Fatal("lost binding")
				}
				if a.resolved {
					resolved++
				} else {
					unresolved++
				}
				return nil
			}
			out, err := readSelectedOwned(deadline(t), root, p, req)
			if err != nil || !bytes.Equal(out.data, tc.want) || out.eof != tc.eof || out.next != tc.offset+int64(len(tc.want)) || out.observed.kind != regularFile || resolved < 3 || unresolved < 1 {
				t.Fatalf("read: %+v %v checks %d/%d", out, err, unresolved, resolved)
			}
			requireConsumed(t, root)
		})
	}
}

func TestSelectedOwnedRejectsPolicyBeforeTraversal(t *testing.T) {
	for _, fault := range []string{"denied", "missing-provider", "wrong-path", "wrong-root", "expired", "version", "offset", "nil-context"} {
		t.Run(fault, func(t *testing.T) {
			path, root, req, p := folderFixture(t, []byte("fixture"))
			ctx := deadline(t)
			calls := 0
			p.recheck = func(_ context.Context, a readAccess) error {
				calls++
				if fault == "denied" {
					return errors.New("private")
				}
				return nil
			}
			switch fault {
			case "missing-provider":
				p.recheck = nil
			case "wrong-path":
				req.path = "not-allowed.txt"
			case "wrong-root":
				p.rootIdentity[0]++
			case "expired":
				p.until = time.Now().Add(-time.Second)
			case "version":
				req.expectedVersion = [32]byte{8}
				p.allowed = req
			case "offset":
				req.offset = 100
				p.allowed = req
			case "nil-context":
				ctx = nil
			}
			if fault == "denied" {
				// Even an unsafe child never gets inspected/opened: initial
				// provider refusal wins over the filesystem failure classification.
				if err := os.Remove(filepath.Join(path, req.path)); err != nil {
					t.Fatal(err)
				}
			}
			out, err := readSelectedOwned(ctx, root, p, req)
			requireSelectedRefusal(t, root, out, err)
			if fault == "denied" && (err != errDenied || calls != 1) {
				t.Fatalf("late denial: %v calls%d", err, calls)
			}
		})
	}
	out, err := readSelectedOwned(deadline(t), nil, readPermission{}, selectedRequest{})
	if err != errInvalid || !reflect.DeepEqual(out, selectedResult{}) {
		t.Fatalf("nil ownership: %v", err)
	}
}

func TestSelectedOwnedRefusesSymlinkHardlinkAndSpecialFiles(t *testing.T) {
	for _, fault := range []string{"leaf-symlink", "parent-symlink", "hardlink", "fifo", "directory", "writable-parent", "writable-root"} {
		t.Run(fault, func(t *testing.T) {
			path, root, req, p := folderFixture(t, []byte("fixture"))
			leaf := filepath.Join(path, req.path)
			switch fault {
			case "leaf-symlink":
				if err := os.Rename(leaf, filepath.Join(path, "original")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../original", leaf); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				if err := os.Rename(filepath.Join(path, "documents"), filepath.Join(path, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("moved", filepath.Join(path, "documents")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(leaf, filepath.Join(path, "alias")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(leaf, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(leaf, 0700); err != nil {
					t.Fatal(err)
				}
			case "writable-parent":
				if err := os.Chmod(filepath.Dir(leaf), 0777); err != nil {
					t.Fatal(err)
				}
			case "writable-root":
				if err := os.Chmod(path, 0777); err != nil {
					t.Fatal(err)
				}
			}
			out, err := readSelectedOwned(deadline(t), root, p, req)
			requireSelectedRefusal(t, root, out, err)
		})
	}
}

func TestSelectedOwnedRefusesChangesAfterResolvingTarget(t *testing.T) {
	for _, change := range []string{"write", "leaf-rename", "parent-rename", "hardlink", "symlink-replacement", "revoke", "cancel", "expiry"} {
		t.Run(change, func(t *testing.T) {
			path, root, req, p := folderFixture(t, []byte("fixture"))
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			done := false
			p.recheck = func(_ context.Context, a readAccess) error {
				if !a.resolved || done {
					return nil
				}
				done = true
				leaf := filepath.Join(path, req.path)
				var err error
				switch change {
				case "write":
					err = os.WriteFile(leaf, []byte("changed-longer"), 0600)
				case "leaf-rename":
					err = os.Rename(leaf, leaf+".moved")
				case "parent-rename":
					err = os.Rename(filepath.Dir(leaf), filepath.Join(path, "moved"))
				case "hardlink":
					err = os.Link(leaf, filepath.Join(path, "alias"))
				case "symlink-replacement":
					err = os.Rename(leaf, leaf+".moved")
					if err == nil {
						err = os.Symlink("report.txt.moved", leaf)
					}
				case "revoke":
					return errors.New("revoked")
				case "cancel":
					cancel()
				case "expiry":
					time.Sleep(15 * time.Millisecond)
				}
				if err != nil {
					t.Fatal(err)
				}
				return nil
			}
			if change == "expiry" {
				p.until = time.Now().Add(10 * time.Millisecond)
			}
			out, err := readSelectedOwned(ctx, root, p, req)
			requireSelectedRefusal(t, root, out, err)
		})
	}
}

func TestSelectedOwnedRefusesRootMoveDuringLookup(t *testing.T) {
	path, root, req, p := folderFixture(t, []byte("fixture"))
	parent := t.TempDir()
	calls := 0
	p.recheck = func(context.Context, readAccess) error {
		calls++
		if calls == 2 {
			if err := os.Rename(path, filepath.Join(parent, "moved")); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	out, err := readSelectedOwned(deadline(t), root, p, req)
	requireSelectedRefusal(t, root, out, err)
	// This observed persistent rename is not a proof of continuous containment
	// or of detection for a change/restore between checkpoints.
}

func TestSelectedOwnedClosesAfterPartialLookupAndCleanupFailure(t *testing.T) {
	path, root, req, p := folderFixture(t, []byte("fixture"))
	if err := os.Remove(filepath.Join(path, req.path)); err != nil {
		t.Fatal(err)
	}
	out, err := readSelectedOwned(deadline(t), root, p, req)
	requireSelectedRefusal(t, root, out, err)

	_, root, req, p = folderFixture(t, nil)
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	out, err = readSelectedOwned(deadline(t), root, p, req)
	if !errors.Is(err, errCleanup) {
		t.Fatalf("cleanup unknown lost: %v", err)
	}
	requireSelectedRefusal(t, root, out, err)

	// Exercise aggregate close: one failed close must not skip other handles.
	a, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Open(filepath.Join(path, "documents"))
	if err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	h := &selectedHandle{files: []*os.File{a, b}}
	if err := h.close(); err != errCleanup {
		t.Fatalf("aggregate close: %v", err)
	}
	requireConsumed(t, a)
}

func TestSelectedNativeStateRejectsOtherOwnerDeviceAndMount(t *testing.T) {
	base := nativeState{device: 1, uid: 100, mode: unix.S_IFDIR | 0700, links: 1, fsid: unix.Fsid{Val: [2]int32{1, 2}}}
	leaf := base
	leaf.mode = unix.S_IFREG | 0600
	if !leaf.safeChild(base, true) {
		t.Fatal("fixture not regular")
	}
	for _, change := range []func(*nativeState){
		func(s *nativeState) { s.uid++ }, func(s *nativeState) { s.device++ },
		func(s *nativeState) { s.fsid.Val[0]++ }, func(s *nativeState) { s.links = 2 },
		func(s *nativeState) { s.links = 0 }, func(s *nativeState) { s.mode = unix.S_IFCHR },
	} {
		bad := leaf
		change(&bad)
		if bad.safeChild(base, true) {
			t.Fatal("unsafe native state accepted")
		}
	}
	// These metadata hashes are not content hashes: same stamps can report
	// equality despite unobserved byte mutations.
	if leaf.observation() != leaf.observation() {
		t.Fatal("unstable metadata hash")
	}
}
