//go:build !darwin || !cgo || osusergo

package residentmac

import "context"

func currentEnrolledPolicy(context.Context) (string, error) {
	return "", ErrStandbyUnavailable
}
