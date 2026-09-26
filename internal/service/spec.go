package service

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// EnvVar is a single environment override for a service.
type EnvVar struct {
	Name  string
	Value string
}

// RestartPolicy decides whether a service is started again after it stops.
type RestartPolicy uint8

const (
	// RestartNever is the default: a service is started once.
	RestartNever RestartPolicy = iota
	// RestartOnFailure restarts a service unless it exited cleanly or was
	// stopped on purpose.
	RestartOnFailure
	// RestartAlways restarts a service whatever ended it, including a clean
	// exit.
	RestartAlways
)

// String implements fmt.Stringer.
func (p RestartPolicy) String() string {
	switch p {
	case RestartNever:
		return "never"
	case RestartOnFailure:
		return "on-failure"
	case RestartAlways:
		return "always"
	default:
		return "policy(" + fmt.Sprint(uint8(p)) + ")"
	}
}

// ParseRestartPolicy is the inverse of String.
func ParseRestartPolicy(s string) (RestartPolicy, error) {
	for _, p := range []RestartPolicy{RestartNever, RestartOnFailure, RestartAlways} {
		if p.String() == s {
			return p, nil
		}
	}
	return 0, fmt.Errorf("process: unknown restart policy %q", s)
}

// Backoff describes the delay between restart attempts.
type Backoff struct {
	Base   time.Duration
	Max    time.Duration
	Factor float64
}

// Default backoff values applied when a policy leaves them unset.
const (
	DefaultBackoffBase = 250 * time.Millisecond
	DefaultBackoffMax  = 30 * time.Second
	DefaultBackoffRate = 2.0
)

// RestartConfig is the complete restart behaviour of a service.
type RestartConfig struct {
	Policy      RestartPolicy
	MaxAttempts int // zero means unlimited
	Backoff     Backoff
}

// DefaultRestartConfig is the behaviour of a service that does not ask for
// restarts.
func DefaultRestartConfig() RestartConfig {
	return RestartConfig{
		Policy:  RestartNever,
		Backoff: Backoff{Base: DefaultBackoffBase, Max: DefaultBackoffMax, Factor: DefaultBackoffRate},
	}
}

// Delay returns how long to wait before restart attempt, which is 1-based.
func (c RestartConfig) Delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := c.Backoff.Base
	if base <= 0 {
		base = DefaultBackoffBase
	}
	factor := c.Backoff.Factor
	if factor < 1 {
		factor = DefaultBackoffRate
	}
	delay := float64(base) * math.Pow(factor, float64(attempt-1))
	maxDelay := c.Backoff.Max
	if maxDelay <= 0 {
		maxDelay = DefaultBackoffMax
	}
	if delay > float64(maxDelay) {
		return maxDelay
	}
	return time.Duration(delay)
}

// Validate reports why the restart configuration is unusable.
func (c RestartConfig) Validate() error {
	var problems []error
	if c.Policy > RestartAlways {
		problems = append(problems, fmt.Errorf("unknown policy %d", c.Policy))
	}
	if c.MaxAttempts < 0 {
		problems = append(problems, fmt.Errorf("max_attempts must not be negative"))
	}
	if c.Backoff.Base < 0 || c.Backoff.Max < 0 {
		problems = append(problems, fmt.Errorf("backoff durations must not be negative"))
	}
	if c.Backoff.Factor != 0 && c.Backoff.Factor < 1 {
		problems = append(problems, fmt.Errorf("backoff factor must be at least 1"))
	}
	return errors.Join(problems...)
}

// Dependency is another service that must be ready before this one starts.
type Dependency struct {
	Name      string
	Condition Condition
}

// Spec is a fully resolved service definition: everything the manager needs in
// order to run a service. It carries no YAML, no defaults and no relative
// paths; those are the configuration package's business.
type Spec struct {
	Name      string
	Argv      []string
	Dir       string
	Env       []EnvVar
	Restart   RestartConfig
	Health    *HealthCheck
	DependsOn []Dependency
	Monitor   bool
}

// Validate reports why the spec could not be run. The manager calls it for
// every service before anything starts, so a bad spec never reaches a process.
func (s Spec) Validate() error {
	var problems []error
	if s.Name == "" {
		problems = append(problems, fmt.Errorf("name must not be empty"))
	}
	if len(s.Argv) == 0 {
		problems = append(problems, fmt.Errorf("command must not be empty"))
	} else if s.Argv[0] == "" {
		problems = append(problems, fmt.Errorf("command program must not be empty"))
	}
	for _, env := range s.Env {
		if env.Name == "" {
			problems = append(problems, fmt.Errorf("environment variable name must not be empty"))
		}
	}
	if err := s.Restart.Validate(); err != nil {
		problems = append(problems, err)
	}
	if s.Health != nil {
		if err := s.Health.Validate(); err != nil {
			problems = append(problems, err)
		}
	}
	seen := make(map[string]bool, len(s.DependsOn))
	for _, dep := range s.DependsOn {
		if dep.Name == "" {
			problems = append(problems, fmt.Errorf("dependency name must not be empty"))
			continue
		}
		if dep.Name == s.Name {
			problems = append(problems, fmt.Errorf("service depends on itself"))
		}
		if seen[dep.Name] {
			problems = append(problems, fmt.Errorf("dependency %q listed twice", dep.Name))
		}
		seen[dep.Name] = true
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w %q: %w", ErrInvalidSpec, s.Name, errors.Join(problems...))
	}
	return nil
}
