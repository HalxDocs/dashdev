// Package config reads dashdev.yaml and turns it into the service definitions
// the process manager runs.
//
// The split of responsibility is deliberate: this package owns the file format,
// its defaults and its validation, and hands back service.Spec values that are
// already resolved. Nothing downstream knows that YAML was involved.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/service"
)

// FileName is the configuration file dashdev looks for.
const FileName = "dashdev.yaml"

// Version is the only configuration format version this build understands.
// Absent means one, because a file with no version is not interestingly
// different from a version one file.
const Version = 1

// Health check defaults, applied when a service configures a check but leaves
// the timing to dashdev.
const (
	DefaultHealthInterval = 10 * time.Second
	DefaultHealthTimeout  = 2 * time.Second
	DefaultHealthRetries  = 3
)

// Loaded is a validated configuration together with where it came from.
//
// Dir matters: service directories written as relative paths are resolved
// against the configuration file's own directory rather than the invocation
// working directory, so `dashdev up` does the same thing from anywhere.
type Loaded struct {
	// Path is the absolute path of the file that was read.
	Path string
	// Dir is the directory containing that file.
	Dir string

	specs []service.Spec
}

// Specs returns the resolved services in declaration order. The result is
// owned by Loaded and must be treated as read-only.
func (l *Loaded) Specs() []service.Spec { return l.specs }

// Names returns the configured service names in declaration order.
func (l *Loaded) Names() []string {
	names := make([]string, 0, len(l.specs))
	for _, spec := range l.specs {
		names = append(names, spec.Name)
	}
	return names
}

// Load reads, decodes, validates and resolves the configuration at path.
//
// Every problem found is reported at once rather than one per run, because a
// service file is usually edited in batches.
func Load(path string) (*Loaded, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}

	file, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	defer file.Close()

	var schema fileSchema
	decoder := yaml.NewDecoder(file)
	// Unknown fields are rejected rather than ignored: a typo such as
	// `commandd:` would otherwise silently start nothing.
	decoder.KnownFields(true)
	switch err := decoder.Decode(&schema); {
	case errors.Is(err, io.EOF):
		return nil, fmt.Errorf("%w: %s: file is empty", ErrInvalid, absolute)
	case err != nil:
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, absolute, err)
	}

	var trailing any
	err = decoder.Decode(&trailing)
	if err == nil && trailing != nil {
		return nil, fmt.Errorf("%w: %s: expected a single YAML document", ErrInvalid, absolute)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, absolute, err)
	}

	dir := filepath.Dir(absolute)
	if err := schema.validate(dir); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, absolute, err)
	}

	specs := schema.build(dir)
	// Ordering is validated here as well as in the manager so that a cycle is a
	// configuration error the user sees immediately, not a surprise at startup.
	if _, err := service.Plan(specs); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, absolute, err)
	}

	return &Loaded{Path: absolute, Dir: dir, specs: specs}, nil
}

// Find looks for FileName in startDir and then in each of its parents,
// returning the first match.
func Find(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	for {
		candidate := filepath.Join(dir, FileName)
		info, err := os.Stat(candidate)
		switch {
		case err == nil && !info.IsDir():
			return candidate, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w: no %s in %s or any parent directory", ErrNotFound, FileName, startDir)
		}
		dir = parent
	}
}

// build resolves the schema into service definitions. It assumes validate has
// already succeeded, so it never has to report a parse failure twice.
func (f fileSchema) build(baseDir string) []service.Spec {
	defaults := f.Defaults
	baseRestart := defaults.Restart.restartConfig()
	baseEnv := envVars(defaults.Env)

	specs := make([]service.Spec, 0, len(f.Services))
	for _, declared := range f.Services {
		directory := declared.Directory
		if directory == "" {
			directory = defaults.Directory
		}

		spec := service.Spec{
			Name:      declared.Name,
			Argv:      declared.argv(),
			Dir:       resolveDir(baseDir, directory),
			Env:       mergeEnvVars(baseEnv, envVars(declared.Env)),
			Restart:   declared.Restart.restartConfigFrom(baseRestart),
			DependsOn: dependencies(declared.DependsOn),
			Monitor:   flagOr(declared.Monitor, flagOr(defaults.Monitor, false)),
		}
		if declared.Health != nil {
			check := declared.Health.healthCheck()
			spec.Health = &check
		}
		specs = append(specs, spec)
	}
	return specs
}

// argv returns the command to run, honouring an explicit request for a shell.
func (s serviceSchema) argv() []string {
	if s.Shell != nil && *s.Shell {
		return platform.ShellArgv(s.Command.raw)
	}
	return slices.Clone(s.Command.argv)
}

// resolveDir makes a configured directory absolute against the configuration
// file's location.
func resolveDir(baseDir, directory string) string {
	switch {
	case directory == "":
		return baseDir
	case filepath.IsAbs(directory):
		return filepath.Clean(directory)
	default:
		return filepath.Clean(filepath.Join(baseDir, directory))
	}
}

func envVars(entries []envSchema) []service.EnvVar {
	if len(entries) == 0 {
		return nil
	}
	out := make([]service.EnvVar, 0, len(entries))
	for _, entry := range entries {
		out = append(out, service.EnvVar{Name: entry.Name, Value: entry.Value})
	}
	return out
}

// mergeEnvVars lays service overrides over the defaults. Service entries win,
// and order is deterministic: defaults first, then new service entries in the
// order they were declared.
func mergeEnvVars(base, overrides []service.EnvVar) []service.EnvVar {
	if len(base) == 0 {
		return slices.Clone(overrides)
	}
	out := slices.Clone(base)
	index := make(map[string]int, len(out))
	for i, env := range out {
		index[env.Name] = i
	}
	for _, env := range overrides {
		if i, exists := index[env.Name]; exists {
			out[i] = env
			continue
		}
		index[env.Name] = len(out)
		out = append(out, env)
	}
	return out
}

func dependencies(entries []dependsEntry) []service.Dependency {
	if len(entries) == 0 {
		return nil
	}
	out := make([]service.Dependency, 0, len(entries))
	for _, entry := range entries {
		// Validation has already rejected an unparseable condition.
		condition, err := service.ParseCondition(entry.Condition)
		if err != nil {
			condition = service.ReadyWhenStarted
		}
		out = append(out, service.Dependency{Name: entry.Name, Condition: condition})
	}
	return out
}

// restartConfig turns the defaults block into a policy.
func (r *restartSchema) restartConfig() service.RestartConfig {
	return r.restartConfigFrom(service.DefaultRestartConfig())
}

// restartConfigFrom layers a service's restart block over an inherited policy,
// field by field, so a service can raise max_attempts without restating the
// backoff curve.
func (r *restartSchema) restartConfigFrom(base service.RestartConfig) service.RestartConfig {
	if r == nil {
		return base
	}
	out := base
	if r.Policy != "" {
		// Validation has already rejected an unknown policy.
		if policy, err := service.ParseRestartPolicy(r.Policy); err == nil {
			out.Policy = policy
		}
	}
	if r.MaxAttempts != nil {
		out.MaxAttempts = *r.MaxAttempts
	}
	if r.Backoff != nil {
		if r.Backoff.Base != 0 {
			out.Backoff.Base = r.Backoff.Base.value()
		}
		if r.Backoff.Max != 0 {
			out.Backoff.Max = r.Backoff.Max.value()
		}
		if r.Backoff.Factor != nil {
			out.Backoff.Factor = *r.Backoff.Factor
		}
	}
	return out
}

func (h *healthSchema) healthCheck() service.HealthCheck {
	// Validation has already rejected an unknown probe type.
	kind, err := service.ParseProbeKind(h.Type)
	if err != nil {
		kind = service.ProbeHTTP
	}
	check := service.HealthCheck{
		Kind:        kind,
		Target:      h.Target,
		Interval:    DefaultHealthInterval,
		Timeout:     DefaultHealthTimeout,
		Retries:     DefaultHealthRetries,
		StartPeriod: 0,
	}
	if h.Interval != 0 {
		check.Interval = h.Interval.value()
	}
	if h.Timeout != 0 {
		check.Timeout = h.Timeout.value()
	}
	if h.Retries != nil {
		check.Retries = *h.Retries
	}
	if h.StartPeriod != 0 {
		check.StartPeriod = h.StartPeriod.value()
	}
	return check
}

func flagOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
