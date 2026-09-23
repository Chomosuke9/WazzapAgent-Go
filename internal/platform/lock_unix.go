//go:build !windows

package platform

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errLockContention = errors.New("lock contention")

func lockFile(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return errLockContention
		}
		return err
	}
	return nil
}

func unlockFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
