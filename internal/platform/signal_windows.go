//go:build windows

package platform

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/windows"
)

func notify(ch chan<- os.Signal) { signal.Notify(ch, os.Interrupt) }

func stopNotify(ch chan<- os.Signal) { signal.Stop(ch) }

// prepareProcess gives the child its own process group. Windows has no
// equivalent of SIGTERM, so this exists to keep the option of a console control
// event open and to keep the shape of the code identical across platforms.
func prepareProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// terminate is honest about the platform: Windows cannot deliver a catchable
// shutdown request to an arbitrary child process, so a graceful stop degrades
// to an immediate one. The manager still runs its grace sequence, which simply
// observes the process already gone.
func terminate(proc *os.Process) error { return proc.Kill() }

func kill(proc *os.Process) error { return proc.Kill() }
