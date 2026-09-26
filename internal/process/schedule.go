package process

import (
	"context"
	"fmt"

	"github.com/HalxDocs/dashdev/internal/service"
)

// reschedule starts services whose dependencies are ready and fails services
// whose dependencies can never become ready.
//
// It runs after every change that could unblock a service, and it loops until
// nothing moves so that a failure propagates along a whole chain in one pass
// rather than one link per event. The loop always terminates: each iteration
// permanently clears the waiting flag on at least one service, and there are
// finitely many services.
func (m *Manager) reschedule(tasks context.Context, states map[string]*serviceState) {
	for moved := true; moved; {
		moved = false
		for _, name := range m.order {
			st := states[name]
			if !st.waiting || st.disabled {
				continue
			}
			blocker, unreachable := m.blockedBy(states, st)
			switch {
			case blocker == "":
				st.waiting = false
				moved = true
				m.beginStart(tasks, states, st, "dependencies ready")
			case unreachable:
				st.waiting = false
				moved = true
				reason := fmt.Sprintf("dependency %q is %s", blocker, states[blocker].status.State)
				next := service.StateCrashed
				if st.status.State == service.StateStopped || st.releasedBy != "" {
					// It was benched by an earlier failure rather than running
					// against this one, so nothing of it failed this time. It goes
					// back to being stopped, and keeps naming the dependency that
					// is holding it, so it is restored when that one returns.
					next = service.StateStopped
					st.releasedBy = blocker
				}
				m.transition(st, next, reason)
			}
		}
	}
}

// blockedBy reports which dependency is holding a service back, and whether
// that dependency can no longer become ready.
func (m *Manager) blockedBy(states map[string]*serviceState, st *serviceState) (string, bool) {
	for _, dep := range st.spec.DependsOn {
		other, known := states[dep.Name]
		if !known {
			return dep.Name, true
		}
		if other.status.Ready(dep.Condition) {
			continue
		}
		if other.waiting {
			// It is on the bench rather than gone: it is waiting for its own
			// dependencies and will be started when they are ready, so waiting
			// for it is a plan rather than a dead end.
			m.publish(st, fmt.Sprintf("waiting for %q to become %s", dep.Name, dep.Condition))
			return dep.Name, false
		}
		if other.status.State.IsTerminal() && !other.restartPending {
			// It has stopped and is not coming back, so waiting longer is not
			// a plan.
			return dep.Name, true
		}
		if other.status.Health.State == service.HealthUnhealthy {
			// An unhealthy dependency is still retrying rather than dead, so
			// its dependents wait with a reason the dashboard can show.
			m.publish(st, fmt.Sprintf("waiting for %q to become healthy", dep.Name))
			return dep.Name, false
		}
		m.publish(st, fmt.Sprintf("waiting for %q to become %s", dep.Name, dep.Condition))
		return dep.Name, false
	}
	return "", false
}

// releaseDependents stops services that are still running after a dependency
// they need has ended for good.
//
// reschedule answers one half of the dependency question: may this service
// start? This answers the other half, which is the one the dashboard's central
// claim rests on — a running service has what it needs. A service that started
// while its dependency was healthy would otherwise keep running against
// something that is dead, and the dashboard would show a green service whose
// prerequisite is gone. Instead it is stopped, with the dependency named as the
// reason.
//
// It deliberately does not loop to a fixpoint. Each dependent's own exit runs
// this again, and a dependent that was released by a failed dependency counts
// as lost in its turn, so a failure travels along a whole chain one link per
// exit rather than in one pass. Nothing here waits for anything: beginStop
// hands the work to the runner and returns.
func (m *Manager) releaseDependents(tasks context.Context, states map[string]*serviceState) {
	for _, name := range m.order {
		st := states[name]
		// Only a service with a live process needs releasing. One that is
		// waiting is reschedule's business, and one that is already on its way
		// down or has been taken down on purpose must not be touched again.
		if st.handle == nil || st.stopping || st.disabled {
			continue
		}
		blocker, lost := m.lostDependency(states, st)
		if !lost {
			continue
		}
		// The name is recorded here because it is what distinguishes "dashdev
		// took this down because something it needs died" from "the user
		// stopped it", and it is what lets the loss reach this service's own
		// dependents.
		st.releasedBy = blocker
		m.beginStop(tasks, st, fmt.Sprintf("dependency %q is %s", blocker, states[blocker].status.State))
	}
}

// restoreDependents starts the services that a dependency took down with it,
// now that the dependency has started again.
//
// releaseDependents answers "the thing this service needed has died"; this is
// the other direction, and the pair is what makes a dependency failure
// survivable rather than permanent. A service that was stood down was not
// broken, it was benched, and it comes back when the thing it needs does.
//
// A restored service is put back on the bench rather than started here, so that
// its own dependencies are checked again: a chain recovers in the same order it
// started, and a service whose second dependency is still down waits instead of
// being started against it. Nothing loops, because every restored service that
// reaches running clears its own bench in its turn.
func (m *Manager) restoreDependents(tasks context.Context, states map[string]*serviceState, restored *serviceState) {
	moved := false
	for _, name := range m.order {
		st := states[name]
		if st.releasedBy != restored.spec.Name {
			continue
		}
		// The name is deliberately left in place: beginStart clears it when the
		// service actually runs again, so a dependency that fails twice in a
		// row still finds its dependents on the bench.
		st.waiting = true
		st.disabled = false
		moved = true
	}
	if moved {
		m.reschedule(tasks, states)
	}
}

// lostDependency reports the first dependency of st that has ended for good.
//
// A dependency that is restarting is not lost: stopping its dependents would
// turn a recovering dependency into an outage, which is the opposite of what a
// restart policy is for. A dependency that exited cleanly is not lost either —
// the service it released was never broken by it — so only a crash that is not
// coming back, and a service that was itself released by such a crash, count.
func (m *Manager) lostDependency(states map[string]*serviceState, st *serviceState) (string, bool) {
	for _, dep := range st.spec.DependsOn {
		other, known := states[dep.Name]
		if !known {
			continue
		}
		if other.releasedBy != "" {
			return dep.Name, true
		}
		if other.status.State == service.StateCrashed && !other.restartPending {
			return dep.Name, true
		}
	}
	return "", false
}
