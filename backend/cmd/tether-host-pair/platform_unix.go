//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var errPairBusy = errors.New("pair busy")
var errPairUnsupported = errors.New("pair unsupported")
var pairLock *os.File
var flockPair = syscall.Flock

func lockPair(dir string) error {
	f, e := os.OpenFile(filepath.Join(dir, "pair.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if e = flockPair(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		if e == syscall.EWOULDBLOCK {
			return errPairBusy
		}
		return e
	}
	info, e := f.Stat()
	if e == nil && info.Size() == 0 {
		_, e = f.WriteString("tether-host-pair lock\n")
		if e == nil {
			e = f.Sync()
		}
	}
	if e != nil {
		_ = flockPair(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return e
	}
	pairLock = f
	return nil
}
func unlockPair() {
	if pairLock != nil {
		_ = flockPair(int(pairLock.Fd()), syscall.LOCK_UN)
		_ = pairLock.Close()
		pairLock = nil
	}
}
