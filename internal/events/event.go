// Package events carries the facts the process manager produces to whoever
// wants to render them.
//
// The flow is one-way by construction: the manager emits values, and a
// subscriber can do nothing but read them. There is no way to reach back into
// the manager through an event, which is what keeps the dashboard honest.
package events

import (
	"fmt"
	"time"

	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/service"
)

// Kind identifies an event type for logging, tests, and delivery policy.
type Kind uint8

const (
	// KindServiceAdded reports a service the manager knows about.
	KindServiceAdded Kind = iota
	// KindStateChanged reports a lifecycle transition.
	KindStateChanged
	// KindStatusUpdated reports a status change that is not a transition.
	KindStatusUpdated
	// KindLogEmitted reports one line of service output.
	KindLogEmitted
	// KindStatsUpdated reports a resource sample.
	KindStatsUpdated
)

// String implements fmt.Stringer.
func (k Kind) String() string {
	switch k {
	case KindServiceAdded:
		return "service-added"
	case KindStateChanged:
		return "state-changed"
	case KindStatusUpdated:
		return "status-updated"
	case KindLogEmitted:
		return "log-emitted"
	case KindStatsUpdated:
		return "stats-updated"
	default:
		return "kind(" + fmt.Sprint(uint8(k)) + ")"
	}
}

// Event is something that happened to a service.
//
// The set is closed: every implementation lives in this file and marks itself
// with the unexported isEvent method, so a type switch over Event is exhaustive
// and adding a case is a compile-time concern rather than a convention.
type Event interface {
	isEvent()
	// Kind reports the event type.
	Kind() Kind
}

// ServiceAdded reports a service as the manager first sees it. It is always
// emitted for every service before any other event for that service, which lets
// a subscriber build its whole view from the event stream alone.
type ServiceAdded struct {
	Status service.Status
	Time   time.Time
}

func (ServiceAdded) isEvent()   {}
func (ServiceAdded) Kind() Kind { return KindServiceAdded }

// StateChanged reports that a service moved from one lifecycle state to
// another. Status is the complete snapshot after the transition, so a
// subscriber never has to merge partial updates.
type StateChanged struct {
	Status service.Status
	From   service.State
	To     service.State
	Reason string
	Time   time.Time
}

func (StateChanged) isEvent()   {}
func (StateChanged) Kind() Kind { return KindStateChanged }

// StatusUpdated reports a change to a service's status that is not a lifecycle
// transition: a restart countdown, a health result, or a stop in progress.
// Health is deliberately not a lifecycle state, so it needs a way to reach the
// dashboard without pretending to be one.
type StatusUpdated struct {
	Status service.Status
	Reason string
	Time   time.Time
}

func (StatusUpdated) isEvent()   {}
func (StatusUpdated) Kind() Kind { return KindStatusUpdated }

// LogEmitted reports one line of service output.
type LogEmitted struct {
	Line logs.Line
}

func (LogEmitted) isEvent()   {}
func (LogEmitted) Kind() Kind { return KindLogEmitted }

// StatsUpdated reports a resource sample for a running service.
type StatsUpdated struct {
	Name  string
	Stats platform.Stats
}

func (StatsUpdated) isEvent()   {}
func (StatsUpdated) Kind() Kind { return KindStatsUpdated }

// Droppable reports whether a subscriber that has fallen behind may discard the
// event. Lifecycle facts are never droppable; a dashboard with a truncated log
// is useful, a dashboard with a service in the wrong state is not.
func Droppable(e Event) bool {
	switch e.Kind() {
	case KindLogEmitted, KindStatsUpdated:
		return true
	default:
		return false
	}
}

// Sink accepts events. It must never block the caller for long: the manager
// emits from the goroutine that owns service state, and a stalled sink would
// stall a service.
type Sink interface {
	Emit(e Event)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(e Event)

// Emit implements Sink.
func (f SinkFunc) Emit(e Event) { f(e) }
