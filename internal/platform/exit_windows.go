//go:build windows

package platform

import "os"

// Windows has no signals, so a process either exits on its own or is killed.
func exitStatus(state *os.ProcessState) (int, bool) {
	if state == nil {
		return 0, false
	}
	return state.ExitCode(), false
}
