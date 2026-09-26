package process_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/config"
	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/process"
	"github.com/HalxDocs/dashdev/internal/service"
)

// integrationRun is a manager running real processes, with the same bookkeeping
// the dashboard does: it reads the event stream and remembers the latest status
// of every service rather than asking the manager.
type integrationRun struct {
	t       *testing.T
	manager *process.Manager
	store   *logs.Store
	cancel  context.CancelFunc
	stopped chan error

	stopOnce sync.Once
	stopErr  error

	mu       sync.Mutex
	observed map[string]service.Status
}

// startIntegration loads a configuration, starts a manager on real processes and
// waits until events are flowing. The run is shut down by the test's cleanup if
// the test has not stopped it itself.
func startIntegration(t *testing.T, configPath string) *integrationRun {
	t.Helper()

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	bus := events.NewBus()
	store := logs.NewStore(500)
	manager, err := process.NewManager(loaded.Specs(), process.Options{
		Runner: process.NewExecRunner(),
		Sink:   bus,
		Store:  store,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	run := &integrationRun{
		t:        t,
		manager:  manager,
		store:    store,
		stopped:  make(chan error, 1),
		observed: make(map[string]service.Status),
	}

	// Track what the manager publishes, exactly as the dashboard does.
	subscription := bus.Subscribe(0)
	consumeCtx, stopConsuming := context.WithCancel(context.Background())
	t.Cleanup(stopConsuming)
	go func() {
		for {
			batch, err := subscription.Pop(consumeCtx)
			if err != nil {
				return
			}
			run.mu.Lock()
			for _, event := range batch {
				switch event := event.(type) {
				case events.ServiceAdded:
					run.observed[event.Status.Name] = event.Status
				case events.StateChanged:
					run.observed[event.Status.Name] = event.Status
				case events.StatusUpdated:
					run.observed[event.Status.Name] = event.Status
				}
			}
			run.mu.Unlock()
		}
	}()

	runCtx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel
	t.Cleanup(func() {
		// A test that fails an assertion before shutting down must not leave
		// real processes behind, so the shutdown is also a cleanup.
		run.shutdown()
	})
	go func() { run.stopped <- manager.Run(runCtx) }()
	return run
}

// statusOf returns the last status the manager published for a service.
func (r *integrationRun) statusOf(name string) service.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.observed[name]
}

// waitFor polls the published status until condition holds.
func (r *integrationRun) waitFor(reason string, condition func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s", reason)
}

// waitState waits for a service to reach a lifecycle state.
func (r *integrationRun) waitState(name string, want service.State) {
	r.t.Helper()
	r.waitFor(name+" to be "+want.String(), func() bool {
		return r.statusOf(name).State == want
	})
}

// shutdown stops the manager and waits for every process to be reaped. It is
// safe to call more than once, so a test can assert on the shutdown and the
// cleanup still works.
func (r *integrationRun) shutdown() {
	r.t.Helper()
	r.stopOnce.Do(func() {
		r.cancel()
		select {
		case r.stopErr = <-r.stopped:
		case <-time.After(30 * time.Second):
			r.t.Fatalf("the manager did not shut down")
		}
	})
	if r.stopErr != nil {
		r.t.Fatalf("manager.Run: %v", r.stopErr)
	}
}

// firstLine returns the first captured log line of a service.
func (r *integrationRun) firstLine(name string) (logs.Line, bool) {
	lines := r.store.Buffer(name).Snapshot()
	if len(lines) == 0 {
		return logs.Line{}, false
	}
	return lines[0], true
}

// TestIntegrationRunsRealProcessesAndLeavesNoneBehind is the test that keeps
// the whole design honest: real processes, real pipes, real exit codes, real
// shutdown. Everything else in this package uses a fake runner, which is what
// makes this the one that would catch a mistake in the boundary between them.
func TestIntegrationRunsRealProcessesAndLeavesNoneBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test launches real processes")
	}
	helper := buildHelper(t)

	directory := t.TempDir()
	configPath := filepath.Join(directory, config.FileName)
	writeFile(t, configPath, strings.Join([]string{
		"version: 1",
		"services:",
		"  - name: server",
		"    command: [" + quote(helper) + ", -mode=serve, -label=server]",
		"  - name: job",
		"    command: [" + quote(helper) + ", -mode=exit, -code=0, -label=job]",
		"  - name: broken",
		"    command: [" + quote(helper) + ", -mode=exit, -code=7, -label=broken]",
		"",
	}, "\n"))

	run := startIntegration(t, configPath)

	run.waitState("server", service.StateRunning)
	run.waitFor("output from the running process", func() bool {
		return run.store.Buffer("server").Len() > 0
	})
	run.waitState("job", service.StateExited)
	run.waitFor("the failure exit to be reported", func() bool {
		status := run.statusOf("broken")
		return status.State == service.StateCrashed && status.ExitCode == 7
	})

	// Log lines carry the metadata the dashboard needs, and they came from a
	// real process rather than a fake.
	line, ok := run.firstLine("server")
	if !ok {
		t.Fatal("no log lines were captured from a real process")
	}
	if line.Service != "server" || line.Stream != logs.StreamStdout {
		t.Errorf("log metadata = %+v, want server on stdout", line)
	}
	if !strings.Contains(line.Message, "ready") {
		t.Errorf("first line = %q, want the helper's readiness message", line.Message)
	}
	if line.Time.IsZero() {
		t.Error("log line has no timestamp")
	}

	serverPID := run.statusOf("server").PID
	if serverPID <= 0 {
		t.Fatal("the running service reported no pid")
	}
	if !platform.Alive(serverPID) {
		t.Fatal("the service was reported running but the operating system disagrees")
	}

	run.shutdown()

	if platform.Alive(serverPID) {
		t.Errorf("process %d is still alive after shutdown", serverPID)
	}
	// The manager published the final events before Run returned, but the
	// consumer goroutine reads the stream independently, so the terminal
	// states have to be waited for rather than read at the instant the run
	// loop exits.
	run.waitState("server", service.StateStopped)
	if status := run.statusOf("job"); status.State != service.StateExited {
		t.Errorf("job ended as %s, want exited", status.State)
	}
	if status := run.statusOf("broken"); status.State != service.StateCrashed {
		t.Errorf("broken ended as %s, want crashed", status.State)
	}
}

// TestIntegrationDependenciesAreOrderedAndFollowAFailure runs a dependency chain
// on real processes. It covers the two halves of the dependency rule: a service
// declared after the service it depends on still starts, in order, and a
// service whose dependency dies for good is taken down with it rather than left
// running against nothing.
//
// The declaration order is deliberate. A dependent declared below its
// dependency is how a person naturally writes the file, and it is the
// arrangement that a scheduler driven only by later events gets wrong.
func TestIntegrationDependenciesAreOrderedAndFollowAFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test launches real processes")
	}
	helper := buildHelper(t)

	directory := t.TempDir()
	configPath := filepath.Join(directory, config.FileName)
	writeFile(t, configPath, strings.Join([]string{
		"version: 1",
		"services:",
		"  - name: db",
		"    command: [" + quote(helper) + ", -mode=serve, -label=db]",
		"  - name: api",
		"    command: [" + quote(helper) + ", -mode=serve, -label=api]",
		"    depends_on:",
		"      db: started",
		"  - name: doomed",
		"    command: [" + quote(helper) + ", -mode=exit, -code=5, -label=doomed]",
		"  - name: watcher",
		"    command: [" + quote(helper) + ", -mode=serve, -label=watcher]",
		"    depends_on:",
		"      doomed: started",
		"",
	}, "\n"))

	run := startIntegration(t, configPath)

	run.waitState("db", service.StateRunning)
	run.waitState("api", service.StateRunning)
	if status := run.statusOf("api"); status.Reason != "started" {
		t.Errorf("api reason = %q, want it to have started on its own rather than waited", status.Reason)
	}

	// The dependent really ran, and its first output was recorded after the
	// output of the service it needs.
	run.waitFor("both services to produce output", func() bool {
		_, db := run.firstLine("db")
		_, api := run.firstLine("api")
		return db && api
	})
	dbLine, _ := run.firstLine("db")
	apiLine, _ := run.firstLine("api")
	if apiLine.Time.Before(dbLine.Time) {
		t.Errorf("api produced output at %s, before db at %s", apiLine.Time, dbLine.Time)
	}

	// The dependency of watcher ends for good, so watcher has to end with it.
	run.waitState("doomed", service.StateCrashed)
	run.waitState("watcher", service.StateStopped)
	if reason := run.statusOf("watcher").Reason; !strings.Contains(reason, `"doomed"`) {
		t.Errorf("watcher reason = %q, want the dependency named", reason)
	}
	if pid := run.statusOf("watcher").PID; pid != 0 {
		t.Errorf("watcher still reports pid %d after being released", pid)
	}

	run.shutdown()
	// The consumer reads the stream independently of the run loop, so the last
	// state is waited for rather than read the instant Run returns.
	run.waitState("db", service.StateStopped)
	if status := run.statusOf("watcher"); status.State != service.StateStopped {
		t.Errorf("watcher ended as %s, want stopped", status.State)
	}
}

// TestIntegrationAReleasedChainComesBackWithItsDependency runs the whole
// dependency cycle on real processes: a service dies, the dependents running
// against it are stood down, the dependency is started again, and the
// dependents come back with it — twice, so that the second failure proves the
// bench was re-armed rather than cleared.
func TestIntegrationAReleasedChainComesBackWithItsDependency(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test launches real processes")
	}
	helper := buildHelper(t)

	// The trigger file's presence is what keeps the fragile service alive, so
	// the test decides exactly when it dies.
	trigger := filepath.Join(t.TempDir(), "alive")
	writeFile(t, trigger, "")

	directory := t.TempDir()
	configPath := filepath.Join(directory, config.FileName)
	writeFile(t, configPath, strings.Join([]string{
		"version: 1",
		"services:",
		"  - name: fragile",
		// The whole argument is quoted rather than just the path: inside an
		// unquoted scalar the quote characters would survive into argv.
		"    command: [" + quote(helper) + ", -mode=crash, " + quote("-trigger="+trigger) + ", -code=5, -label=fragile]",
		"  - name: api",
		"    command: [" + quote(helper) + ", -mode=serve, -label=api]",
		"    depends_on:",
		"      fragile: started",
		"",
	}, "\n"))

	run := startIntegration(t, configPath)
	run.waitState("fragile", service.StateRunning)
	run.waitState("api", service.StateRunning)
	firstPID := run.statusOf("api").PID
	fragilePID := run.statusOf("fragile").PID

	// The dependency dies while its dependent is running.
	if err := os.Remove(trigger); err != nil {
		t.Fatalf("removing the trigger: %v", err)
	}
	run.waitState("fragile", service.StateCrashed)
	run.waitState("api", service.StateStopped)
	if reason := run.statusOf("api").Reason; !strings.Contains(reason, `"fragile"`) {
		t.Errorf("api reason = %q, want the dependency named", reason)
	}
	if run.statusOf("fragile").PID != 0 {
		t.Errorf("the crashed dependency still reports pid %d", run.statusOf("fragile").PID)
	}
	if platform.Alive(fragilePID) {
		t.Errorf("process %d is still alive after being reported as crashed", fragilePID)
	}

	// The dependency is brought back, and the dependent comes with it.
	writeFile(t, trigger, "")
	if err := run.manager.Start(context.Background(), "fragile"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	run.waitState("fragile", service.StateRunning)
	run.waitState("api", service.StateRunning)
	recoveredPID := run.statusOf("api").PID
	if recoveredPID == firstPID {
		t.Errorf("the recovered api reused pid %d, so it is not a new process", recoveredPID)
	}

	// It dies a second time, and the dependent goes down again: the first
	// release did not spend the relationship.
	if err := os.Remove(trigger); err != nil {
		t.Fatalf("removing the trigger: %v", err)
	}
	run.waitState("fragile", service.StateCrashed)
	run.waitState("api", service.StateStopped)
	if pid := run.statusOf("api").PID; pid != 0 {
		t.Errorf("api still reports pid %d after being released twice", pid)
	}

	// Shutting down changes nothing about services that were already at rest:
	// the dependency is still responsible for its own death, and its dependent
	// is still stopped rather than crashed.
	run.shutdown()
	if state := run.statusOf("fragile").State; state != service.StateCrashed {
		t.Errorf("fragile ended as %s, want the crash it caused", state)
	}
	if state := run.statusOf("api").State; state != service.StateStopped {
		t.Errorf("api ended as %s, want stopped", state)
	}
}

// buildHelper compiles the helper service into the test's temporary directory.
func buildHelper(t *testing.T) string {
	t.Helper()
	name := "helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)

	command := exec.Command("go", "build", "-o", binary, "./testdata/helper")
	command.Dir = projectRoot(t)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("building the helper service: %v\n%s", err, output)
	}
	return binary
}

// projectRoot walks up from the test's working directory to the module root, so
// that a build command names the same package however the test was started.
func projectRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the module root")
		}
		directory = parent
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// quote makes a path safe to put in a YAML flow sequence. Windows paths contain
// backslashes, which YAML double-quoted scalars would treat as escapes, so
// single quotes are used instead.
func quote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
}
