package service

import (
	"errors"
	"fmt"
	"time"
)

// ProbeKind identifies how a health check reaches its target.
type ProbeKind uint8

const (
	// ProbeHTTP issues a GET request and treats a 2xx response as healthy.
	ProbeHTTP ProbeKind = iota
	// ProbeTCP opens a TCP connection to the target address.
	ProbeTCP
	// ProbeExec runs a command and treats a zero exit as healthy.
	ProbeExec
)

// String implements fmt.Stringer.
func (k ProbeKind) String() string {
	switch k {
	case ProbeHTTP:
		return "http"
	case ProbeTCP:
		return "tcp"
	case ProbeExec:
		return "exec"
	default:
		return "probe(" + fmt.Sprint(uint8(k)) + ")"
	}
}

// ParseProbeKind is the inverse of String.
func ParseProbeKind(s string) (ProbeKind, error) {
	for _, k := range []ProbeKind{ProbeHTTP, ProbeTCP, ProbeExec} {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, fmt.Errorf("process: unknown probe kind %q", s)
}

// HealthCheck describes how to decide whether a running service is usable.
type HealthCheck struct {
	Kind        ProbeKind
	Target      string
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	StartPeriod time.Duration
}

// Validate reports why the health check could not be executed.
func (h HealthCheck) Validate() error {
	var problems []error
	if h.Target == "" {
		problems = append(problems, fmt.Errorf("target must not be empty"))
	}
	if h.Interval <= 0 {
		problems = append(problems, fmt.Errorf("interval must be greater than zero"))
	}
	if h.Timeout <= 0 {
		problems = append(problems, fmt.Errorf("timeout must be greater than zero"))
	}
	if h.Retries < 0 {
		problems = append(problems, fmt.Errorf("retries must not be negative"))
	}
	if h.StartPeriod < 0 {
		problems = append(problems, fmt.Errorf("start_period must not be negative"))
	}
	return errors.Join(problems...)
}

// HealthState is the outcome of the most recent health check.
type HealthState uint8

const (
	// HealthUnknown means no check has completed yet.
	HealthUnknown HealthState = iota
	// HealthStarting means the start period has not elapsed yet.
	HealthStarting
	// HealthHealthy means the most recent check succeeded.
	HealthHealthy
	// HealthUnhealthy means the check has failed more times than allowed.
	HealthUnhealthy
)

// String implements fmt.Stringer.
func (h HealthState) String() string {
	switch h {
	case HealthUnknown:
		return "unknown"
	case HealthStarting:
		return "starting"
	case HealthHealthy:
		return "healthy"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return "health(" + fmt.Sprint(uint8(h)) + ")"
	}
}

// HealthStatus is the current health of a service. It is deliberately separate
// from State: a process can be running and unhealthy at the same time, and
// collapsing the two would make that state unrepresentable.
type HealthStatus struct {
	State    HealthState
	Since    time.Time
	Message  string
	Failures int
}

// Condition is the bar a dependency must clear before its dependents start.
type Condition uint8

const (
	// ReadyWhenStarted only requires the process to be running.
	ReadyWhenStarted Condition = iota
	// ReadyWhenHealthy requires the health check to pass.
	ReadyWhenHealthy
)

// String implements fmt.Stringer.
func (c Condition) String() string {
	switch c {
	case ReadyWhenStarted:
		return "started"
	case ReadyWhenHealthy:
		return "healthy"
	default:
		return "condition(" + fmt.Sprint(uint8(c)) + ")"
	}
}

// ParseCondition is the inverse of String.
func ParseCondition(s string) (Condition, error) {
	for _, c := range []Condition{ReadyWhenStarted, ReadyWhenHealthy} {
		if c.String() == s {
			return c, nil
		}
	}
	return 0, fmt.Errorf("process: unknown condition %q", s)
}
