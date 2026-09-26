package process

import (
	"context"
	"sync"

	"github.com/HalxDocs/dashdev/internal/service"
)

// serviceState is the mutable state of one service.
//
// Every field is owned by the manager's Run goroutine. Worker goroutines read
// values out of it (a name, a spec, a handle) before they are started and never
// write back; the two WaitGroups are the only fields designed to be used from
// more than one goroutine, and they are only waited on, never mutated, by
// anyone but the owner.
type serviceState struct {
	spec   service.Spec
	status service.Status

	// handle is the live process, or nil when nothing is running.
	handle Handle
	// generation increments on every start. Notices carry the generation they
	// belong to so a message from a replaced process cannot affect its
	// successor.
	generation uint64

	// readers tracks the goroutines draining this generation's output, and
	// probes tracks its health checks. Both are waited on before the next
	// generation starts, which is what stops a stale log line from landing in
	// the new instance's buffer.
	readers sync.WaitGroup
	probes  sync.WaitGroup

	cancelProbes context.CancelFunc

	// stopping reports that a stop has been requested for the live process, so
	// its exit is intentional rather than a crash.
	stopping   bool
	stopReason string
	// disabled reports that the service must not be restarted: it was stopped
	// on purpose, or dashdev is shutting down.
	disabled bool
	// waiting reports that the service has not started yet because its
	// dependencies are not ready.
	waiting bool
	// restartPending reports that a backoff timer is running for this service.
	restartPending bool
	// restartAfterStop reports that the current stop is the first half of a
	// restart, so the service should start again when it has finished stopping.
	restartAfterStop bool
	// releasedBy names the dependency whose permanent failure took this
	// service down, and it stays set for as long as the service is benched by
	// it. It is empty for a service that is running, and for one that was
	// stopped for any other reason, which is what separates a benched service
	// from a deliberately stopped one. It is cleared by a start, and it is what
	// lets a failure travel along a chain of dependents and back again.
	releasedBy string

	// attempt counts consecutive restart attempts and is reset by an explicit
	// start or restart.
	attempt int
	// healthFailures counts consecutive failed checks.
	healthFailures int
}

func newServiceState(spec service.Spec) *serviceState {
	return &serviceState{
		spec: spec,
		// A service begins in the starting phase: at `dashdev up` it is either
		// being launched or waiting for a dependency, and both are starting.
		status: service.Status{
			Name:   spec.Name,
			State:  service.StateStarting,
			Health: service.HealthStatus{State: service.HealthUnknown},
		},
	}
}
