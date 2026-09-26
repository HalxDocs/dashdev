//go:build !windows

package platform_test

import (
	"os"
	"syscall"
)

// interruptChild delivers an interrupt to a child process, which is what a user
// pressing Ctrl+C in a terminal asks for.
func interruptChild(process *os.Process) error {
	return process.Signal(syscall.SIGINT)
}
