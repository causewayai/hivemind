//go:build windows

package main

import (
	"os"
	"syscall"
	"time"
)

func daemonExeName() string { return "hivemindd.exe" }

func signalTerm(p *os.Process) error { return p.Kill() } // no SIGTERM on Windows

func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

// acquireLock: Windows has no flock; exclusive-create with a bounded spin,
// stealing a lock file older than 30s (a crashed holder).
func acquireLock(path string) (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return func() { _ = f.Close(); _ = os.Remove(path) }, nil
		}
		if fi, statErr := os.Stat(path); statErr == nil && time.Since(fi.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
