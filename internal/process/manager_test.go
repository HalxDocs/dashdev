package process_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/process"
	"github.com/HalxDocs/dashdev/internal/service"
)

func TestManagerAnnouncesAndStartsEveryService(t *testing.T) {
	specs := []service.Spec{spec("api"), spec("web"), spec("worker")}
	h := newHarness(t, specs, process.Options{})

	for _, name := range []string{"api", "web", "worker"} {
		h.waitState(name, service.StateRunning)
	}

	// Every service is announced before anything starts, so a subscriber can
	// build its whole view from the stream.
	if order := h.announcedOrder(); len(order) != 3 || order[0] != "api" || order[1] != "web" || order[2] != "worker" {
		t.Fatalf("announcement order = %v, want declaration order", order)
	}
	kinds := h.traceKinds()
	if kinds[0] != events.KindServiceAdded {
		t.Fatalf("first event = %v, want ServiceAdded", kinds[0])
	}
	serviceAdded := 0
	for _, kind := range kinds {
		if kind != events.KindServiceAdded {
			break
		}
		serviceAdded++
	}
	if serviceAdded != 3 {
		t.Fatalf("published %d announcements before starting anything, want 3", serviceAdded)
	}

	if len(h.runner.Started()) != 3 {
		t.Fatalf("started %d processes, want 3", len(h.runner.Started()))
	}
}

func TestManagerCapturesOutputWithMetadata(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("api")}, process.Options{})
	h.waitState("api", service.StateRunning)

	handle := h.runner.StartedFor("api")[0]
	handle.WriteStdout("listening on :8080\n")
	handle.WriteStderr("warning: deprecated flag\n")

	h.waitFor("both streams to be captured", func() bool {
		return len(h.linesFor("api")) >= 2
	})

	lines := h.linesFor("api")
	if lines[0].Service != "api" {
		t.Errorf("line service = %q, want api", lines[0].Service)
	}
	if lines[0].Stream != logs.StreamStdout {
		t.Errorf("first line stream = %v, want stdout", lines[0].Stream)
	}
	if lines[1].Stream != logs.StreamStderr {
		t.Errorf("second line stream = %v, want stderr", lines[1].Stream)
	}
	if lines[0].Message != "listening on :8080" {
		t.Errorf("first line = %q, want the message without a newline", lines[0].Message)
	}
	if lines[1].Seq <= lines[0].Seq {
		t.Errorf("sequence numbers are not increasing: %d then %d", lines[0].Seq, lines[1].Seq)
	}
	if lines[0].Time.IsZero() {
		t.Error("line has no timestamp")
	}

	// The buffer is bounded, and 64 lines was the capacity the harness asked
	// for, so a chatty service cannot grow it.
	for i := range 200 {
		handle.WriteStdout("line " + strings.Repeat("x", i%7) + "\n")
	}
	h.waitFor("the buffer to fill", func() bool {
		return h.store.Buffer("api").Dropped() > 0
	})
	if got := h.store.Buffer("api").Len(); got > 64 {
		t.Errorf("buffer holds %d lines, want at most 64", got)
	}
}

func TestShutdownStopsAndReapsEveryService(t *testing.T) {
	specs := []service.Spec{spec("a"), spec("b"), spec("c"), spec("d"), spec("e")}
	h := newHarness(t, specs, process.Options{})
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		h.waitState(name, service.StateRunning)
	}

	h.stop(errors.New("test over"))

	if h.runner.Unexited() != 0 {
		t.Fatalf("%d processes survived shutdown", h.runner.Unexited())
	}
	for _, handle := range h.runner.Started() {
		if handle.StopRequests() == 0 {
			t.Errorf("%s was reaped without ever being asked to stop", handle.Spec().Name)
		}
		if handle.KillUsed() {
			t.Errorf("%s had to be killed despite being cooperative", handle.Spec().Name)
		}
	}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		h.waitState(name, service.StateStopped)
	}
}

func TestShutdownKillsAProcessThatIgnoresAStopRequest(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("stubborn")}, process.Options{GracePeriod: 20 * time.Millisecond})
	h.waitState("stubborn", service.StateRunning)

	handle := h.runner.StartedFor("stubborn")[0]
	handle.Stubborn = true

	h.stop(errors.New("test over"))

	if !handle.Exited() {
		t.Fatal("a process that ignored a stop request was left running")
	}
	if !handle.KillUsed() {
		t.Error("the manager never escalated to a kill")
	}
	h.waitState("stubborn", service.StateStopped)
}

func TestRestartTearsTheOldProcessDownBeforeStartingTheReplacement(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("api")}, process.Options{})
	h.waitState("api", service.StateRunning)

	first := h.runner.StartedFor("api")[0]
	first.WriteStdout("first generation\n")
	h.waitFor("the first line", func() bool { return h.logIndex("first generation") >= 0 })

	// A line written as the stop begins is still the old process's output and
	// must be recorded before the replacement exists.
	first.WriteStdout("last words\n")

	if err := h.manager.Restart(context.Background(), "api"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	handles := h.waitStarted("api", 2)
	second := handles[1]
	second.WriteStdout("second generation\n")
	h.waitFor("the second line", func() bool { return h.logIndex("second generation") >= 0 })
	h.waitState("api", service.StateRunning)

	if !first.Exited() {
		t.Error("the replaced process was never reaped")
	}
	if first.StopRequests() == 0 {
		t.Error("the replaced process was never asked to stop")
	}

	replacementStart := h.runningIndex("api", service.StateRunning, 2)
	if replacementStart < 0 {
		t.Fatal("the replacement never reached the running state")
	}
	if index := h.logIndex("last words"); index < 0 || index > replacementStart {
		t.Errorf("a line from the replaced process arrived after its replacement started (index %d, start %d)",
			index, replacementStart)
	}

	// The restart is a teardown and a start, so the lifecycle passes through a
	// terminal state rather than jumping from running to running.
	if index := h.runningIndex("api", service.StateRunning, 1); index < 0 || index >= replacementStart {
		t.Errorf("the second running state did not follow the first (first %d, second %d)", index, replacementStart)
	}
}

func TestManualStopIsNotUndoneByTheRestartPolicy(t *testing.T) {
	specs := []service.Spec{spec("api", func(s *service.Spec) {
		s.Restart = service.RestartConfig{
			Policy:  service.RestartAlways,
			Backoff: service.Backoff{Base: time.Millisecond, Max: time.Millisecond, Factor: 1},
		}
	})}
	h := newHarness(t, specs, process.Options{})
	h.waitState("api", service.StateRunning)

	if err := h.manager.Stop(context.Background(), "api"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.waitState("api", service.StateStopped)

	// Give a restart scheduled for a millisecond every chance to happen.
	time.Sleep(50 * time.Millisecond)
	if started := len(h.runner.StartedFor("api")); started != 1 {
		t.Errorf("service started %d times, want 1: a deliberate stop must not be undone", started)
	}
}

func TestRestartPolicyBacksOffAndGivesUpVisibly(t *testing.T) {
	backoff := service.Backoff{Base: 10 * time.Millisecond, Max: 40 * time.Millisecond, Factor: 2}
	specs := []service.Spec{spec("flaky", func(s *service.Spec) {
		s.Restart = service.RestartConfig{Policy: service.RestartOnFailure, MaxAttempts: 3, Backoff: backoff}
	})}
	h := newHarness(t, specs, process.Options{})
	h.waitState("flaky", service.StateRunning)

	for attempt := 1; attempt <= 3; attempt++ {
		handles := h.runner.StartedFor("flaky")
		handles[len(handles)-1].Crash(1)
		h.waitState("flaky", service.StateCrashed)
		h.waitFor("restart attempt "+strconv.Itoa(attempt)+" to be scheduled", func() bool {
			return !h.status("flaky").NextRestartAt.IsZero()
		})
		// The delay must not have elapsed yet: the clock is only moved by the
		// test, which is what makes the backoff verifiable rather than timed.
		if started := len(h.runner.StartedFor("flaky")); started != attempt {
			t.Fatalf("service restarted before its backoff elapsed (%d starts)", started)
		}
		h.clock.Advance(5 * time.Second)
		h.waitStarted("flaky", attempt+1)
		h.waitState("flaky", service.StateRunning)
	}

	// The fourth crash exhausts max_attempts, so the failure becomes visible
	// instead of looping forever.
	handles := h.runner.StartedFor("flaky")
	handles[len(handles)-1].Crash(1)
	h.waitState("flaky", service.StateCrashed)
	h.waitFor("the restart limit to be reported", func() bool {
		return strings.Contains(h.status("flaky").Reason, "restart limit")
	})
	if started := len(h.runner.StartedFor("flaky")); started != 4 {
		t.Errorf("service started %d times, want 4 (one initial plus three restarts)", started)
	}
}

func TestRestartPolicyNeverLeavesACrashAlone(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("api")}, process.Options{})
	h.waitState("api", service.StateRunning)
	h.runner.StartedFor("api")[0].Crash(3)
	h.waitState("api", service.StateCrashed)

	time.Sleep(30 * time.Millisecond)
	if started := len(h.runner.StartedFor("api")); started != 1 {
		t.Errorf("service started %d times, want 1: the default policy is to leave it alone", started)
	}
	if reason := h.status("api").Reason; !strings.Contains(reason, "3") {
		t.Errorf("crash reason = %q, want the exit code", reason)
	}
}

func TestCleanExitIsReportedAsExitedNotCrashed(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("job")}, process.Options{})
	h.waitState("job", service.StateRunning)
	h.runner.StartedFor("job")[0].Exit(0)
	h.waitState("job", service.StateExited)
}

func TestFailedStartIsReportedAsACrash(t *testing.T) {
	runner := newFailingRunner(errors.New("no such file or directory"))
	h := newHarness(t, []service.Spec{spec("ghost")}, process.Options{Runner: runner})
	h.waitState("ghost", service.StateCrashed)

	if reason := h.status("ghost").Reason; !strings.Contains(reason, "no such file") {
		t.Errorf("crash reason = %q, want the start error", reason)
	}
}

func TestSnapshotReportsEveryServiceInDeclarationOrder(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("api"), spec("web")}, process.Options{})
	h.waitState("api", service.StateRunning)
	h.waitState("web", service.StateRunning)

	statuses, err := h.manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(statuses) != 2 || statuses[0].Name != "api" || statuses[1].Name != "web" {
		t.Fatalf("Snapshot = %+v, want api then web", statuses)
	}
	if statuses[0].PID == 0 {
		t.Error("running service has no pid")
	}
	if statuses[0].Uptime(h.clock.Now()) < 0 {
		t.Error("uptime is negative")
	}
}

func TestActionsOnUnknownServicesAreRefused(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("api")}, process.Options{})
	h.waitState("api", service.StateRunning)

	ctx := context.Background()
	for name, call := range map[string]func() error{
		"restart": func() error { return h.manager.Restart(ctx, "nope") },
		"stop":    func() error { return h.manager.Stop(ctx, "nope") },
		"start":   func() error { return h.manager.Start(ctx, "nope") },
	} {
		err := call()
		if !errors.Is(err, process.ErrNotFound) {
			t.Errorf("%s of an unknown service = %v, want ErrNotFound", name, err)
		}
	}
}

func TestStartOnARunningServiceIsRefused(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("api")}, process.Options{})
	h.waitState("api", service.StateRunning)
	if err := h.manager.Start(context.Background(), "api"); !errors.Is(err, process.ErrAlreadyRunning) {
		t.Fatalf("Start on a running service = %v, want ErrAlreadyRunning", err)
	}
}

func TestRequestsAfterShutdownAreRefusedRatherThanHung(t *testing.T) {
	h := newHarness(t, []service.Spec{spec("api")}, process.Options{})
	h.waitState("api", service.StateRunning)
	h.stop(errors.New("test over"))

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.manager.Restart(ctx, "api"); !errors.Is(err, process.ErrStopped) {
		t.Fatalf("Restart after shutdown = %v, want ErrStopped", err)
	}
	if _, err := h.manager.Snapshot(ctx); !errors.Is(err, process.ErrStopped) {
		t.Fatalf("Snapshot after shutdown = %v, want ErrStopped", err)
	}
}

// failingRunner always refuses to start, which is how a missing binary or a
// directory that has been deleted shows up.
type failingRunner struct {
	err    error
	mu     sync.Mutex
	starts int
}

func newFailingRunner(err error) *failingRunner { return &failingRunner{err: err} }

func (r *failingRunner) Start(context.Context, service.Spec) (process.Handle, error) {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	return nil, r.err
}

func (r *failingRunner) Signal(context.Context, process.Handle, time.Duration) error { return nil }

func (r *failingRunner) Wait(context.Context, process.Handle) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
