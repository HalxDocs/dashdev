// Package process runs services and owns their lifecycle.
//
// The package is built around one rule: a service's mutable state belongs to a
// single goroutine. Manager.Run starts that goroutine and owns every field of
// every service until it returns. Everything else in dashdev talks to the
// manager by sending it a request or by reading the event stream, which is why
// this package contains almost no mutexes and why the race detector has very
// little to find.
package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/service"
)

// Defaults applied when Options leaves a value unset.
const (
	// DefaultGracePeriod is how long a service is given to exit after being
	// asked politely.
	DefaultGracePeriod = 5 * time.Second
	// DefaultLogCapacity is how many lines are kept per service.
	DefaultLogCapacity = 1000
	// DefaultSampleInterval is how often resource usage is read.
	DefaultSampleInterval = 2 * time.Second
	// doneBuffer bounds the queue of notifications from worker goroutines to
	// the owner. It only has to be big enough that a burst of simultaneous
	// events does not make a worker wait.
	doneBuffer = 64
)

// stopSlack is added to the grace period to bound a single stop operation.
const stopSlack = 5 * time.Second

// Options configures a Manager. Only Runner is required.
type Options struct {
	// Runner starts and controls processes. Use NewExecRunner for real
	// services, or a fake for tests.
	Runner Runner
	// Sink receives lifecycle events. A nil sink discards them.
	Sink events.Sink
	// Store holds the per-service log buffers. A nil store is created with
	// DefaultLogCapacity.
	Store *logs.Store
	// Clock supplies time. A nil clock uses the system clock.
	Clock platform.Clock
	// Sampler reads resource usage when a service opts in with monitor: true.
	Sampler platform.Sampler
	// Prober runs health checks for services that declare them.
	Prober Prober
	// GracePeriod is how long a service has to stop cleanly.
	GracePeriod time.Duration
	// SampleInterval is how often monitored services are sampled.
	SampleInterval time.Duration
}

// Manager owns the lifecycle of every configured service.
type Manager struct {
	specs   []service.Spec
	order   []string
	byName  map[string]int
	stages  []service.Stage
	runner  Runner
	sink    events.Sink
	store   *logs.Store
	clock   platform.Clock
	prober  Prober
	sampler platform.Sampler
	grace   time.Duration

	sampleInterval time.Duration

	requests chan request
	done     chan notice

	runOnce  sync.Once
	finished chan struct{}

	// draining is owned by the run goroutine. It is set once the shutdown has
	// begun, so that an exit which the shutdown caused is not reported as a
	// service failure.
	draining bool
}

// NewManager validates specs and returns a manager ready to run.
//
// Validation happens here as well as in the configuration loader because a
// manager can be built programmatically, and a service definition that cannot
// work should fail before anything is started rather than halfway through.
func NewManager(specs []service.Spec, options Options) (*Manager, error) {
	if options.Runner == nil {
		return nil, fmt.Errorf("%w: a runner is required", ErrInvalidOptions)
	}

	// Plan is the single source of truth for whether the dependency graph can
	// be ordered at all: duplicate names, unknown dependencies and cycles are
	// all reported here, before a single process exists.
	stages, err := service.Plan(specs)
	if err != nil {
		return nil, err
	}

	var problems []error
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			problems = append(problems, err)
		}
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}

	manager := &Manager{
		specs:          specs,
		order:          make([]string, 0, len(specs)),
		byName:         make(map[string]int, len(specs)),
		stages:         stages,
		runner:         options.Runner,
		sink:           options.Sink,
		store:          options.Store,
		clock:          options.Clock,
		prober:         options.Prober,
		sampler:        options.Sampler,
		grace:          options.GracePeriod,
		sampleInterval: options.SampleInterval,
		requests:       make(chan request, doneBuffer),
		done:           make(chan notice, doneBuffer),
		finished:       make(chan struct{}),
	}
	if manager.sink == nil {
		manager.sink = events.SinkFunc(func(events.Event) {})
	}
	if manager.store == nil {
		manager.store = logs.NewStore(DefaultLogCapacity)
	}
	if manager.clock == nil {
		manager.clock = platform.System()
	}
	if manager.grace < 0 {
		return nil, fmt.Errorf("%w: grace period must not be negative", ErrInvalidOptions)
	}
	if manager.grace == 0 {
		manager.grace = DefaultGracePeriod
	}
	if manager.sampleInterval <= 0 {
		manager.sampleInterval = DefaultSampleInterval
	}

	for i, spec := range specs {
		manager.order = append(manager.order, spec.Name)
		manager.byName[spec.Name] = i
	}
	return manager, nil
}

// Store returns the log buffers this manager writes to.
func (m *Manager) Store() *logs.Store { return m.store }

// Specs returns the services this manager runs, in declaration order.
func (m *Manager) Specs() []service.Spec { return m.specs }

// Stages returns the dependency order validated at construction. It is exposed
// for diagnostics: the manager itself starts a service as soon as its
// dependencies are ready rather than waiting for a whole stage.
func (m *Manager) Stages() []service.Stage { return m.stages }

// Run starts every service and owns their state until ctx is done.
//
// Run returns when every service has been stopped and reaped, so a caller that
// waits for it knows nothing of dashdev's processes is left behind. A nil error
// means the shutdown was orderly; only a failure to record the reason for
// stopping is reported.
func (m *Manager) Run(ctx context.Context) error {
	var err error
	m.runOnce.Do(func() { err = m.run(ctx) })
	return err
}

// Restart tears a service down and starts it again. It returns once the
// replacement has been launched, not once it has become healthy.
func (m *Manager) Restart(ctx context.Context, name string) error {
	return m.sendAction(ctx, actionRestart, name)
}

// Stop stops a single service and marks it as deliberately stopped, so the
// restart policy does not immediately bring it back.
func (m *Manager) Stop(ctx context.Context, name string) error {
	return m.sendAction(ctx, actionStop, name)
}

// Start starts a service that is not running. It is the undo of Stop.
func (m *Manager) Start(ctx context.Context, name string) error {
	return m.sendAction(ctx, actionStart, name)
}

// Snapshot returns the current status of every service in declaration order.
func (m *Manager) Snapshot(ctx context.Context) ([]service.Status, error) {
	reply := make(chan snapshotReply, 1)
	if err := m.send(ctx, &snapshotRequest{reply: reply}); err != nil {
		return nil, err
	}
	result, err := awaitReply(m, ctx, reply)
	if err != nil {
		return nil, err
	}
	return result.statuses, result.err
}

// sendAction performs a state-changing action and waits for it to be accepted.
func (m *Manager) sendAction(ctx context.Context, act action, name string) error {
	reply := make(chan error, 1)
	if err := m.send(ctx, &actionRequest{action: act, name: name, reply: reply}); err != nil {
		return err
	}
	result, err := awaitReply(m, ctx, reply)
	if err != nil {
		return err
	}
	return result
}

// send hands a request to the owner goroutine.
//
// A caller can never be left waiting on a manager that is not going to answer.
// The finished channel is checked before the send, raced against it, and raced
// again against the reply: a request that lands in the queue after the owner
// has drained its last one is still refused rather than ignored.
func (m *Manager) send(ctx context.Context, req request) error {
	select {
	case <-m.finished:
		return ErrStopped
	default:
	}

	select {
	case m.requests <- req:
		return nil
	case <-m.finished:
		return ErrStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// awaitReply waits for the answer to a request, or for the manager to stop.
func awaitReply[T any](m *Manager, ctx context.Context, reply <-chan T) (T, error) {
	var zero T
	select {
	case value := <-reply:
		return value, nil
	case <-m.finished:
		return zero, ErrStopped
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
