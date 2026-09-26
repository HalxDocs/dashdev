package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"

	"github.com/HalxDocs/dashdev/internal/service"
)

// Prober runs a service's health check.
type Prober interface {
	// Probe returns nil when the target is healthy, and an error describing
	// the failure otherwise.
	Probe(ctx context.Context, check service.HealthCheck) error
}

// NewProber returns a Prober that performs real network and process checks.
func NewProber() Prober {
	return &prober{client: &http.Client{}}
}

// prober holds the HTTP client rather than using a package-level one, so that
// nothing in this project is global mutable state.
type prober struct {
	client *http.Client
}

var _ Prober = (*prober)(nil)

// Probe implements Prober. The check's own timeout bounds every kind of probe,
// so a hung target cannot stall the manager's health loop.
func (p *prober) Probe(ctx context.Context, check service.HealthCheck) error {
	probeCtx, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()

	switch check.Kind {
	case service.ProbeHTTP:
		return p.probeHTTP(probeCtx, check.Target)
	case service.ProbeTCP:
		return probeTCP(probeCtx, check.Target)
	case service.ProbeExec:
		return probeExec(probeCtx, check.Target)
	default:
		return fmt.Errorf("process: cannot run a %s probe", check.Kind)
	}
}

func (p *prober) probeHTTP(ctx context.Context, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	// The body is drained and discarded so the connection can be reused, but
	// only a bounded prefix is read: a health endpoint that streams forever
	// must not be able to hold the check open past its timeout.
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	_ = response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("GET %s returned %s", target, response.Status)
	}
	return nil
}

func probeTCP(ctx context.Context, target string) error {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	return connection.Close()
}

func probeExec(ctx context.Context, target string) error {
	argv, err := service.SplitCommand(target)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("exec probe has no command")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", argv[0], err, firstLine(output.String()))
	}
	return nil
}

// firstLine keeps a probe failure readable: the dashboard shows one line, so
// reporting an entire command's output would be noise.
func firstLine(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "no output"
	}
	if index := strings.IndexByte(trimmed, '\n'); index >= 0 {
		trimmed = trimmed[:index]
	}
	const limit = 200
	if len(trimmed) > limit {
		return trimmed[:limit] + "..."
	}
	return trimmed
}

// startProbes begins health checking for a service, if it declared a check.
func (m *Manager) startProbes(tasks context.Context, st *serviceState, generation uint64) {
	if st.spec.Health == nil || m.prober == nil {
		return
	}
	check := *st.spec.Health
	probeCtx, cancel := context.WithCancel(tasks)
	st.cancelProbes = cancel

	st.probes.Go(func() {
		if check.StartPeriod > 0 {
			// A service is allowed a settling period before its failures count,
			// which is the difference between a health check and a race.
			select {
			case <-m.clock.After(check.StartPeriod):
			case <-probeCtx.Done():
				return
			}
		}
		for {
			err := m.prober.Probe(probeCtx, check)
			outcome := probeOutcome{passed: err == nil, at: m.clock.Now()}
			if err != nil {
				outcome.message = err.Error()
			}
			report := notice{
				kind:       noticeProbe,
				name:       st.spec.Name,
				generation: generation,
				probe:      outcome,
				at:         outcome.at,
			}
			select {
			case m.done <- report:
			case <-probeCtx.Done():
				return
			}
			select {
			case <-m.clock.After(check.Interval):
			case <-probeCtx.Done():
				return
			}
		}
	})
}

// stopProbes ends health checking for a service and waits for the goroutine to
// leave, so a replaced generation cannot report into its successor.
func (m *Manager) stopProbes(st *serviceState) {
	if st.cancelProbes != nil {
		st.cancelProbes()
		st.cancelProbes = nil
	}
	st.probes.Wait()
}

// handleProbe applies a health result. Health is not a lifecycle state: a
// service can be running and unhealthy at once, and it stays running while the
// failures accumulate.
func (m *Manager) handleProbe(tasks context.Context, states map[string]*serviceState, report notice) {
	st, known := states[report.name]
	if !known || st.generation != report.generation {
		return
	}

	if report.probe.passed {
		st.healthFailures = 0
		if st.status.Health.State != service.HealthHealthy {
			st.status.Health = service.HealthStatus{State: service.HealthHealthy, Since: report.probe.at}
			m.publish(st, "health check passed")
			m.reschedule(tasks, states)
		}
		return
	}

	st.healthFailures++
	tolerance := 0
	if st.spec.Health != nil {
		tolerance = st.spec.Health.Retries
	}
	if st.healthFailures <= tolerance {
		m.publish(st, fmt.Sprintf("health check failed (%d/%d): %s",
			st.healthFailures, tolerance+1, report.probe.message))
		return
	}
	if st.status.Health.State == service.HealthUnhealthy {
		return
	}
	st.status.Health = service.HealthStatus{
		State:    service.HealthUnhealthy,
		Since:    report.probe.at,
		Message:  report.probe.message,
		Failures: st.healthFailures,
	}
	m.publish(st, "health check failing: "+report.probe.message)
	m.reschedule(tasks, states)
}
