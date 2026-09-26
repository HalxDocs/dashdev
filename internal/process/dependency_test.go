package process_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/process"
	"github.com/HalxDocs/dashdev/internal/service"
)

// dependsOn is the short form of a dependency declaration: the dependent needs
// the named service to have started.
func dependsOn(name string) []service.Dependency {
	return []service.Dependency{{Name: name, Condition: service.ReadyWhenStarted}}
}

// A dependent that is still running when the service it needs dies for good
// must be stopped. Anything else would let the dashboard show a green service
// whose prerequisite is gone.
func TestDependentsAreStoppedWhenTheirDependencyDiesForGood(t *testing.T) {
	specs := []service.Spec{
		spec("db"),
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("db") }),
	}
	h := newHarness(t, specs, process.Options{})
	h.waitState("db", service.StateRunning)
	h.waitState("api", service.StateRunning)

	// The default policy is never, so this crash is final.
	h.runner.StartedFor("db")[0].Crash(1)
	h.waitState("db", service.StateCrashed)
	h.waitState("api", service.StateStopped)

	if reason := h.status("api").Reason; !strings.Contains(reason, `"db"`) {
		t.Errorf("api reason = %q, want the dependency named", reason)
	}
	handle := h.runner.StartedFor("api")[0]
	if handle.StopRequests() == 0 {
		t.Error("api was reaped without ever being asked to stop")
	}
	if handle.KillUsed() {
		t.Error("api had to be killed despite being cooperative")
	}
	if started := len(h.runner.StartedFor("api")); started != 1 {
		t.Errorf("api started %d times, want 1: a released dependent must not be restarted", started)
	}
	if running := h.runner.Unexited(); running != 0 {
		t.Errorf("%d process(es) were still running after the release", running)
	}
}

// A dependency that is on its way back is not a dependency that is gone.
// Stopping dependents while a restart is pending would turn a recovering
// service into an outage, which is the opposite of what a restart policy is
// for.
func TestADependencyThatIsComingBackDoesNotTakeItsDependentsDown(t *testing.T) {
	specs := []service.Spec{
		spec("db", func(s *service.Spec) {
			s.Restart = service.RestartConfig{
				Policy:  service.RestartOnFailure,
				Backoff: service.Backoff{Base: time.Millisecond, Max: time.Millisecond, Factor: 1},
			}
		}),
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("db") }),
	}
	h := newHarness(t, specs, process.Options{})
	h.waitState("db", service.StateRunning)
	h.waitState("api", service.StateRunning)

	h.runner.StartedFor("db")[0].Crash(1)
	h.waitState("db", service.StateCrashed)
	h.waitFor("a restart to be scheduled", func() bool {
		return !h.status("db").NextRestartAt.IsZero()
	})

	if state := h.status("api").State; state != service.StateRunning {
		t.Fatalf("api = %s while a restart was pending, want running", state)
	}
	if requests := h.runner.StartedFor("api")[0].StopRequests(); requests != 0 {
		t.Errorf("api was asked to stop %d time(s) while a restart was pending", requests)
	}

	// The clock is advanced by the test, so the recovery is deterministic
	// rather than timed.
	h.clock.Advance(time.Second)
	h.waitStarted("db", 2)
	h.waitState("db", service.StateRunning)
	if state := h.status("api").State; state != service.StateRunning {
		t.Errorf("api = %s after db recovered, want running", state)
	}
}

// A released service takes its own dependents with it, so one death reaches the
// end of the chain rather than stopping at the first link.
func TestAFailureTravelsAlongTheChainOfDependents(t *testing.T) {
	specs := []service.Spec{
		spec("db"),
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("db") }),
		spec("web", func(s *service.Spec) { s.DependsOn = dependsOn("api") }),
	}
	h := newHarness(t, specs, process.Options{})
	for _, name := range []string{"db", "api", "web"} {
		h.waitState(name, service.StateRunning)
	}

	h.runner.StartedFor("db")[0].Crash(1)
	h.waitState("api", service.StateStopped)
	h.waitState("web", service.StateStopped)

	if reason := h.status("api").Reason; !strings.Contains(reason, `"db"`) {
		t.Errorf("api reason = %q, want the dependency that took it down", reason)
	}
	// The second link names its own dependency, not the original failure: it
	// was api that was stopped, and that is what a person reading the
	// dashboard needs to see.
	if reason := h.status("web").Reason; !strings.Contains(reason, `"api"`) {
		t.Errorf("web reason = %q, want the dependency that took it down", reason)
	}
	for _, name := range []string{"api", "web"} {
		if started := len(h.runner.StartedFor(name)); started != 1 {
			t.Errorf("%s started %d times, want 1", name, started)
		}
	}
}

// A successful one-shot dependency is not a lost dependency. The pattern of
// running a migration once and then serving is the reason: the dependency did
// everything that was asked of it.
func TestACleanExitDoesNotTakeDependentsDown(t *testing.T) {
	specs := []service.Spec{
		spec("migrate"),
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("migrate") }),
	}
	h := newHarness(t, specs, process.Options{})
	h.waitState("migrate", service.StateRunning)
	h.waitState("api", service.StateRunning)

	h.runner.StartedFor("migrate")[0].Exit(0)
	h.waitState("migrate", service.StateExited)

	// The release is attempted on the same goroutine that published the exit,
	// so this interval is enough to catch a release that should not happen.
	time.Sleep(30 * time.Millisecond)
	if state := h.status("api").State; state != service.StateRunning {
		t.Fatalf("api = %s after its one-shot dependency exited cleanly, want running", state)
	}
	if requests := h.runner.StartedFor("api")[0].StopRequests(); requests != 0 {
		t.Errorf("api was asked to stop %d time(s) for a clean exit", requests)
	}
}

// A dependent that never managed to start is in the other half of the same
// question, and it must fail rather than wait forever for a dependency that
// cannot come up.
func TestADependentFailsWhenItsDependencyCannotStart(t *testing.T) {
	runner := newFailingRunner(errors.New("no such file or directory"))
	specs := []service.Spec{
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("db") }),
		spec("db"),
	}
	h := newHarness(t, specs, process.Options{Runner: runner})

	h.waitState("db", service.StateCrashed)
	h.waitState("api", service.StateCrashed)

	if reason := h.status("api").Reason; !strings.Contains(reason, `"db"`) {
		t.Errorf("api reason = %q, want the dependency named", reason)
	}
	if started := len(h.runner.Started()); started != 0 {
		t.Errorf("api was started %d time(s) with no dependency to run against", started)
	}
}

// A dependency that is required to be healthy holds its dependent back until a
// check passes, and then releases it.
func TestAHealthyDependencyReleasesItsDependent(t *testing.T) {
	prober := &fakeProber{}
	specs := []service.Spec{
		spec("db", func(s *service.Spec) { s.Health = healthCheck() }),
		spec("api", func(s *service.Spec) {
			s.DependsOn = []service.Dependency{{Name: "db", Condition: service.ReadyWhenHealthy}}
		}),
	}
	h := newHarness(t, specs, process.Options{Prober: prober})

	// The first check fails, so db is running but not usable and api is held
	// back with a reason a person can read. An unhealthy dependency is still
	// working on it rather than dead, so nothing is crashed for it.
	h.waitFor("the unhealthy dependency to be reported", func() bool {
		return strings.Contains(h.status("api").Reason, "healthy")
	})
	if state := h.status("api").State; state != service.StateStarting {
		t.Fatalf("api = %s while db was unhealthy, want starting", state)
	}
	if started := len(h.runner.StartedFor("api")); started != 0 {
		t.Fatalf("api started %d time(s) while db was unhealthy", started)
	}

	prober.setHealthy(true)
	// The clock is only moved by the test, so the next check happens when the
	// test says so rather than when the machine feels like it.
	h.clock.Advance(time.Second)
	h.waitState("api", service.StateRunning)
}

// Health is not death. A dependency that has gone unhealthy after its
// dependent started is a reason to wait and try again, not a reason to take a
// working service down.
func TestAnUnhealthyDependencyDoesNotTakeDependentsDown(t *testing.T) {
	prober := &fakeProber{}
	specs := []service.Spec{
		spec("db", func(s *service.Spec) { s.Health = healthCheck() }),
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("db") }),
	}
	h := newHarness(t, specs, process.Options{Prober: prober})
	h.waitState("api", service.StateRunning)

	h.waitFor("db to be reported unhealthy", func() bool {
		return h.status("db").Health.State == service.HealthUnhealthy
	})
	if state := h.status("api").State; state != service.StateRunning {
		t.Fatalf("api = %s while db was unhealthy, want running", state)
	}
	if requests := h.runner.StartedFor("api")[0].StopRequests(); requests != 0 {
		t.Errorf("api was asked to stop %d time(s) for an unhealthy dependency", requests)
	}
}

// A service that was stood down because its dependency died was not broken by
// it, so starting that dependency again has to bring it back rather than
// leaving a person to start it by hand.
func TestAReleasedDependentComesBackWhenItsDependencyDoes(t *testing.T) {
	specs := []service.Spec{
		spec("db"),
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("db") }),
	}
	h := newHarness(t, specs, process.Options{})
	h.waitState("api", service.StateRunning)

	h.runner.StartedFor("db")[0].Crash(1)
	h.waitState("db", service.StateCrashed)
	h.waitState("api", service.StateStopped)

	if err := h.manager.Start(context.Background(), "db"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitState("db", service.StateRunning)
	h.waitState("api", service.StateRunning)

	if started := len(h.runner.StartedFor("api")); started != 2 {
		t.Errorf("api started %d times, want 2 (the original and the recovery)", started)
	}
	if got, want := h.runner.StartedFor("api")[1].PID(), h.runner.StartedFor("api")[0].PID(); got == want {
		t.Errorf("the recovered api reused pid %d, so it is not a new process", got)
	}
}

// A chain recovers in the order it started: a service is not started against a
// dependency that has been released itself but has not come back yet, and it is
// not crashed for it either.
func TestARestoredChainRecoversInDependencyOrder(t *testing.T) {
	specs := []service.Spec{
		spec("db"),
		spec("api", func(s *service.Spec) { s.DependsOn = dependsOn("db") }),
		spec("web", func(s *service.Spec) { s.DependsOn = dependsOn("api") }),
	}
	h := newHarness(t, specs, process.Options{})
	for _, name := range []string{"db", "api", "web"} {
		h.waitState(name, service.StateRunning)
	}

	h.runner.StartedFor("db")[0].Crash(1)
	for _, name := range []string{"api", "web"} {
		h.waitState(name, service.StateStopped)
	}

	if err := h.manager.Start(context.Background(), "db"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		h.waitState(name, service.StateRunning)
	}

	// The dependency was started again before the service that needs it, and
	// nothing in the chain was reported as crashing: a release is not a
	// failure of the service that was released.
	second := secondStarts(h)
	api, order := second["api"], second["web"]
	if api == 0 || order == 0 {
		t.Fatalf("the chain was not started a second time: %v", second)
	}
	if api > order {
		t.Errorf("web was restarted (position %d) before api (position %d)", order, api)
	}
	for _, name := range []string{"api", "web"} {
		if index := h.transitionTo(name, service.StateCrashed); index >= 0 {
			t.Errorf("%s was reported as crashed during recovery", name)
		}
	}
}

// A service with two dependencies is restored by whichever one comes back, and
// stays stopped while the other is still down. It goes back to stopped rather
// than crashing: nothing of it failed this time, and it has to be restorable
// again when the second dependency returns.
func TestARestoredDependentKeepsWaitingForItsSecondDependency(t *testing.T) {
	specs := []service.Spec{
		spec("one"),
		spec("two"),
		spec("app", func(s *service.Spec) {
			s.DependsOn = []service.Dependency{
				{Name: "one", Condition: service.ReadyWhenStarted},
				{Name: "two", Condition: service.ReadyWhenStarted},
			}
		}),
	}
	h := newHarness(t, specs, process.Options{})
	h.waitState("app", service.StateRunning)

	// The two dependencies die one after the other, and each death is waited
	// for. Crashing both at once would leave the service benched by whichever
	// notice the manager happened to read first, which is a fact about the
	// channel rather than about the rule under test.
	h.runner.StartedFor("one")[0].Crash(1)
	h.waitState("app", service.StateStopped)
	if reason := h.status("app").Reason; !strings.Contains(reason, `"one"`) {
		t.Errorf("app reason = %q, want the dependency that benched it", reason)
	}
	h.runner.StartedFor("two")[0].Crash(1)
	h.waitState("two", service.StateCrashed)
	if state := h.status("app").State; state != service.StateStopped {
		t.Fatalf("app = %s, want stopped", state)
	}

	// The first dependency returns, and the service is still held back by the
	// second one. It must not be started, and it must not be crashed either.
	if err := h.manager.Start(context.Background(), "one"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitFor("app to name the second dependency", func() bool {
		return strings.Contains(h.status("app").Reason, `"two"`)
	})
	if state := h.status("app").State; state != service.StateStopped {
		t.Fatalf("app = %s while one of its dependencies was still down, want stopped", state)
	}
	if started := len(h.runner.StartedFor("app")); started != 1 {
		t.Errorf("app started %d times, want 1: it was never released by its second dependency", started)
	}

	// The second dependency returns, and now the service can run again.
	if err := h.manager.Start(context.Background(), "two"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitState("app", service.StateRunning)
	if index := h.transitionTo("app", service.StateCrashed); index >= 0 {
		t.Error("app was reported as crashed while its dependencies were recovering")
	}
}

// secondStarts maps each service to the position in the start order of its
// second process.
func secondStarts(h *harness) map[string]int {
	positions := make(map[string]int)
	seen := make(map[string]int)
	for i, handle := range h.runner.Started() {
		name := handle.Spec().Name
		seen[name]++
		if seen[name] == 2 {
			positions[name] = i
		}
	}
	return positions
}

// healthCheck is a health check that is valid and cheap to describe, because
// the prober is what answers it in these tests.
func healthCheck() *service.HealthCheck {
	return &service.HealthCheck{
		Kind:     service.ProbeHTTP,
		Target:   "http://127.0.0.1:8080/healthz",
		Interval: time.Second,
		Timeout:  time.Second,
	}
}

// fakeProber answers health checks with whatever the test decided, so health
// behaviour is verified without a network or a clock of its own.
type fakeProber struct {
	mu      sync.Mutex
	healthy bool
	checks  int
}

func (p *fakeProber) Probe(context.Context, service.HealthCheck) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks++
	if p.healthy {
		return nil
	}
	return errors.New("connection refused")
}

func (p *fakeProber) setHealthy(healthy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = healthy
}
