package process

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/service"
)

// run is the owner goroutine. Everything that mutates a service happens here.
func (m *Manager) run(ctx context.Context) error {
	// Worker goroutines are detached from the caller's context: when the user
	// presses Ctrl+C, the cancellation that started the shutdown must not also
	// cut short the graceful stop sequence it triggered.
	tasks, cancelTasks := context.WithCancel(context.WithoutCancel(ctx))
	defer close(m.finished)
	defer cancelTasks()

	states := make(map[string]*serviceState, len(m.specs))
	for _, spec := range m.specs {
		states[spec.Name] = newServiceState(spec)
	}

	// Every service is announced before anything starts, so a subscriber can
	// build its entire view from the event stream and never needs to ask the
	// manager a question.
	now := m.clock.Now()
	for _, spec := range m.specs {
		m.sink.Emit(events.ServiceAdded{Status: states[spec.Name].status, Time: now})
	}

	// Every service that depends on something is marked as waiting before
	// anything starts. Starting inside the same pass would make readiness
	// depend on where a service sits in the file: a dependent declared below
	// its dependency would be started before the mark existed for it, and one
	// declared above would be marked after the only reschedule that could
	// release it had already run.
	for _, name := range m.order {
		st := states[name]
		if len(st.spec.DependsOn) == 0 {
			continue
		}
		st.waiting = true
		m.publish(st, "waiting for dependencies")
	}

	// The services that depend on nothing start immediately; the rest are
	// released by the scheduler as their dependencies become ready.
	for _, name := range m.order {
		st := states[name]
		if st.waiting {
			continue
		}
		m.beginStart(tasks, states, st, "")
	}
	m.reschedule(tasks, states)

	tick := m.armSample(tasks, states)
	for {
		// A cancellation is checked before the blocking select so that a
		// shutdown which is already under way is never overtaken by a service
		// exiting because of it: the exit would otherwise be reported as a
		// crash caused by the very shutdown that asked for it.
		select {
		case <-ctx.Done():
			return m.terminate(tasks, states)
		default:
		}
		select {
		case <-ctx.Done():
			return m.terminate(tasks, states)
		case request := <-m.requests:
			m.handleRequest(tasks, states, request)
		case report := <-m.done:
			m.handleNotice(tasks, states, report)
		case <-tick:
			m.sample(states)
			tick = m.armSample(tasks, states)
		}
	}
}

// terminate stops every service and drains until all of them are gone.
//
// The loop deliberately ignores the caller's context. By the time it runs that
// context is already done, and the whole point of shutting down is to keep
// working long enough to reap every child.
func (m *Manager) terminate(tasks context.Context, states map[string]*serviceState) error {
	// From here on, every exit is the shutdown's doing rather than the
	// service's failure, however it was caused.
	m.draining = true

	for _, name := range m.order {
		st := states[name]
		st.disabled = true
		st.waiting = false
		st.restartPending = false
		st.restartAfterStop = false
		st.status.NextRestartAt = time.Time{}
		switch {
		case st.handle != nil:
			m.beginStop(tasks, st, "shutting down")
		case !st.status.State.IsTerminal():
			m.transition(st, service.StateStopped, "shutting down")
		}
	}

	for m.live(states) > 0 {
		select {
		case request := <-m.requests:
			// Shutdown refuses new work rather than half-applying it.
			refuse(request, ErrStopped)
		case report := <-m.done:
			m.handleNotice(tasks, states, report)
		}
	}
	return nil
}

// handleRequest performs a command from a caller.
func (m *Manager) handleRequest(tasks context.Context, states map[string]*serviceState, request request) {
	switch request := request.(type) {
	case *snapshotRequest:
		statuses := make([]service.Status, 0, len(m.order))
		for _, name := range m.order {
			statuses = append(statuses, states[name].status)
		}
		request.reply <- snapshotReply{statuses: statuses}

	case *actionRequest:
		if _, known := m.byName[request.name]; !known {
			replyAction(request, &NotFoundError{Name: request.name})
			return
		}
		st := states[request.name]
		switch request.action {
		case actionStart:
			m.startOnRequest(tasks, states, st, request)
		case actionStop:
			m.stopOnRequest(tasks, states, st, request)
		case actionRestart:
			m.restartOnRequest(tasks, states, st, request)
		}
	}
}

func (m *Manager) startOnRequest(tasks context.Context, states map[string]*serviceState, st *serviceState, request *actionRequest) {
	if st.handle != nil {
		replyAction(request, ErrAlreadyRunning)
		return
	}
	st.attempt = 0
	st.disabled = false
	st.restartAfterStop = false
	m.beginStart(tasks, states, st, "started on request")
	replyAction(request, nil)
}

func (m *Manager) stopOnRequest(tasks context.Context, states map[string]*serviceState, st *serviceState, request *actionRequest) {
	if st.handle == nil && st.status.State.IsTerminal() {
		replyAction(request, ErrNotRunning)
		return
	}
	st.disabled = true
	st.restartPending = false
	st.status.NextRestartAt = time.Time{}
	if st.handle != nil {
		m.beginStop(tasks, st, "stopped on request")
	} else {
		m.transition(st, service.StateStopped, "stopped on request")
		// There is no exit coming to reschedule on, so a dependent held back by
		// this service would otherwise wait for something that has already
		// finished happening.
		m.reschedule(tasks, states)
	}
	replyAction(request, nil)
}

func (m *Manager) restartOnRequest(tasks context.Context, states map[string]*serviceState, st *serviceState, request *actionRequest) {
	st.attempt = 0
	st.disabled = false
	if st.handle != nil {
		// The replacement waits for the old process to be fully torn down, so
		// the new instance cannot inherit a half-written log line.
		st.restartAfterStop = true
		m.beginStop(tasks, st, "restarting")
	} else {
		st.waiting = false
		m.beginStart(tasks, states, st, "restarted on request")
	}
	replyAction(request, nil)
}

// beginStart launches a service and wires up everything that observes it.
func (m *Manager) beginStart(tasks context.Context, states map[string]*serviceState, st *serviceState, reason string) {
	if st.handle != nil {
		return
	}
	st.generation++
	generation := st.generation
	st.stopping = false
	st.stopReason = ""
	st.disabled = false
	st.waiting = false
	st.restartAfterStop = false
	st.restartPending = false
	st.releasedBy = ""
	st.healthFailures = 0
	if st.spec.Health != nil && st.spec.Health.StartPeriod > 0 {
		st.status.Health = service.HealthStatus{State: service.HealthStarting, Since: m.clock.Now()}
	} else {
		st.status.Health = service.HealthStatus{State: service.HealthUnknown}
	}
	st.status.NextRestartAt = time.Time{}

	// Starting is announced before the process exists, so a slow launch is
	// visible rather than absent.
	m.transition(st, service.StateStarting, reason)

	handle, err := m.runner.Start(tasks, st.spec)
	if err != nil {
		st.status.Reason = err.Error()
		m.transition(st, service.StateCrashed, err.Error())
		// The restart is scheduled before anything is rescheduled, because a
		// service that is coming back has not lost its dependents.
		m.scheduleRestart(tasks, st)
		m.reschedule(tasks, states)
		m.releaseDependents(tasks, states)
		return
	}

	now := m.clock.Now()
	st.handle = handle
	st.status.PID = handle.PID()
	st.status.Started = now
	st.status.Stopped = time.Time{}
	st.status.ExitCode = 0
	m.transition(st, service.StateRunning, "started")

	// Readers are the only goroutines allowed to write to the log store, which
	// is why the store is built to be shared rather than owned.
	m.pump(st, logs.StreamStdout, handle.Stdout())
	m.pump(st, logs.StreamStderr, handle.Stderr())
	m.watch(tasks, st, generation, handle)
	m.startProbes(tasks, st, generation)
	m.reschedule(tasks, states)
	// A dependency that is running again brings back what it took down. This
	// runs once the service is genuinely up rather than when its start was
	// requested, so nothing is restored against a dependency that has not
	// made it back.
	m.restoreDependents(tasks, states, st)
}

// beginStop asks a live process to stop, off the owner goroutine so that
// stopping many services at once does not take as long as their grace periods
// added together.
func (m *Manager) beginStop(tasks context.Context, st *serviceState, reason string) {
	handle := st.handle
	if handle == nil {
		m.transition(st, service.StateStopped, reason)
		return
	}
	// The process is still running, so its state stays running; the reason is
	// what tells the dashboard that a stop is under way.
	st.stopping = true
	st.stopReason = reason
	m.publish(st, reason)

	name := st.spec.Name
	generation := st.generation
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(tasks), m.grace+stopSlack)
	go func() {
		defer cancel()
		err := m.runner.Signal(stopCtx, handle, m.grace)
		report := notice{kind: noticeSignalError, name: name, generation: generation, err: err, at: m.clock.Now()}
		select {
		case m.done <- report:
		case <-tasks.Done():
		}
	}()
}

// watch reaps a process. It is the only place that can report an exit, because
// the runner's Wait is what closes the output pipes and gives the readers their
// EOF.
func (m *Manager) watch(tasks context.Context, st *serviceState, generation uint64, handle Handle) {
	name := st.spec.Name
	go func() {
		info, err := m.runner.Wait(tasks, handle)
		if err != nil {
			info.Err = err
		}
		report := notice{kind: noticeExit, name: name, generation: generation, exit: info, at: m.clock.Now()}
		select {
		case m.done <- report:
		case <-tasks.Done():
		}
	}()
}

// pump drains one of a process's output streams into its log buffer.
func (m *Manager) pump(st *serviceState, stream logs.Stream, reader io.ReadCloser) {
	name := st.spec.Name
	writer := logs.NewWriter(name, stream, m.clock, func(line logs.Line) {
		// This runs on the reader goroutine. Appending to the buffer and
		// emitting an event are both safe from any goroutine; nothing here
		// touches service state.
		stored := m.store.Buffer(name).Append(line)
		m.sink.Emit(events.LogEmitted{Line: stored})
	})
	st.readers.Go(func() {
		defer reader.Close()
		_, _ = io.Copy(writer, reader)
		_ = writer.Close()
	})
}

// handleNotice applies a report from a worker goroutine.
func (m *Manager) handleNotice(tasks context.Context, states map[string]*serviceState, report notice) {
	if report.kind == noticeProbe {
		m.handleProbe(tasks, states, report)
		return
	}

	st, known := states[report.name]
	if !known {
		return
	}
	// A report from a previous generation is history: the teardown that
	// replaced it has already accounted for its output and its exit.
	stale := st.generation != report.generation

	switch report.kind {
	case noticeSignalError:
		if report.err != nil && !stale {
			m.publish(st, "stop failed: "+report.err.Error())
		}
	case noticeDelay:
		if stale || !st.restartPending {
			return
		}
		st.restartPending = false
		m.beginStart(tasks, states, st, "restarting after failure")
	case noticeExit:
		if stale {
			return
		}
		m.handleExit(tasks, states, st, report)
	}
}

// handleExit finalises a service whose process has ended.
func (m *Manager) handleExit(tasks context.Context, states map[string]*serviceState, st *serviceState, report notice) {
	// Wait for the readers before deciding anything. Their output is already
	// queued, and the service is not truly gone until that output has been
	// recorded, which is also what keeps it out of the next generation's
	// buffer.
	st.readers.Wait()
	m.stopProbes(st)

	if st.handle != nil {
		m.forgetSampler(st.handle.PID())
	}
	st.handle = nil
	st.status.ExitCode = report.exit.Code
	st.status.Stopped = report.exit.At

	intentional := st.stopping || m.draining
	stopReason := st.stopReason
	if m.draining && stopReason == "" {
		stopReason = "shutting down"
	}
	st.stopping = false
	st.stopReason = ""

	next := service.StateExited
	switch {
	case intentional:
		next = service.StateStopped
	case !report.exit.Success():
		next = service.StateCrashed
	}

	reason := m.exitReason(report.exit, intentional, stopReason)
	if st.status.State == service.StateStarting {
		// The process died before it was ever observed running.
		next = service.StateCrashed
	}
	m.transition(st, next, reason)

	if st.restartAfterStop {
		st.restartAfterStop = false
		m.beginStart(tasks, states, st, "restarted")
		return
	}
	if intentional {
		// A service that was stopped on purpose stays down, and one that was
		// released by a failed dependency has already been marked so.
		st.disabled = true
	} else {
		m.scheduleRestart(tasks, st)
	}
	m.reschedule(tasks, states)
	// Whatever was running against this service has to be told that it is
	// gone. A service that a restart policy is bringing back is not gone, so
	// this is a no-op until a failure is final.
	m.releaseDependents(tasks, states)
}

func (m *Manager) exitReason(exit ExitInfo, intentional bool, stopReason string) string {
	switch {
	case exit.Err != nil:
		return "could not observe exit: " + exit.Err.Error()
	case intentional:
		if stopReason == "" {
			return "stopped"
		}
		return stopReason
	case exit.Signaled:
		return fmt.Sprintf("killed by signal %d", exit.Code)
	default:
		return fmt.Sprintf("exit code %d", exit.Code)
	}
}

// scheduleRestart arms a backoff timer for a service that may be brought back.
func (m *Manager) scheduleRestart(tasks context.Context, st *serviceState) {
	if st.disabled {
		return
	}
	policy := st.spec.Restart.Policy
	switch policy {
	case service.RestartNever:
		return
	case service.RestartOnFailure:
		if st.status.State != service.StateCrashed {
			return
		}
	case service.RestartAlways:
	}

	if st.spec.Restart.MaxAttempts > 0 && st.attempt >= st.spec.Restart.MaxAttempts {
		m.publish(st, fmt.Sprintf("restart limit reached after %d attempts", st.attempt))
		return
	}

	st.attempt++
	delay := st.spec.Restart.Delay(st.attempt)
	st.restartPending = true
	st.status.NextRestartAt = m.clock.Now().Add(delay)
	m.publish(st, fmt.Sprintf("restarting in %s (attempt %d)", delay, st.attempt))

	name := st.spec.Name
	generation := st.generation
	go func() {
		select {
		case <-m.clock.After(delay):
		case <-tasks.Done():
			return
		}
		report := notice{kind: noticeDelay, name: name, generation: generation, at: m.clock.Now()}
		select {
		case m.done <- report:
		case <-tasks.Done():
		}
	}()
}

// transition moves a service to a new lifecycle state and publishes it.
func (m *Manager) transition(st *serviceState, next service.State, reason string) {
	now := m.clock.Now()
	previous := st.status.State
	st.status.Reason = reason

	if previous == next {
		m.publish(st, reason)
		return
	}
	if !previous.CanTransitionTo(next) {
		// Not reachable through any call site in this package. If a future edit
		// makes it reachable, the dashboard should say so rather than render a
		// lifecycle that cannot happen.
		reason = fmt.Sprintf("%s (note: %s to %s is not a lifecycle transition)", reason, previous, next)
		st.status.Reason = reason
	}
	st.status.State = next
	switch {
	case next == service.StateRunning:
		st.status.Started = now
		st.status.Stopped = time.Time{}
	case next.IsTerminal():
		st.status.Stopped = now
		st.status.PID = 0
	}

	m.sink.Emit(events.StateChanged{
		Status: st.status,
		From:   previous,
		To:     next,
		Reason: reason,
		Time:   now,
	})
}

// publish reports a change to a service's status that is not a lifecycle
// transition, such as a restart countdown or a health result.
//
// A publish with nothing to say is dropped: the state alone is the message, and
// repeating it would fill the dashboard's history with entries that say
// nothing.
func (m *Manager) publish(st *serviceState, reason string) {
	if reason == "" && st.status.Reason == "" {
		return
	}
	st.status.Reason = reason
	m.sink.Emit(events.StatusUpdated{Status: st.status, Reason: reason, Time: m.clock.Now()})
}

func replyAction(request *actionRequest, err error) {
	if request.reply == nil {
		return
	}
	request.reply <- err
}

// refuse answers a request that arrived after the manager stopped accepting
// work. Both request kinds are answered, because a caller left waiting for a
// reply during shutdown would be a hang we caused.
func refuse(request request, err error) {
	switch request := request.(type) {
	case *actionRequest:
		replyAction(request, err)
	case *snapshotRequest:
		request.reply <- snapshotReply{err: err}
	}
}

// live reports how many services still have a process attached.
func (m *Manager) live(states map[string]*serviceState) int {
	count := 0
	for _, st := range states {
		if st.handle != nil {
			count++
		}
	}
	return count
}
