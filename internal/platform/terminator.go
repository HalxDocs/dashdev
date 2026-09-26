package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// ErrTerminated reports that the user asked dashdev to stop.
var ErrTerminated = errors.New("termination requested")

// Terminator waits for an operating system request to terminate dashdev.
type Terminator interface {
	// Wait blocks until the process is asked to terminate by the operating
	// system, the context is done, or the terminator is closed. The returned
	// error wraps ErrTerminated when a signal caused the return.
	Wait(ctx context.Context) error
	// Close stops listening for signals and unblocks Wait.
	Close()
}

// NewTerminator returns a Terminator backed by the operating system's
// interrupt and termination signals.
func NewTerminator() Terminator { return newTerminator() }

type osTerminator struct {
	signals chan os.Signal
	closed  chan struct{}
}

func newTerminator() *osTerminator {
	t := &osTerminator{
		signals: make(chan os.Signal, 1),
		closed:  make(chan struct{}),
	}
	notify(t.signals)
	return t
}

func (t *osTerminator) Wait(ctx context.Context) error {
	select {
	case sig := <-t.signals:
		return errors.Join(ErrTerminated, errors.New(sig.String()))
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return nil
	}
}

func (t *osTerminator) Close() {
	stopNotify(t.signals)
	close(t.closed)
}

// PrepareProcess configures a command so that it can later be terminated
// together with any processes it spawned.
func PrepareProcess(cmd *exec.Cmd) { prepareProcess(cmd) }

// Terminate asks a process and the group it leads to stop gracefully. On
// platforms that cannot deliver a graceful stop request it is equivalent to
// Kill.
func Terminate(proc *os.Process) error { return terminate(proc) }

// Kill stops a process and the group it leads immediately.
func Kill(proc *os.Process) error { return kill(proc) }
