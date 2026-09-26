// Package processtest provides a Runner that answers to the test instead of to
// the operating system.
//
// It lives in a normal package rather than a _test.go file because the manager,
// the dashboard and the command line all need to exercise the lifecycle without
// launching real processes. Behaviour that matters to the manager is modelled
// faithfully: Signal completes the process (or kills a stubborn one), Wait reaps
// it, and the output pipes produce EOF at the right moment.
package processtest

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/HalxDocs/dashdev/internal/process"
	"github.com/HalxDocs/dashdev/internal/service"
)

// Handle is a process that exists only in memory.
type Handle struct {
	spec service.Spec
	pid  int

	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	stderrReader *io.PipeReader
	stderrWriter *io.PipeWriter

	// Stubborn makes the handle ignore a graceful stop request, so a test can
	// exercise the kill path.
	Stubborn bool

	mu       sync.Mutex
	exited   bool
	info     process.ExitInfo
	done     chan struct{}
	signals  []time.Time
	killUsed bool
}

// PID implements process.Handle.
func (h *Handle) PID() int { return h.pid }

// Stdout implements process.Handle.
func (h *Handle) Stdout() io.ReadCloser { return h.stdoutReader }

// Stderr implements process.Handle.
func (h *Handle) Stderr() io.ReadCloser { return h.stderrReader }

// Spec returns the definition this handle was started from.
func (h *Handle) Spec() service.Spec { return h.spec }

// Exit makes the process end by itself.
func (h *Handle) Exit(code int) { h.finish(code, false) }

// Crash makes the process end with a non-zero code.
func (h *Handle) Crash(code int) { h.finish(code, false) }

// Signaled makes the process end as if it had been killed.
func (h *Handle) Signaled(signal int) { h.finish(signal, true) }

// WriteStdout writes output as if the process had printed it.
func (h *Handle) WriteStdout(text string) { _, _ = io.WriteString(h.stdoutWriter, text) }

// WriteStderr writes output as if the process had written to its error stream.
func (h *Handle) WriteStderr(text string) { _, _ = io.WriteString(h.stderrWriter, text) }

// Exited reports whether the process has been reaped.
func (h *Handle) Exited() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exited
}

// KillUsed reports whether the process had to be killed rather than asked to
// stop.
func (h *Handle) KillUsed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killUsed
}

// StopRequests reports how many times a graceful stop was requested.
func (h *Handle) StopRequests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.signals)
}

func (h *Handle) finish(code int, signaled bool) {
	h.mu.Lock()
	if h.exited {
		h.mu.Unlock()
		return
	}
	h.exited = true
	now := time.Now()
	h.info = process.ExitInfo{Code: code, Signaled: signaled, Started: now, At: now}
	h.mu.Unlock()

	// Closing the write ends is what gives the manager's readers their EOF, so
	// this ordering mirrors a real process exiting.
	_ = h.stdoutWriter.Close()
	_ = h.stderrWriter.Close()
	close(h.done)
}

func (h *Handle) markKilled() {
	h.mu.Lock()
	h.killUsed = true
	h.mu.Unlock()
}

// Runner is a process.Runner driven entirely by the test.
type Runner struct {
	mu    sync.Mutex
	procs []*Handle
	// StartErr, when set, is returned by every Start.
	StartErr error
	// SignalErr, when set, is returned by every Signal after the process has
	// been completed.
	SignalErr error
	// SignalDelay postpones the completion of a stop request, which lets a test
	// observe the manager while a service is on its way down.
	SignalDelay time.Duration
	nextPID     int
}

var _ process.Runner = (*Runner)(nil)

// NewRunner returns a runner with no processes.
func NewRunner() *Runner { return &Runner{nextPID: 1000} }

// Start implements process.Runner.
func (r *Runner) Start(_ context.Context, spec service.Spec) (process.Handle, error) {
	if r.StartErr != nil {
		return nil, r.StartErr
	}
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	r.mu.Lock()
	r.nextPID++
	handle := &Handle{
		spec:         spec,
		pid:          r.nextPID,
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		stderrReader: stderrReader,
		stderrWriter: stderrWriter,
		done:         make(chan struct{}),
	}
	r.procs = append(r.procs, handle)
	r.mu.Unlock()
	return handle, nil
}

// Signal implements process.Runner: it asks the process to stop and, if the
// handle is stubborn, kills it once the grace period has elapsed.
func (r *Runner) Signal(ctx context.Context, handle process.Handle, grace time.Duration) error {
	h, ok := handle.(*Handle)
	if !ok {
		return errors.Join(process.ErrUnsupportedHandle, errors.New("processtest: foreign handle"))
	}
	if h.Exited() {
		return r.SignalErr
	}

	h.mu.Lock()
	h.signals = append(h.signals, time.Now())
	h.mu.Unlock()

	if r.SignalDelay > 0 {
		select {
		case <-time.After(r.SignalDelay):
		case <-ctx.Done():
		}
	}

	if !h.Stubborn {
		h.finish(0, false)
		return r.SignalErr
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-h.done:
	case <-timer.C:
		h.markKilled()
		h.finish(137, true)
	case <-ctx.Done():
		h.markKilled()
		h.finish(137, true)
		return ctx.Err()
	}
	return r.SignalErr
}

// Wait implements process.Runner.
func (r *Runner) Wait(_ context.Context, handle process.Handle) (process.ExitInfo, error) {
	h, ok := handle.(*Handle)
	if !ok {
		return process.ExitInfo{}, errors.Join(process.ErrUnsupportedHandle, errors.New("processtest: foreign handle"))
	}
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info, nil
}

// Started returns the handles in start order.
func (r *Runner) Started() []*Handle {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Handle(nil), r.procs...)
}

// StartedFor returns the handles started from a service definition, in order.
func (r *Runner) StartedFor(name string) []*Handle {
	var out []*Handle
	for _, handle := range r.Started() {
		if handle.spec.Name == name {
			out = append(out, handle)
		}
	}
	return out
}

// Unexited reports how many started processes have not been reaped. A test that
// reaches the end of a run with a non-zero count has found a leaked process,
// which is exactly the failure mode a process manager must not have.
func (r *Runner) Unexited() int {
	count := 0
	for _, handle := range r.Started() {
		if !handle.Exited() {
			count++
		}
	}
	return count
}

// WaitForStarted blocks until count processes have been started, and reports
// whether that happened before the timeout.
func (r *Runner) WaitForStarted(count int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(r.Started()) >= count {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return len(r.Started()) >= count
}
