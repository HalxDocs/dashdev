//go:build !windows

package platform

import (
	"errors"
	"syscall"
)

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal zero performs the permission and existence checks without
	// delivering a signal. A process owned by another user is still alive, so
	// EPERM counts as alive rather than gone.
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
