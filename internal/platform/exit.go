package platform

import "os"

// ExitStatus decodes a process state into an exit code and whether the process
// was terminated by a signal.
func ExitStatus(state *os.ProcessState) (code int, signaled bool) {
	return exitStatus(state)
}
