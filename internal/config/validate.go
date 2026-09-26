package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/HalxDocs/dashdev/internal/service"
)

// serviceNamePattern keeps names usable as identifiers in a dashboard and in
// error messages. The first character is restricted so a name can never be
// mistaken for a flag or an empty string.
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validate checks the whole file and reports every problem it finds.
//
// It runs before anything is started, which is the point: a service definition
// that cannot work should fail while the user is still looking at the file, not
// after four other services are already running.
func (f fileSchema) validate(baseDir string) error {
	var problems []error

	if f.Version != 0 && f.Version != Version {
		problems = append(problems, fieldf("version", "unsupported version %d, this build understands %d", f.Version, Version))
	}
	if len(f.Services) == 0 {
		problems = append(problems, fmt.Errorf("%w: define at least one service in `services`", service.ErrNoServices))
	}
	if f.Defaults.Directory != "" {
		problems = append(problems, checkDirectory("defaults.directory", baseDir, f.Defaults.Directory))
	}
	if f.Defaults.Restart != nil {
		problems = append(problems, f.Defaults.Restart.validate("defaults.restart", service.DefaultRestartConfig())...)
	}
	problems = append(problems, validateEnv("defaults.env", map[string]int{}, f.Defaults.Env)...)

	seen := make(map[string]int, len(f.Services))
	for i, declared := range f.Services {
		path := fmt.Sprintf("services[%d]", i)
		problems = append(problems, declared.validate(path, baseDir, f.Defaults.Directory)...)

		if declared.Name == "" {
			continue
		}
		if first, exists := seen[declared.Name]; exists {
			problems = append(problems, fieldf(path+".name",
				"service %q is already defined by services[%d]; names must be unique", declared.Name, first))
			continue
		}
		seen[declared.Name] = i
	}

	// Dependency existence is checked after every name is known, so a
	// dependency declared later in the file is not mistaken for a typo.
	for i, declared := range f.Services {
		for j, dependency := range declared.DependsOn {
			if dependency.Name == "" {
				continue
			}
			if _, exists := seen[dependency.Name]; !exists {
				problems = append(problems, fieldf(
					fmt.Sprintf("services[%d].depends_on[%d]", i, j),
					"service %q does not exist", dependency.Name))
			}
		}
	}

	return errors.Join(problems...)
}

func (s serviceSchema) validate(path, baseDir, inheritedDir string) []error {
	var problems []error

	switch {
	case s.Name == "":
		problems = append(problems, fieldf(path+".name", "name must not be empty"))
	case !serviceNamePattern.MatchString(s.Name):
		problems = append(problems, fieldf(path+".name",
			"%q is not a valid service name; use letters, digits, dot, dash or underscore", s.Name))
	}

	problems = append(problems, s.Command.validate(path+".command")...)

	if s.Shell != nil && *s.Shell && s.Command.list {
		problems = append(problems, fieldf(path+".shell",
			"shell: true needs a single command string, not a list of arguments"))
	}

	directory := s.Directory
	if directory == "" {
		directory = inheritedDir
	}
	if directory != "" {
		problems = append(problems, checkDirectory(path+".directory", baseDir, directory))
	}

	problems = append(problems, validateEnv(path+".env", map[string]int{}, s.Env)...)

	if s.Health != nil {
		problems = append(problems, s.Health.validate(path+".health")...)
	}
	if s.Restart != nil {
		problems = append(problems, s.Restart.validate(path+".restart", service.DefaultRestartConfig())...)
	}

	for i, dependency := range s.DependsOn {
		depPath := fmt.Sprintf("%s.depends_on[%d]", path, i)
		if dependency.Name == "" {
			problems = append(problems, fieldf(depPath, "dependency name must not be empty"))
			continue
		}
		if dependency.Name == s.Name {
			problems = append(problems, fieldf(depPath, "service %q cannot depend on itself", s.Name))
		}
		if dependency.Condition == "" {
			continue
		}
		if _, err := service.ParseCondition(dependency.Condition); err != nil {
			problems = append(problems, fieldf(depPath,
				"unknown condition %q; use started or healthy", dependency.Condition))
		}
	}

	return problems
}

func (c commandSchema) validate(path string) []error {
	var problems []error
	if len(c.argv) == 0 {
		problems = append(problems, fieldf(path, "command must not be empty"))
		return problems
	}
	if c.argv[0] == "" {
		problems = append(problems, fieldf(path, "command program must not be empty"))
		return problems
	}
	if c.list {
		// A list is taken verbatim, so an empty argument is a mistake worth
		// reporting rather than something to strip.
		for i, argument := range c.argv {
			if argument == "" {
				problems = append(problems, fieldf(fmt.Sprintf("%s[%d]", path, i), "argument must not be empty"))
			}
		}
	}
	return problems
}

func (h healthSchema) validate(path string) []error {
	var problems []error
	if h.Type == "" {
		problems = append(problems, fieldf(path+".type", "type is required; use http, tcp or exec"))
	} else if _, err := service.ParseProbeKind(h.Type); err != nil {
		problems = append(problems, fieldf(path+".type", "unknown type %q; use http, tcp or exec", h.Type))
	}
	if h.Target == "" {
		problems = append(problems, fieldf(path+".target", "target must not be empty"))
	} else if h.Type == string(service.ProbeExec.String()) {
		if _, err := service.SplitCommand(h.Target); err != nil {
			problems = append(problems, fieldf(path+".target", "%s", err))
		}
	}
	if h.Interval < 0 {
		problems = append(problems, fieldf(path+".interval", "interval must not be negative"))
	}
	if h.Timeout < 0 {
		problems = append(problems, fieldf(path+".timeout", "timeout must not be negative"))
	}
	if h.Retries != nil && *h.Retries < 0 {
		problems = append(problems, fieldf(path+".retries", "retries must not be negative"))
	}
	if h.StartPeriod < 0 {
		problems = append(problems, fieldf(path+".start_period", "start_period must not be negative"))
	}
	return problems
}

func (r restartSchema) validate(path string, base service.RestartConfig) []error {
	var problems []error

	// The effective policy is the one declared here if there is one, and the
	// inherited policy otherwise. Consulting the inherited value instead would
	// warn that max_attempts is pointless on the very service that has just
	// asked for restarts.
	policy := base.Policy
	if r.Policy != "" {
		parsed, err := service.ParseRestartPolicy(r.Policy)
		if err != nil {
			problems = append(problems, fieldf(path+".policy",
				"unknown policy %q; use never, on-failure or always", r.Policy))
		} else {
			policy = parsed
		}
	}
	if r.MaxAttempts != nil && *r.MaxAttempts < 0 {
		problems = append(problems, fieldf(path+".max_attempts", "max_attempts must not be negative"))
	}
	if r.Backoff != nil {
		if r.Backoff.Base < 0 {
			problems = append(problems, fieldf(path+".backoff.base", "base must not be negative"))
		}
		if r.Backoff.Max < 0 {
			problems = append(problems, fieldf(path+".backoff.max", "max must not be negative"))
		}
		if r.Backoff.Max != 0 && r.Backoff.Base != 0 && r.Backoff.Max < r.Backoff.Base {
			problems = append(problems, fieldf(path+".backoff.max", "max must not be less than base"))
		}
		if r.Backoff.Factor != nil && *r.Backoff.Factor < 1 {
			problems = append(problems, fieldf(path+".backoff.factor", "factor must be at least 1"))
		}
	}
	if r.MaxAttempts != nil && policy == service.RestartNever && *r.MaxAttempts > 0 {
		problems = append(problems, fieldf(path+".max_attempts",
			"max_attempts has no effect while the policy is never"))
	}
	return problems
}

func validateEnv(path string, names map[string]int, entries []envSchema) []error {
	var problems []error
	for i, entry := range entries {
		entryPath := fmt.Sprintf("%s[%d]", path, i)
		if entry.Name == "" {
			problems = append(problems, fieldf(entryPath+".name", "name must not be empty"))
			continue
		}
		if first, exists := names[entry.Name]; exists {
			problems = append(problems, fieldf(entryPath+".name",
				"%q is already set by %s[%d]", entry.Name, path, first))
			continue
		}
		names[entry.Name] = i
	}
	return problems
}

func checkDirectory(path, baseDir, directory string) error {
	resolved := resolveDir(baseDir, directory)
	info, err := os.Stat(resolved)
	switch {
	case err != nil:
		return fieldf(path, "%s does not exist (resolved to %s)", directory, resolved)
	case !info.IsDir():
		return fieldf(path, "%s is not a directory (resolved to %s)", directory, resolved)
	default:
		return nil
	}
}
