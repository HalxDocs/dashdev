//go:build !windows

package platform

import (
	"os"
	"syscall"
)

func exitStatus(state *os.ProcessState) (int, bool) {
	if state == nil {
		return 0, false
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return int(ws.Signal()), true
	}
	return state.ExitCode(), false
}
