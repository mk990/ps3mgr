//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package filelock

import (
	"os"
	"os/exec"
	"syscall"
)

// Open takes an exclusive lock without waiting. Close releases it when the
// last holder exits. Keep the lock file in place so all holders use one inode.
func Open(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// Inherit lets the builder hold the lock if its parent exits unexpectedly.
func Inherit(cmd *exec.Cmd, file *os.File) error {
	cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	return nil
}
