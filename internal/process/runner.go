package process

import (
	"context"
	"io"
	"strconv"
	"time"

	"github.com/HalxDocs/dashdev/internal/service"
)

// ExitInfo describes how a process finished.
type ExitInfo struct {
	// Code is the exit code, or the signal number when Signaled is true.
	Code int
	// Signaled reports that the process was killed rather than exiting.
	Signaled bool
	// Started and At bracket the process's lifetime.
	Started time.Time
	At      time.Time
	Uptime  time.Duration
	// Err is set only when the exit could not be observed, which is a defect
	// in dashdev rather than a failure of the service.
	Err error
}

// Success reports whether the process ended cleanly and on purpose.
func (e ExitInfo) Success() bool {
	return e.Err == nil && !e.Signaled && e.Code == 0
}

// String implements fmt.Stringer.
func (e ExitInfo) String() string {
	switch {
	case e.Err != nil:
		return "unobservable exit: " + e.Err.Error()
	case e.Signaled:
		return "killed by signal " + strconv.Itoa(e.Code)
	default:
		return "exit code " + strconv.Itoa(e.Code)
	}
}

// Handle is a started process. Handles are created by a Runner and are only
// meaningful to the Runner that produced them.
type Handle interface {
	// PID reports the process identifier.
	PID() int
	// Stdout and Stderr return the streams to read the process's output from.
	// The reader observes EOF once the process has been waited on.
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
}

// Runner starts and controls local processes. It is the seam that allows the
// manager to be tested without launching real processes.
type Runner interface {
	// Start launches spec and returns a handle to it.
	Start(ctx context.Context, spec service.Spec) (Handle, error)
	// Signal asks the process, and the group it leads, to stop. It waits up to
	// grace for a clean exit and then kills the process. Signal returns once
	// the process has exited, so a caller that waits for it knows the old
	// process is fully gone before starting a replacement.
	Signal(ctx context.Context, handle Handle, grace time.Duration) error
	// Wait blocks until the process exits and reports how it ended. It also
	// closes the process's output streams so readers observe EOF.
	Wait(ctx context.Context, handle Handle) (ExitInfo, error)
}
