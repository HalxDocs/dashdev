//go:build !windows

package platform

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func notify(ch chan<- os.Signal) {
	// SIGINT and SIGTERM both mean "stop dashdev"; SIGHUP is deliberately not
	// handled because a detached terminal should not silently kill services.
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
}

func stopNotify(ch chan<- os.Signal) { signal.Stop(ch) }

// prepareProcess puts the child in its own process group so that a service
// which spawns helpers (a shell running a dev server, for example) can be
// stopped as a unit instead of leaking orphans.
func prepareProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func terminate(proc *os.Process) error {
	if err := syscall.Kill(-proc.Pid, syscall.SIGTERM); err != nil {
		// The group may already be gone, or the child may not have become a
		// group leader. Fall back to signalling the process itself.
		return errors.Join(err, proc.Signal(syscall.SIGTERM))
	}
	return nil
}

func kill(proc *os.Process) error {
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		return errors.Join(err, proc.Kill())
	}
	return nil
}
