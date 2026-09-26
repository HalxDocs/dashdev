package process_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/platform/platformtest"
	"github.com/HalxDocs/dashdev/internal/process"
	"github.com/HalxDocs/dashdev/internal/process/processtest"
	"github.com/HalxDocs/dashdev/internal/service"
)

// waitTimeout is how long a test waits for a lifecycle outcome. It is generous
// because the tests run under the race detector on shared CI machines; a
// healthy run never comes close to it.
const waitTimeout = 5 * time.Second

// traceEntry is one event in the order the manager published it. Ordering is
// the thing under test in several cases, so the harness keeps a single ordered
// record rather than one per service.
type traceEntry struct {
	kind   events.Kind
	name   string
	from   service.State
	to     service.State
	text   string
	reason string
}

// transition records a lifecycle move so a test can assert that every one of
// them was legal.
type transition struct {
	name string
	from service.State
	to   service.State
	// previous is the state the harness had recorded before this event, which
	// must equal from.
	previous service.State
}

// harness drives a manager and keeps a faithful model of what it published.
type harness struct {
	t      *testing.T
	runner *processtest.Runner
	clock  *platformtest.Clock
	bus    *events.Bus
	store  *logs.Store

	manager *process.Manager
	cancel  context.CancelCauseFunc
	stopped chan error

	stopOnce      sync.Once
	stopErr       error
	consumer      sync.WaitGroup
	stopConsuming context.CancelFunc

	mu          sync.Mutex
	statuses    map[string]service.Status
	order       []string
	trace       []traceEntry
	transitions []transition
	lines       []logs.Line
	stats       map[string]platform.Stats
}

// newHarness starts a manager with the given specs and waits until it is
// running. options.Runner, Store, Clock and Sink are supplied by the harness
// unless the caller set them.
func newHarness(t *testing.T, specs []service.Spec, options process.Options) *harness {
	t.Helper()

	h := &harness{
		t:        t,
		runner:   processtest.NewRunner(),
		clock:    platformtest.NewClock(),
		bus:      events.NewBus(),
		store:    logs.NewStore(64),
		statuses: make(map[string]service.Status),
		stats:    make(map[string]platform.Stats),
		stopped:  make(chan error, 1),
	}

	if options.Runner == nil {
		options.Runner = h.runner
	}
	if options.Store == nil {
		options.Store = h.store
	}
	if options.Clock == nil {
		options.Clock = h.clock
	}
	if options.Sink == nil {
		options.Sink = h.bus
	}

	manager, err := process.NewManager(specs, options)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h.manager = manager

	subscription := h.bus.Subscribe(0)
	ctx, cancel := context.WithCancelCause(context.Background())
	h.cancel = cancel

	// The consumer's lifetime is deliberately longer than the manager's: the
	// manager publishes its last events while shutting down, and a harness that
	// stopped reading the moment shutdown began would miss exactly the events
	// the shutdown tests are about.
	consumeCtx, stopConsuming := context.WithCancel(context.Background())
	h.stopConsuming = stopConsuming
	h.consumer.Add(1)
	go func() {
		defer h.consumer.Done()
		h.consume(consumeCtx, subscription)
	}()
	go func() { h.stopped <- manager.Run(ctx) }()

	t.Cleanup(func() {
		h.stop(context.Canceled)
		h.bus.Close()
		drained := make(chan struct{})
		go func() {
			h.consumer.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(waitTimeout):
			stopConsuming()
			t.Error("the event consumer did not drain after the bus closed")
		}
		if remaining := h.runner.Unexited(); remaining != 0 {
			t.Errorf("%d process(es) were never reaped", remaining)
		}
		h.checkTransitions()
	})
	return h
}

// stop shuts the manager down and waits for it to finish. It is safe to call
// more than once, so a test can assert on the shutdown and the cleanup still
// works.
func (h *harness) stop(cause error) {
	h.t.Helper()
	h.stopOnce.Do(func() {
		h.cancel(cause)
		select {
		case h.stopErr = <-h.stopped:
		case <-time.After(waitTimeout):
			h.t.Fatalf("manager did not stop after its context was cancelled")
		}
	})
}

// consume applies events in publication order, exactly as the dashboard does.
func (h *harness) consume(ctx context.Context, subscription *events.Subscription) {
	for {
		batch, err := subscription.Pop(ctx)
		if err != nil {
			return
		}
		h.mu.Lock()
		for _, event := range batch {
			h.apply(event)
		}
		h.mu.Unlock()
	}
}

func (h *harness) apply(event events.Event) {
	switch event := event.(type) {
	case events.ServiceAdded:
		h.order = append(h.order, event.Status.Name)
		h.statuses[event.Status.Name] = event.Status
		h.trace = append(h.trace, traceEntry{kind: event.Kind(), name: event.Status.Name})

	case events.StateChanged:
		previous := h.statuses[event.Status.Name].State
		h.transitions = append(h.transitions, transition{
			name: event.Status.Name, from: event.From, to: event.To, previous: previous,
		})
		h.statuses[event.Status.Name] = event.Status
		h.trace = append(h.trace, traceEntry{
			kind: event.Kind(), name: event.Status.Name, from: event.From, to: event.To, reason: event.Reason,
		})

	case events.StatusUpdated:
		h.statuses[event.Status.Name] = event.Status
		h.trace = append(h.trace, traceEntry{kind: event.Kind(), name: event.Status.Name, reason: event.Reason})

	case events.LogEmitted:
		h.lines = append(h.lines, event.Line)
		h.trace = append(h.trace, traceEntry{kind: event.Kind(), name: event.Line.Service, text: event.Line.Message})

	case events.StatsUpdated:
		h.stats[event.Name] = event.Stats
		h.trace = append(h.trace, traceEntry{kind: event.Kind(), name: event.Name})
	}
}

// checkTransitions asserts that the manager only ever published legal lifecycle
// moves, and that each event's From matched the state the harness already had.
// This is the property that lets the dashboard trust the stream.
func (h *harness) checkTransitions() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, move := range h.transitions {
		if move.previous != move.from {
			h.t.Errorf("%s: event reported %s -> %s but the previous state was %s",
				move.name, move.from, move.to, move.previous)
		}
		if !move.from.CanTransitionTo(move.to) {
			h.t.Errorf("%s: %s -> %s is not a legal lifecycle transition", move.name, move.from, move.to)
		}
	}
}

func (h *harness) status(name string) service.Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statuses[name]
}

func (h *harness) linesFor(name string) []logs.Line {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []logs.Line
	for _, line := range h.lines {
		if line.Service == name {
			out = append(out, line)
		}
	}
	return out
}

// waitFor polls until condition holds, and fails the test with reason if it
// never does.
func (h *harness) waitFor(reason string, condition func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", reason)
}

func (h *harness) waitState(name string, want service.State) {
	h.t.Helper()
	h.waitFor(name+" to be "+want.String(), func() bool {
		return h.status(name).State == want
	})
}

func (h *harness) waitStarted(name string, count int) []*processtest.Handle {
	h.t.Helper()
	h.waitFor(name+" to have started "+strconv.Itoa(count)+" time(s)", func() bool {
		return len(h.runner.StartedFor(name)) >= count
	})
	return h.runner.StartedFor(name)
}

// announcedOrder returns the services in the order they were announced.
func (h *harness) announcedOrder() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.order...)
}

// traceKinds returns the kind of every trace entry in publication order.
func (h *harness) traceKinds() []events.Kind {
	h.mu.Lock()
	defer h.mu.Unlock()
	kinds := make([]events.Kind, 0, len(h.trace))
	for _, entry := range h.trace {
		kinds = append(kinds, entry.kind)
	}
	return kinds
}

// traceIndex returns the position of the first trace entry matching the
// predicate, or -1.
func (h *harness) traceIndex(match func(traceEntry) bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, entry := range h.trace {
		if match(entry) {
			return i
		}
	}
	return -1
}

// logIndex returns the position of a log line with the given message.
func (h *harness) logIndex(message string) int {
	return h.traceIndex(func(entry traceEntry) bool {
		return entry.kind == events.KindLogEmitted && entry.text == message
	})
}

// runningIndex returns the position of the nth transition of name into state.
func (h *harness) runningIndex(name string, state service.State, nth int) int {
	seen := 0
	return h.traceIndex(func(entry traceEntry) bool {
		if entry.kind != events.KindStateChanged || entry.name != name || entry.to != state {
			return false
		}
		seen++
		return seen == nth
	})
}

// transitionTo returns the position of the first published transition of a
// service into a state, or -1 when it never went there.
func (h *harness) transitionTo(name string, state service.State) int {
	return h.traceIndex(func(entry traceEntry) bool {
		return entry.kind == events.KindStateChanged && entry.name == name && entry.to == state
	})
}

// spec builds a service definition with sensible defaults for a test.
func spec(name string, mutate ...func(*service.Spec)) service.Spec {
	definition := service.Spec{
		Name:    name,
		Argv:    []string{name},
		Restart: service.DefaultRestartConfig(),
	}
	for _, apply := range mutate {
		apply(&definition)
	}
	return definition
}
