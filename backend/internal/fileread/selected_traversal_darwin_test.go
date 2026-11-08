package fileread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Mutations occur inside the last permission callback before the next child
// lookup. These are persistent anonymous-fixture changes, not a simulation of
// every concurrent rename interleaving or of a hostile filesystem.
func TestSelectedOwnedRechecksChainBeforeNextChild(t *testing.T) {
	for _, change := range []string{"root-move", "parent-move", "parent-replace", "parent-symlink", "parent-mode", "root-child"} {
		t.Run(change, func(t *testing.T) {
			path, root, req, p := folderFixture(t, []byte("anonymous fixture"))
			outside := t.TempDir()
			if change == "parent-mode" {
				t.Cleanup(func() {
					if err := os.Chmod(filepath.Join(path, "documents"), 0700); err != nil {
						t.Error(err)
					}
				})
			}
			mutated := false
			lookups, opens, checks := 0, 0, 0
			var acquired []int
			acquire := selectedAcquisition{
				lookup: func(parent int, name string) (nativeState, error) {
					if mutated {
						lookups++
					}
					return inspectName(parent, name)
				},
				open: func(parent int, name string, flags int, mode uint32) (int, error) {
					if mutated {
						opens++
					}
					fd, err := unix.Openat(parent, name, flags, mode)
					if err == nil {
						acquired = append(acquired, fd)
					}
					return fd, err
				},
			}
			p.recheck = func(_ context.Context, access readAccess) error {
				if access.resolved {
					t.Fatal("mutated traversal resolved a target")
				}
				checks++
				when := 3 // after parent acquisition, before the leaf lookup
				if change == "root-move" {
					when = 2 // no child has been acquired yet
				}
				if checks != when {
					return nil
				}
				parent := filepath.Join(path, "documents")
				var err error
				switch change {
				case "root-move":
					err = os.Rename(path, filepath.Join(outside, "moved"))
				case "parent-move", "parent-replace", "parent-symlink":
					err = os.Rename(parent, filepath.Join(outside, "moved"))
					if err == nil && change == "parent-replace" {
						err = os.Mkdir(parent, 0700)
					}
					if err == nil && change == "parent-symlink" {
						err = os.Symlink(filepath.Join(outside, "moved"), parent)
					}
				case "parent-mode":
					err = os.Chmod(parent, 0500)
				case "root-child":
					err = os.Mkdir(filepath.Join(path, "sibling"), 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
				mutated = true
				return nil
			}
			out, err := readSelectedAcquiring(deadline(t), root, p, req, acquire)
			requireSelectedRefusal(t, root, out, err)
			if !mutated || !errors.Is(err, errChanged) || lookups != 0 || opens != 0 {
				t.Fatalf("continued after persistent mutation: changed=%v lookup=%d open=%d error=%v", mutated, lookups, opens, err)
			}
			want := 1
			if change == "root-move" {
				want = 0
			}
			if len(acquired) != want {
				t.Fatalf("acquired %d children before refusal, want %d", len(acquired), want)
			}
			for _, fd := range acquired {
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
					t.Fatalf("acquired child was not closed: %v", err)
				}
			}
		})
	}
}

func TestSelectedOwnedMissingAcquisitionConsumesRoot(t *testing.T) {
	for _, acquire := range []selectedAcquisition{{lookup: inspectName}, {open: unix.Openat}} {
		_, root, req, p := folderFixture(t, nil)
		out, err := readSelectedAcquiring(deadline(t), root, p, req, acquire)
		requireSelectedRefusal(t, root, out, err)
		if err != errInvalid {
			t.Fatalf("missing acquisition accepted: %v", err)
		}
	}
}
