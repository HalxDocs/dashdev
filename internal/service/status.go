package service

import (
	"strconv"
	"time"

	"github.com/HalxDocs/dashdev/internal/platform"
)

// Status is an immutable snapshot of a service. Values are copied rather than
// shared, which is what allows the dashboard to hold state that the manager
// owns without a lock between them.
type Status struct {
	Name     string
	State    State
	PID      int
	ExitCode int
	Started  time.Time
	Stopped  time.Time
	Restarts int
	// Reason is the latest notable explanation for the current state, such as
	// a crash cause, a restart countdown or a wait for dependencies.
	Reason        string
	NextRestartAt time.Time
	Health        HealthStatus
	Stats         platform.Stats
}

// Ready reports whether cond is satisfied by this status.
func (s Status) Ready(cond Condition) bool {
	if s.State != StateRunning {
		return false
	}
	if cond == ReadyWhenHealthy {
		return s.Health.State == HealthHealthy
	}
	return true
}

// Uptime reports how long the service has been running as of now, or how long
// it ran before it stopped.
func (s Status) Uptime(now time.Time) time.Duration {
	if s.Started.IsZero() {
		return 0
	}
	if !s.Stopped.IsZero() {
		return s.Stopped.Sub(s.Started)
	}
	return now.Sub(s.Started)
}

// Summary renders the one-line description shown in the dashboard's detail
// pane.
func (s Status) Summary(now time.Time) string {
	switch s.State {
	case StateRunning:
		return "up " + shortDuration(s.Uptime(now)) + " (pid " + strconv.Itoa(s.PID) + ")"
	case StateStarting:
		if s.Reason != "" {
			return "starting: " + s.Reason
		}
		return "starting"
	case StateExited:
		return "exited cleanly"
	case StateStopped:
		return "stopped"
	case StateCrashed:
		if s.Reason != "" {
			return "crashed: " + s.Reason
		}
		return "crashed with exit code " + strconv.Itoa(s.ExitCode)
	default:
		return s.State.String()
	}
}

// shortDuration renders an uptime compactly without losing the resolution a
// person reads a dashboard for: ninety seconds is "1m30s", not "1m".
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	case d < time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d < time.Hour:
		minutes := int64(d / time.Minute)
		seconds := int64(d % time.Minute / time.Second)
		if seconds == 0 {
			return strconv.FormatInt(minutes, 10) + "m"
		}
		return strconv.FormatInt(minutes, 10) + "m" + strconv.FormatInt(seconds, 10) + "s"
	default:
		hours := int64(d / time.Hour)
		minutes := int64(d % time.Hour / time.Minute)
		if minutes == 0 {
			return strconv.FormatInt(hours, 10) + "h"
		}
		return strconv.FormatInt(hours, 10) + "h" + strconv.FormatInt(minutes, 10) + "m"
	}
}
