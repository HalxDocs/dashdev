package service_test

import (
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/service"
)

// fixedNow is a stable instant so that summaries are comparable.
var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestStateStringRoundTrips(t *testing.T) {
	for _, state := range []service.State{
		service.StateStarting, service.StateRunning, service.StateExited,
		service.StateCrashed, service.StateStopped,
	} {
		parsed, err := service.ParseState(state.String())
		if err != nil {
			t.Errorf("ParseState(%q): %v", state.String(), err)
			continue
		}
		if parsed != state {
			t.Errorf("ParseState(%q) = %v, want %v", state.String(), parsed, state)
		}
	}
}

func TestParseStateRejectsUnknownText(t *testing.T) {
	for _, text := range []string{"", "RUNNING", "runing", "state(9)", "true"} {
		if state, err := service.ParseState(text); err == nil {
			t.Errorf("ParseState(%q) = %v, want an error", text, state)
		}
	}
}

func TestStateMachineAllowsOnlyRealLifecycleMoves(t *testing.T) {
	all := []service.State{
		service.StateStarting, service.StateRunning, service.StateExited,
		service.StateCrashed, service.StateStopped,
	}
	allowed := map[service.State]map[service.State]bool{
		service.StateStarting: {
			service.StateRunning: true, service.StateCrashed: true, service.StateStopped: true,
		},
		service.StateRunning: {
			service.StateExited: true, service.StateCrashed: true, service.StateStopped: true,
		},
		service.StateExited:  {service.StateStarting: true},
		service.StateCrashed: {service.StateStarting: true},
		service.StateStopped: {service.StateStarting: true},
	}

	for _, from := range all {
		for _, to := range all {
			got := from.CanTransitionTo(to)
			want := allowed[from][to]
			if got != want {
				t.Errorf("%s -> %s = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTerminalStatesAreTheOnesThatComeToRest(t *testing.T) {
	for _, state := range []service.State{service.StateExited, service.StateCrashed, service.StateStopped} {
		if !state.IsTerminal() {
			t.Errorf("%s should be terminal", state)
		}
	}
	for _, state := range []service.State{service.StateStarting, service.StateRunning} {
		if state.IsTerminal() {
			t.Errorf("%s should not be terminal", state)
		}
	}
}

func TestReadyDependsOnTheCondition(t *testing.T) {
	running := service.Status{State: service.StateRunning}
	if !running.Ready(service.ReadyWhenStarted) {
		t.Error("a running service should satisfy the started condition")
	}
	if running.Ready(service.ReadyWhenHealthy) {
		t.Error("a running service with no health result should not satisfy the healthy condition")
	}

	healthy := service.Status{
		State:  service.StateRunning,
		Health: service.HealthStatus{State: service.HealthHealthy},
	}
	if !healthy.Ready(service.ReadyWhenHealthy) {
		t.Error("a healthy service should satisfy the healthy condition")
	}

	for _, state := range []service.State{service.StateStarting, service.StateExited, service.StateCrashed, service.StateStopped} {
		status := service.Status{State: state, Health: service.HealthStatus{State: service.HealthHealthy}}
		if status.Ready(service.ReadyWhenStarted) || status.Ready(service.ReadyWhenHealthy) {
			t.Errorf("%s should not be ready", state)
		}
	}
}

func TestSummaryDescribesEachState(t *testing.T) {
	cases := []struct {
		status service.Status
		want   string
	}{
		{service.Status{State: service.StateStarting, Reason: "waiting for db"}, "starting: waiting for db"},
		{service.Status{State: service.StateStarting}, "starting"},
		{service.Status{State: service.StateRunning, PID: 42, Started: fixedNow}, "up 1.5s (pid 42)"},
		{service.Status{State: service.StateExited}, "exited cleanly"},
		{service.Status{State: service.StateStopped}, "stopped"},
		{service.Status{State: service.StateCrashed, Reason: "exit code 7"}, "crashed: exit code 7"},
		{service.Status{State: service.StateCrashed, ExitCode: 9}, "crashed with exit code 9"},
	}
	for _, test := range cases {
		if got := test.status.Summary(fixedNow.Add(1500 * time.Millisecond)); got != test.want {
			t.Errorf("Summary() = %q, want %q", got, test.want)
		}
	}
}
