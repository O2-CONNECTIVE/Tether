//go:build !darwin

package fileread

import (
	"context"
	"errors"
	"os"
)

type selectedResult struct {
	result
	observed observation
}

// Unsupported platforms consume the supplied handle but never inspect or read
// any child. There is no shell or generic path fallback.
func readSelectedOwned(_ context.Context, root *os.File, _ readPermission, _ selectedRequest) (selectedResult, error) {
	if root == nil {
		return selectedResult{}, errInvalid
	}
	if root.Close() != nil {
		return selectedResult{}, errors.Join(errUnsupported, errCleanup)
	}
	return selectedResult{}, errUnsupported
}
