//go:build windows

package serviceready

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OpenProcessToken checks desired token rights separately from process query
// rights. Retain its query-only result in the already-owned process, including
// an error with a partial handle. No token is created, duplicated or elevated.
func (p *nativeProcess) inspectToken(borrowed uintptr) (processTokenState, error) {
	if windows.GetCurrentThreadId() != p.thread {
		return processTokenState{}, ErrFatal
	}
	if p.handle == 0 || borrowed == 0 || borrowed >= ^uintptr(15) {
		return processTokenState{}, ErrDenied
	}
	if p.token == 0 {
		if windows.OpenProcessToken(p.handle, windows.TOKEN_QUERY|windows.READ_CONTROL, &p.token) != nil || p.token == 0 {
			return processTokenState{}, ErrDenied
		}
	}
	before, err := primaryTokenObjectStats(p.token)
	if err != nil {
		return processTokenState{}, err
	}
	security, err := nativeSecurity(windows.Handle(p.token), windows.SE_KERNEL_OBJECT)
	if err != nil {
		return processTokenState{}, err
	}
	borrowedStats, err := primaryTokenObjectStats(windows.Token(borrowed))
	if err != nil {
		return processTokenState{}, err
	}
	// Plane has already established held PID=current PID. The pseudo handle is
	// a fresh view of this process's current primary token, not an owned handle.
	current, err := primaryTokenObjectStats(windows.GetCurrentProcessToken())
	if err != nil {
		return processTokenState{}, err
	}
	after, err := primaryTokenObjectStats(p.token)
	if err != nil || before != after {
		return processTokenState{}, ErrDenied
	}
	return processTokenState{id: before[0], modified: before[1], currentID: current[0], currentModified: current[1], borrowedID: borrowedStats[0], borrowedModified: borrowedStats[1], security: security}, nil
}

func primaryTokenObjectStats(token windows.Token) ([2]uint64, error) {
	var storage [7]uint64
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 56)
	var returned uint32
	if windows.GetTokenInformation(token, windows.TokenStatistics, &data[0], 56, &returned) != nil || returned != 56 || binary.LittleEndian.Uint32(data[24:28]) != windows.TokenPrimary {
		return [2]uint64{}, ErrDenied
	}
	return [2]uint64{binary.LittleEndian.Uint64(data[:8]), binary.LittleEndian.Uint64(data[48:56])}, nil
}
