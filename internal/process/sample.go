package process

import (
	"context"
	"time"

	"github.com/HalxDocs/dashdev/internal/events"
)

// armSample returns a channel that fires when the next resource sample is due,
// or nil when nothing is being monitored. A nil channel blocks forever in a
// select, which is exactly the behaviour wanted: no sample timer and no cost
// for a configuration that does not ask for monitoring.
func (m *Manager) armSample(tasks context.Context, states map[string]*serviceState) <-chan time.Time {
	if m.sampler == nil {
		return nil
	}
	for _, st := range states {
		if st.spec.Monitor && st.handle != nil {
			return m.clock.After(m.sampleInterval)
		}
	}
	return nil
}

// sample reads resource usage for every monitored service.
func (m *Manager) sample(states map[string]*serviceState) {
	for _, name := range m.order {
		st := states[name]
		if !st.spec.Monitor || st.handle == nil || m.sampler == nil {
			continue
		}
		stats, err := m.sampler.Sample(st.handle.PID())
		if err != nil {
			// A process that vanished between the check and the read is not an
			// error worth reporting; its exit is already on its way.
			continue
		}
		m.sink.Emit(events.StatsUpdated{Name: name, Stats: stats})
	}
}

// forgetSampler drops the sampler's history for a process that has ended, so a
// long-lived dashdev does not accumulate measurements for dead processes.
func (m *Manager) forgetSampler(pid int) {
	if m.sampler == nil || pid <= 0 {
		return
	}
	m.sampler.Forget(pid)
}
