//go:build !darwin

package fileread

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestSelectedOwnedUnsupportedConsumesRoot(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := readSelectedOwned(deadline(t), root, readPermission{}, selectedRequest{})
	if err != errUnsupported || !reflect.DeepEqual(out, selectedResult{}) {
		t.Fatalf("unsupported: %+v %v", out, err)
	}
	// Windows Stat on a closed directory can return ERROR_INVALID_HANDLE,
	// not os.ErrClosed. Check the invalid descriptor sentinel used by the
	// tested Unix/Windows Go implementations, not an arbitrary Stat error.
	if root.Fd() != ^uintptr(0) {
		t.Fatal("root ownership not consumed")
	}
	_, err = readSelectedOwned(deadline(t), root, readPermission{}, selectedRequest{})
	if !errors.Is(err, errUnsupported) || !errors.Is(err, errCleanup) {
		t.Fatalf("cleanup unknown lost: %v", err)
	}
}
