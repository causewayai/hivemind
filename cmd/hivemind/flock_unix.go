//go:build !windows

package main

import (
	"os"
	"syscall"
)

func daemonExeName() string { return "hivemindd" }

func detachAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// acquireLock takes an exclusive advisory lock on path (creating it), returning
// an unlock func.
func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
