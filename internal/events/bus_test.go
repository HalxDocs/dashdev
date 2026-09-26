package events_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/service"
)

// logEvent builds a droppable event, which is what a chatty service produces.
func logEvent(sequence int) events.Event {
	return events.LogEmitted{Line: logs.Line{Service: "api", Message: fmt.Sprintf("line %d", sequence)}}
}

// stateEvent builds a lifecycle fact, which is never droppable.
func stateEvent(name string) events.Event {
	return events.StateChanged{
		Status: service.Status{Name: name, State: service.StateRunning},
		From:   service.StateStarting,
		To:     service.StateRunning,
		Reason: "started",
	}
}

func popAll(t *testing.T, subscription *events.Subscription) []events.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	batch, err := subscription.Pop(ctx)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	return batch
}

func TestBusDeliversEveryEventInOrder(t *testing.T) {
	bus := events.NewBus()
	subscription := bus.Subscribe(64)

	facts := []events.Event{stateEvent("a"), logEvent(1), stateEvent("b")}
	for _, event := range facts {
		bus.Emit(event)
	}

	received := popAll(t, subscription)
	if len(received) != len(facts) {
		t.Fatalf("received %d events, want %d", len(received), len(facts))
	}
	for i, event := range received {
		if event.Kind() != facts[i].Kind() {
			t.Errorf("event %d is a %s, want %s", i, event.Kind(), facts[i].Kind())
		}
	}
}

func TestBusFansOutToOneDeliveryPerSubscriber(t *testing.T) {
	bus := events.NewBus()
	first := bus.Subscribe(8)
	second := bus.Subscribe(8)

	bus.Emit(stateEvent("api"))
	bus.Emit(logEvent(1))

	for i, subscription := range []*events.Subscription{first, second} {
		if got := len(popAll(t, subscription)); got != 2 {
			t.Errorf("subscriber %d received %d events, want 2", i, got)
		}
	}
	if bus.Subscribers() != 2 {
		t.Errorf("Subscribers() = %d, want 2", bus.Subscribers())
	}
}

// TestBusNeverBlocksOnASlowSubscriber is the property that keeps a stuck
// terminal from stalling a service. A publisher must be able to keep going
// forever while a subscriber reads nothing at all.
func TestBusNeverBlocksOnASlowSubscriber(t *testing.T) {
	bus := events.NewBus()
	slow := bus.Subscribe(16)

	const published = 10_000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range published {
			bus.Emit(logEvent(i))
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Emit blocked on a subscriber that was not reading")
	}

	if got := slow.Dropped(); got == 0 {
		t.Fatal("nothing was reported as dropped, so the subscriber was not behind")
	}

	batch, err := slow.Pop(context.Background())
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if len(batch) > 16 {
		t.Errorf("queued %d events, want at most the subscribed buffer of 16", len(batch))
	}
}

// TestBusPrefersToDropLogLinesOverLifecycleFacts records the delivery policy
// that the dashboard relies on: a truncated log is useful, a wrong lifecycle is
// not.
func TestBusPrefersToDropLogLinesOverLifecycleFacts(t *testing.T) {
	bus := events.NewBus()
	subscription := bus.Subscribe(8)

	bus.Emit(stateEvent("api"))
	for i := range 64 {
		bus.Emit(logEvent(i))
	}

	batch, err := subscription.Pop(context.Background())
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}

	var lifecycle int
	for _, event := range batch {
		if event.Kind() == events.KindStateChanged {
			lifecycle++
		}
	}
	if lifecycle != 1 {
		t.Errorf("received %d lifecycle events, want the one that was published", lifecycle)
	}
	if subscription.Dropped() == 0 {
		t.Error("no drops were reported")
	}
}

func TestBusKeepsWorkingAfterASubscriberLeaves(t *testing.T) {
	bus := events.NewBus()
	leaving := bus.Subscribe(8)
	staying := bus.Subscribe(8)

	leaving.Close()
	if got := bus.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() = %d, want 1", got)
	}
	// Closing twice is harmless: a dashboard that quits during shutdown should
	// not have to track whether it already detached.
	leaving.Close()

	bus.Emit(stateEvent("api"))
	if got := len(popAll(t, staying)); got != 1 {
		t.Errorf("the remaining subscriber received %d events, want 1", got)
	}
	if _, err := leaving.Pop(context.Background()); !errors.Is(err, events.ErrClosed) {
		t.Errorf("Pop on a closed subscription = %v, want ErrClosed", err)
	}
}

func TestBusCloseDetachesEverySubscriber(t *testing.T) {
	bus := events.NewBus()
	subscription := bus.Subscribe(8)
	bus.Emit(stateEvent("api"))

	bus.Close()
	bus.Emit(stateEvent("ignored"))

	batch, err := subscription.Pop(context.Background())
	if err != nil {
		t.Fatalf("a subscriber should drain what it already had: %v", err)
	}
	if len(batch) != 1 {
		t.Errorf("received %d events, want the one published before the bus closed", len(batch))
	}
	if _, err := subscription.Pop(context.Background()); !errors.Is(err, events.ErrClosed) {
		t.Errorf("Pop after draining = %v, want ErrClosed", err)
	}
	if got := bus.Subscribers(); got != 0 {
		t.Errorf("Subscribers() = %d after closing, want 0", got)
	}
}

func TestPopWaitsUntilThereIsSomethingToRead(t *testing.T) {
	bus := events.NewBus()
	subscription := bus.Subscribe(8)

	received := make(chan int, 1)
	go func() {
		batch, err := subscription.Pop(context.Background())
		if err != nil {
			received <- -1
			return
		}
		received <- len(batch)
	}()

	select {
	case <-received:
		t.Fatal("Pop returned before anything was published")
	case <-time.After(30 * time.Millisecond):
	}

	bus.Emit(stateEvent("api"))
	select {
	case count := <-received:
		if count != 1 {
			t.Errorf("received %d events, want 1", count)
		}
	case <-time.After(time.Second):
		t.Fatal("Pop did not return once an event was published")
	}
}

func TestPopHonoursContextCancellation(t *testing.T) {
	bus := events.NewBus()
	subscription := bus.Subscribe(8)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := subscription.Pop(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Pop with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestDroppableClassifiesEveryKind(t *testing.T) {
	droppable := map[events.Kind]bool{
		events.KindLogEmitted:    true,
		events.KindStatsUpdated:  true,
		events.KindServiceAdded:  false,
		events.KindStateChanged:  false,
		events.KindStatusUpdated: false,
	}
	for kind, want := range droppable {
		event := eventOfKind(kind)
		if got := events.Droppable(event); got != want {
			t.Errorf("Droppable(%s) = %v, want %v", kind, got, want)
		}
	}
}

func eventOfKind(kind events.Kind) events.Event {
	switch kind {
	case events.KindLogEmitted:
		return logEvent(1)
	case events.KindStateChanged:
		return stateEvent("api")
	case events.KindServiceAdded:
		return events.ServiceAdded{Status: service.Status{Name: "api"}}
	case events.KindStatsUpdated:
		return events.StatsUpdated{Name: "api"}
	default:
		return events.StatusUpdated{Status: service.Status{Name: "api"}, Reason: "reason"}
	}
}

func TestBusSubscribeDefaultsToABoundedBuffer(t *testing.T) {
	bus := events.NewBus()
	subscription := bus.Subscribe(0)
	// A non-positive buffer must not mean an unbounded queue.
	for i := range events.DefaultBuffer + 100 {
		bus.Emit(logEvent(i))
	}
	batch, err := subscription.Pop(context.Background())
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if len(batch) > events.DefaultBuffer {
		t.Errorf("queued %d events, want at most %d", len(batch), events.DefaultBuffer)
	}
}

func TestKindsHaveStableNames(t *testing.T) {
	for kind, want := range map[events.Kind]string{
		events.KindServiceAdded:  "service-added",
		events.KindStateChanged:  "state-changed",
		events.KindStatusUpdated: "status-updated",
		events.KindLogEmitted:    "log-emitted",
		events.KindStatsUpdated:  "stats-updated",
	} {
		if got := kind.String(); got != want {
			t.Errorf("Kind(%d) = %q, want %q", kind, got, want)
		}
	}
}

func TestSinkFuncAdaptsAFunction(t *testing.T) {
	var seen int
	var sink events.Sink = events.SinkFunc(func(events.Event) { seen++ })
	sink.Emit(stateEvent("api"))
	if seen != 1 {
		t.Errorf("sink saw %d events, want 1", seen)
	}
}
