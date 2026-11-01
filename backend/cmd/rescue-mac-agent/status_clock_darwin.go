//go:build darwin && cgo

package main

/*
#include <stdint.h>
#include <mach/mach_time.h>
static int tether_status_millis(uint64_t *out) {
 mach_timebase_info_data_t tb;
 if (mach_timebase_info(&tb) != KERN_SUCCESS || tb.denom == 0 || tb.numer == 0) return 0;
 uint64_t ticks=mach_continuous_time();
 __uint128_t value=(__uint128_t)ticks*tb.numer/tb.denom/1000000;
 if (value > 9007199254740991ULL) return 0;
 *out=(uint64_t)value; return 1;
}
*/
import "C"

// Same boot-relative, sleep-inclusive kernel clock as the local Swift viewer.
func statusContinuousMillis() (uint64, bool) {
	var v C.uint64_t
	ok := C.tether_status_millis(&v)
	return uint64(v), ok == 1
}
