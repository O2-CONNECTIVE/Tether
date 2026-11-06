//go:build darwin && cgo

package residentpipe

/*
#include <mach/mach_time.h>
#include <stdint.h>
static uint64_t resident_continuous_ms(void) {
 mach_timebase_info_data_t info;
 if (mach_timebase_info(&info) != KERN_SUCCESS || info.denom == 0) return UINT64_MAX;
 __uint128_t ns = (__uint128_t)mach_continuous_time() * info.numer / info.denom;
 return (uint64_t)(ns / 1000000);
}
*/
import "C"
import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"time"
)

func continuousMS() (uint64, error) {
	n := uint64(C.resident_continuous_ms())
	if n > MaxInteger {
		return 0, ErrProtocol
	}
	return n, nil
}

type ownedPipes struct{ in, out int }

// Only the native launcher may supply these inherited anonymous FIFO handles.
// FIFO metadata is a consistency check, never authentication of a peer.
func capturePipes() (p *ownedPipes, err error) {
	p = &ownedPipes{in: -1, out: -1}
	owned := p
	defer func() {
		if err != nil {
			owned.close()
		}
	}()
	for _, spec := range []struct {
		fd, mode int
		dst      *int
	}{{0, unix.O_RDONLY, &p.in}, {1, unix.O_WRONLY, &p.out}} {
		var s unix.Stat_t
		if e := unix.Fstat(spec.fd, &s); e != nil {
			return nil, e
		}
		flags, e := unix.FcntlInt(uintptr(spec.fd), unix.F_GETFL, 0)
		if e != nil || s.Mode&unix.S_IFMT != unix.S_IFIFO || flags&unix.O_ACCMODE != spec.mode {
			return nil, ErrProtocol
		}
		fd, e := unix.FcntlInt(uintptr(spec.fd), unix.F_DUPFD_CLOEXEC, 3)
		if e != nil {
			return nil, e
		}
		*spec.dst = fd
		if e = unix.SetNonblock(fd, true); e != nil {
			return nil, e
		}
	}
	// Close original descriptors only after both owned duplicates are established.
	if e := unix.Close(0); e != nil {
		return nil, e
	}
	if e := unix.Close(1); e != nil {
		return nil, e
	}
	return p, nil
}
func (p *ownedPipes) close() {
	if p == nil {
		return
	}
	if p.in >= 0 {
		_ = unix.Close(p.in)
		p.in = -1
	}
	if p.out >= 0 {
		_ = unix.Close(p.out)
		p.out = -1
	}
}
func (p *ownedPipes) writeLine(b []byte) error {
	if len(b) > MaxLine {
		return ErrProtocol
	}
	// One atomic bounded write. Partial output is never retried.
	n, e := unix.Write(p.out, b)
	if e != nil {
		return e
	}
	if n != len(b) {
		return errors.New("partial resident pipe write")
	}
	return nil
}
func (p *ownedPipes) readAvailable(buffer *[]byte) ([]Intent, error) {
	var chunk [MaxLine]byte
	n, e := unix.Read(p.in, chunk[:])
	if e == unix.EAGAIN || e == unix.EWOULDBLOCK {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if n == 0 {
		if len(*buffer) > 0 {
			return nil, ErrProtocol
		}
		return nil, os.ErrClosed
	}
	var out []Intent
	for _, b := range chunk[:n] {
		*buffer = append(*buffer, b)
		if len(*buffer) > MaxLine {
			return nil, ErrProtocol
		}
		if b == '\n' {
			r, e := Decode(*buffer)
			if e != nil {
				return nil, e
			}
			out = append(out, r)
			*buffer = (*buffer)[:0]
		}
	}
	return out, nil
}

// Polling is bounded; the native continuous clock, not this wake interval,
// determines freshness across machine sleep.
const pollInterval = 10 * time.Millisecond
