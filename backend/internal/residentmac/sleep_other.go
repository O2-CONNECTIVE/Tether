//go:build !darwin || !cgo

package residentmac

func newSleepAssertion() (sleepAssertion, error) {
	return nil, ErrSleepPreventionUnsupported
}
