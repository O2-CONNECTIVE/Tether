//go:build darwin && !cgo

package macagent

// No alternate API or signal fallback for a native observer unavailable build.
func (d *darwinProcess) observeOwnedGroup() groupObservation { return groupQueryFailed }
