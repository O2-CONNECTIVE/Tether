//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var errPairBusy = errors.New("pair busy")
var pairLock *os.File

func lockPair(dir string) error {
	f, e := os.OpenFile(filepath.Join(dir, "pair.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		if e == syscall.EWOULDBLOCK || e == syscall.EAGAIN {
			return errPairBusy
		}
		return e
	}
	pairLock = f
	return nil
}
func unlockPair() {
	if pairLock != nil {
		_ = syscall.Flock(int(pairLock.Fd()), syscall.LOCK_UN)
		_ = pairLock.Close()
		pairLock = nil
	}
}
