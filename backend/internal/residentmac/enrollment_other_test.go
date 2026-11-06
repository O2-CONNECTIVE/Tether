//go:build !darwin || !cgo || osusergo

package residentmac

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEnrolledPolicyUnsupportedBuildNeverSelectsLocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if path, err := currentEnrolledPolicy(ctx); path != "" || !errors.Is(err, ErrStandbyUnavailable) {
		t.Fatal("unsupported account source selected enrollment")
	}
	if owner, err := loadEnrolledStandby(ctx); owner != nil || !errors.Is(err, ErrStandbyUnavailable) {
		t.Fatal("unsupported account source loaded owner")
	}
}
