//go:build windows

package platform

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var errLockContention = errors.New("lock contention")

func lockFile(file *os.File) error {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_SHARING_VIOLATION {
		return errLockContention
	}
	return err
}

func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
