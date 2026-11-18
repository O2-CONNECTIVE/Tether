//go:build !darwin || !cgo || osusergo

package residentmac

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEnrolledLocationUnsupportedBuildReturnsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	home, policy, err := EnrolledLocation(ctx)
	if home != "" || policy != "" || !errors.Is(err, ErrStandbyUnavailable) {
		t.Fatalf("location=(%q,%q,%v)", home, policy, err)
	}
}
