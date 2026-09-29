package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/service"
)

// ExecRunner runs services as real operating system processes.
type ExecRunner struct{}

// NewExecRunner returns a runner backed by os/exec.
func NewExecRunner() *ExecRunner { return &ExecRunner{} }

var _ Runner = (*ExecRunner)(nil)

// Start implements Runner.
func (*ExecRunner) Start(_ context.Context, spec service.Spec) (Handle, error) {
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("%w: service %q has no command", service.ErrInvalidSpec, spec.Name)
	}

	// exec.CommandContext is deliberately avoided: its cancellation kills the
	// process outright, which would race the manager's terminate-then-kill
	// sequence and deny services a clean shutdown.
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = mergeEnv(os.Environ(), spec.Env)
	platform.PrepareProcess(cmd)

	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		closePipe(stdoutReader, stdoutWriter)
		closePipe(stderrReader, stderrWriter)
		return nil, fmt.Errorf("start service %q: %w", spec.Name, err)
	}

	// The service is contained before it can spawn anything it could later
	// outlive: on Windows this is a Job Object holding the whole tree, on
	// Unix a no-op next to the process group from PrepareProcess.
	job, err := platform.NewJob()
	if err != nil {
		_ = platform.Kill(cmd.Process)
		closePipe(stdoutReader, stdoutWriter)
		closePipe(stderrReader, stderrWriter)
		return nil, fmt.Errorf("start service %q: %w", spec.Name, err)
	}
	if err := job.Assign(cmd.Process); err != nil {
		_ = platform.Kill(cmd.Process)
		_ = job.Close()
		closePipe(stdoutReader, stdoutWriter)
		closePipe(stderrReader, stderrWriter)
		return nil, fmt.Errorf("start service %q: %w", spec.Name, err)
	}

	return &execHandle{
		cmd:       cmd,
		pid:       cmd.Process.Pid,
		started:   time.Now(),
		stdout:    stdoutReader,
		stdoutEnd: stdoutWriter,
		stderr:    stderrReader,
		stderrEnd: stderrWriter,
		job:       job,
		done:      make(chan struct{}),
	}, nil
}

// Signal implements Runner.
func (*ExecRunner) Signal(ctx context.Context, handle Handle, grace time.Duration) error {
	h, ok := handle.(*execHandle)
	if !ok {
		return fmt.Errorf("%w: %T", ErrUnsupportedHandle, handle)
	}
	if h.finished() {
		return nil
	}
	if err := h.terminate(); err != nil {
		// The process may have exited between the check above and the signal.
		if h.finished() || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("terminate pid %d: %w", h.pid, err)
	}
	if grace <= 0 {
		return h.hardStop(ctx)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-h.done:
		return nil
	case <-timer.C:
		return h.hardStop(ctx)
	case <-ctx.Done():
		return errors.Join(ctx.Err(), h.hardStop(ctx))
	}
}

// Wait implements Runner.
func (*ExecRunner) Wait(_ context.Context, handle Handle) (ExitInfo, error) {
	h, ok := handle.(*execHandle)
	if !ok {
		return ExitInfo{}, fmt.Errorf("%w: %T", ErrUnsupportedHandle, handle)
	}
	return h.wait(), nil
}

// execHandle is the Handle produced by ExecRunner.
type execHandle struct {
	cmd     *exec.Cmd
	pid     int
	started time.Time

	stdout    *io.PipeReader
	stdoutEnd *io.PipeWriter
	stderr    *io.PipeReader
	stderrEnd *io.PipeWriter

	// job holds the service's process tree. It is closed once the direct
	// child is reaped, which on Windows terminates any grandchildren still
	// holding inherited pipe handles — without this, Wait below would block
	// until they exit on their own.
	job platform.Job

	done chan struct{}
	once sync.Once
	exit ExitInfo
}

// PID implements Handle.
func (h *execHandle) PID() int { return h.pid }

// Stdout implements Handle.
func (h *execHandle) Stdout() io.ReadCloser { return h.stdout }

// Stderr implements Handle.
func (h *execHandle) Stderr() io.ReadCloser { return h.stderr }

func (h *execHandle) finished() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// wait reaps the process exactly once, no matter how many callers ask.
func (h *execHandle) wait() ExitInfo {
	h.once.Do(func() {
		err := h.cmd.Wait()
		info := ExitInfo{Started: h.started, At: time.Now(), Uptime: time.Since(h.started)}
		if state := h.cmd.ProcessState; state != nil {
			info.Code, info.Signaled = platform.ExitStatus(state)
		}
		// A non-zero exit is an outcome, not an infrastructure failure, so it
		// is recorded as a code rather than an error.
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			info.Err = err
		}
		h.exit = info
		// Closing our own write ends gives the manager's reader goroutines
		// their EOF, which is what lets teardown be observed rather than
		// assumed. Closing the job first reaps grandchildren that inherited
		// the pipes, so Wait cannot block on output that will never end.
		_ = h.job.Close()
		closePipe(nil, h.stdoutEnd)
		closePipe(nil, h.stderrEnd)
		close(h.done)
	})
	return h.exit
}

func (h *execHandle) terminate() error {
	if h.cmd.Process == nil {
		return nil
	}
	return platform.Terminate(h.cmd.Process)
}

// hardStop kills the process tree and waits for it to be reaped, so that a
// caller which returns from Signal knows nothing of the old process survives.
//
// The job is closed before waiting rather than after reaping: grandchildren
// inherit the operating system pipes behind stdout and stderr, and Wait
// cannot return while those pipes are open. Killing only the direct child
// would deadlock here and orphan the tree holding the service's ports.
func (h *execHandle) hardStop(ctx context.Context) error {
	if h.cmd.Process != nil {
		// Best effort: the job close below is the guarantee, and a process
		// that already exited reports an error for being killed again.
		_ = platform.Kill(h.cmd.Process)
	}
	_ = h.job.Close()
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func closePipe(closer io.Closer, other io.Closer) {
	if closer != nil {
		_ = closer.Close()
	}
	if other != nil {
		_ = other.Close()
	}
}
