//go:build darwin && cgo

package residentmac

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>

static int tether_sleep_assertion_create(IOPMAssertionID *id) {
	return (int)IOPMAssertionCreateWithName(
		kIOPMAssertPreventUserIdleSystemSleep,
		kIOPMAssertionLevelOn,
		CFSTR("Tether resident work"),
		id);
}

static int tether_sleep_assertion_release(IOPMAssertionID id) {
	return (int)IOPMAssertionRelease(id);
}
*/
import "C"

import (
	"fmt"
	"sync"
)

type darwinSleepAssertion struct {
	mu   sync.Mutex
	id   C.IOPMAssertionID
	held bool
}

// The OS owns this assertion and releases it when the process exits, including
// abnormal termination; normal paths still call Release explicitly.
func newSleepAssertion() (sleepAssertion, error) { return &darwinSleepAssertion{}, nil }

func (s *darwinSleepAssertion) Take() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held {
		return nil
	}
	var id C.IOPMAssertionID
	if result := C.tether_sleep_assertion_create(&id); result != 0 {
		return fmt.Errorf("residentmac: IOPMAssertionCreateWithName failed: 0x%x", uint32(result))
	}
	s.id, s.held = id, true
	return nil
}

func (s *darwinSleepAssertion) Release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.held {
		return nil
	}
	if result := C.tether_sleep_assertion_release(s.id); result != 0 {
		return fmt.Errorf("residentmac: IOPMAssertionRelease failed: 0x%x", uint32(result))
	}
	s.id, s.held = 0, false
	return nil
}
