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
	if _, err := root.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("root not closed: %v", err)
	}
	_, err = readSelectedOwned(deadline(t), root, readPermission{}, selectedRequest{})
	if !errors.Is(err, errUnsupported) || !errors.Is(err, errCleanup) {
		t.Fatalf("cleanup unknown lost: %v", err)
	}
}
