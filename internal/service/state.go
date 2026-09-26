package service

import (
	"fmt"
	"strconv"
)

// State is a service's position in its lifecycle. It is deliberately a closed
// set of five values rather than a collection of booleans, so that a service
// can never be simultaneously "running" and "crashed".
type State uint8

const (
	// StateStarting means a start has been requested and the process is being
	// launched, or is waiting for its dependencies to become ready.
	StateStarting State = iota
	// StateRunning means the process is alive and, when a health check is
	// configured, considered ready.
	StateRunning
	// StateExited means the process finished on its own with a zero exit code.
	StateExited
	// StateCrashed means the process failed: a non-zero exit, a signal, or a
	// failure to start at all.
	StateCrashed
	// StateStopped means dashdev stopped the process on purpose.
	StateStopped
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateExited:
		return "exited"
	case StateCrashed:
		return "crashed"
	case StateStopped:
		return "stopped"
	default:
		return "state(" + strconv.FormatUint(uint64(s), 10) + ")"
	}
}

// IsTerminal reports whether the service has come to rest and will not advance
// again without an explicit action such as a restart.
func (s State) IsTerminal() bool {
	switch s {
	case StateExited, StateCrashed, StateStopped:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether s to next is a legal lifecycle transition.
// Every mutation of a service's state goes through this check, so an
// impossible lifecycle is a bug that trips in tests rather than a state the
// dashboard silently renders.
func (s State) CanTransitionTo(next State) bool {
	switch s {
	case StateStarting:
		return next == StateRunning || next == StateCrashed || next == StateStopped
	case StateRunning:
		return next == StateExited || next == StateCrashed || next == StateStopped
	case StateExited, StateCrashed, StateStopped:
		// Only an explicit (re)start leaves a terminal state.
		return next == StateStarting
	default:
		return false
	}
}

// allStates is the complete lifecycle in reporting order. It is the one place
// that enumerates states, so adding a state cannot be forgotten in a switch.
var allStates = [...]State{StateStarting, StateRunning, StateExited, StateCrashed, StateStopped}

// ParseState is the inverse of String.
func ParseState(s string) (State, error) {
	for _, candidate := range allStates {
		if candidate.String() == s {
			return candidate, nil
		}
	}
	return 0, fmt.Errorf("process: unknown state %q", s)
}
