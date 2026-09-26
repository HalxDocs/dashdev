//go:build windows

package platform

import "syscall"

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}

	// A handle to a terminated process can still be opened for a short while:
	// the process object survives until the last handle to it is closed, and
	// Go releases its handle through a deferred path after Wait returns. Asking
	// whether the handle exists therefore reports a dead process as alive for
	// a few hundred milliseconds, which is exactly the window a shutdown test
	// looks at.
	//
	// SYNCHRONIZE plus a zero-timeout wait answers the question that matters:
	// a process that has terminated signals its handle, and one that is still
	// running does not.
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION|syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)

	event, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return false
	}
	return event == syscall.WAIT_TIMEOUT
}
