package service_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/service"
)

func TestSplitCommandReadsQuotingLikeAShell(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty", input: "", want: nil},
		{name: "white space only", input: "   \t ", want: nil},
		{name: "simple words", input: "go run ./cmd/api", want: []string{"go", "run", "./cmd/api"}},
		{name: "collapses separators", input: "go    run\t./cmd/api", want: []string{"go", "run", "./cmd/api"}},
		{name: "single quotes are literal", input: "sh -c 'echo one && echo two'",
			want: []string{"sh", "-c", "echo one && echo two"}},
		{name: "single quotes keep backslashes", input: `echo 'a\b'`, want: []string{"echo", `a\b`}},
		{name: "double quotes keep spaces", input: `run "my app" --flag`,
			want: []string{"run", "my app", "--flag"}},
		{name: "double quotes allow an escaped quote", input: `run "say \"hi\""`,
			want: []string{"run", `say "hi"`}},
		{name: "double quotes allow an escaped backslash", input: `run "a\\b"`,
			want: []string{"run", `a\b`}},
		{name: "double quotes leave other backslashes alone", input: `run "C:\tools"`,
			want: []string{"run", `C:\tools`}},
		{name: "escaped space outside quotes", input: `run my\ app`, want: []string{"run", "my app"}},
		{name: "a windows path survives", input: `C:\tools\app.exe --serve`,
			want: []string{`C:\tools\app.exe`, "--serve"}},
		{name: "quotes can be empty", input: `run ""`, want: []string{"run", ""}},
		{name: "single quotes inside double quotes", input: `run "it's fine"`,
			want: []string{"run", "it's fine"}},
		{name: "double quotes inside single quotes", input: `run 'say "hi"'`,
			want: []string{"run", `say "hi"`}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.SplitCommand(test.input)
			if err != nil {
				t.Fatalf("SplitCommand(%q): %v", test.input, err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("SplitCommand(%q) = %q, want %q", test.input, got, test.want)
			}
			for i := range test.want {
				if got[i] != test.want[i] {
					t.Fatalf("SplitCommand(%q) = %q, want %q", test.input, got, test.want)
				}
			}
		})
	}
}

func TestSplitCommandRefusesToGuess(t *testing.T) {
	// An unbalanced quote or a dangling escape is reported rather than
	// interpreted, because guessing wrong starts the wrong program.
	for _, input := range []string{"echo 'unterminated", `echo "unterminated`, `echo trailing\`} {
		if got, err := service.SplitCommand(input); err == nil {
			t.Errorf("SplitCommand(%q) = %q, want an error", input, got)
		}
	}
}

func TestSplitCommandNeverExpandsAnything(t *testing.T) {
	// A configuration file is not a shell: globs and variables stay literal.
	got, err := service.SplitCommand("echo $HOME *.go")
	if err != nil {
		t.Fatalf("SplitCommand: %v", err)
	}
	want := []string{"echo", "$HOME", "*.go"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SplitCommand = %q, want %q", got, want)
		}
	}
}

func TestSpecValidateAcceptsAReasonableService(t *testing.T) {
	spec := service.Spec{
		Name:    "api",
		Argv:    []string{"go", "run", "."},
		Restart: service.DefaultRestartConfig(),
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestSpecValidateReportsEveryProblem(t *testing.T) {
	spec := service.Spec{
		Name: "api",
		Env: []service.EnvVar{
			{Name: "", Value: "1"},
		},
		Restart: service.RestartConfig{Policy: service.RestartNever, MaxAttempts: -1},
		Health: &service.HealthCheck{
			Kind: service.ProbeHTTP, Target: "", Interval: 0, Timeout: 0, Retries: -1,
		},
		DependsOn: []service.Dependency{
			{Name: "api"},
			{Name: "db"},
			{Name: "db"},
		},
	}

	err := spec.Validate()
	if !errors.Is(err, service.ErrInvalidSpec) {
		t.Fatalf("Validate error = %v, want ErrInvalidSpec", err)
	}
	message := err.Error()
	for _, want := range []string{
		"command must not be empty",
		"environment variable name",
		"max_attempts",
		"target must not be empty",
		"interval",
		"retries",
		"depends on itself",
		"listed twice",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error does not mention %q:\n%s", want, message)
		}
	}
}

func TestSpecValidateNamesTheService(t *testing.T) {
	spec := service.Spec{Name: "worker"}
	err := spec.Validate()
	if err == nil {
		t.Fatal("Validate accepted a service with no command")
	}
	if !strings.Contains(err.Error(), "worker") {
		t.Errorf("error = %v, want it to name the service", err)
	}
}

func TestRestartPolicyRoundTrips(t *testing.T) {
	for _, policy := range []service.RestartPolicy{
		service.RestartNever, service.RestartOnFailure, service.RestartAlways,
	} {
		parsed, err := service.ParseRestartPolicy(policy.String())
		if err != nil {
			t.Errorf("ParseRestartPolicy(%q): %v", policy.String(), err)
			continue
		}
		if parsed != policy {
			t.Errorf("ParseRestartPolicy(%q) = %v, want %v", policy.String(), parsed, policy)
		}
	}
	if _, err := service.ParseRestartPolicy("sometimes"); err == nil {
		t.Error("ParseRestartPolicy accepted an unknown policy")
	}
}

func TestBackoffGrowsAndThenStopsGrowing(t *testing.T) {
	config := service.RestartConfig{
		Policy: service.RestartOnFailure,
		Backoff: service.Backoff{
			Base:   100 * time.Millisecond,
			Max:    time.Second,
			Factor: 2,
		},
	}

	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}
	for i, expected := range want {
		if got := config.Delay(i + 1); got != expected {
			t.Errorf("Delay(%d) = %v, want %v", i+1, got, expected)
		}
	}
}

func TestBackoffFallsBackToSensibleDefaults(t *testing.T) {
	config := service.DefaultRestartConfig()
	if got := config.Delay(1); got != service.DefaultBackoffBase {
		t.Errorf("first delay = %v, want the default base %v", got, service.DefaultBackoffBase)
	}
	// A zero delay would mean a service restarting thousands of times a second.
	if got := config.Delay(0); got <= 0 {
		t.Errorf("Delay(0) = %v, want a positive delay", got)
	}
	if got := config.Delay(40); got > service.DefaultBackoffMax {
		t.Errorf("a late delay = %v, want at most the default ceiling %v", got, service.DefaultBackoffMax)
	}
}

func TestRestartConfigValidate(t *testing.T) {
	bad := service.RestartConfig{
		Policy:      service.RestartAlways,
		MaxAttempts: -1,
		Backoff:     service.Backoff{Base: -time.Second, Max: -time.Second, Factor: 0.5},
	}
	err := bad.Validate()
	if err == nil {
		t.Fatal("Validate accepted an unusable restart configuration")
	}
	for _, want := range []string{"max_attempts", "backoff durations", "factor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if err := service.DefaultRestartConfig().Validate(); err != nil {
		t.Errorf("the default configuration is not valid: %v", err)
	}
}

func TestHealthCheckValidate(t *testing.T) {
	check := service.HealthCheck{
		Kind: service.ProbeHTTP, Target: "http://localhost:8080/",
		Interval: time.Second, Timeout: time.Second,
	}
	if err := check.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	broken := service.HealthCheck{Kind: service.ProbeTCP}
	if err := broken.Validate(); err == nil {
		t.Error("Validate accepted a check with no target and no timings")
	}
}

func TestConditionRoundTrips(t *testing.T) {
	for _, condition := range []service.Condition{service.ReadyWhenStarted, service.ReadyWhenHealthy} {
		parsed, err := service.ParseCondition(condition.String())
		if err != nil {
			t.Fatalf("ParseCondition(%q): %v", condition.String(), err)
		}
		if parsed != condition {
			t.Errorf("ParseCondition(%q) = %v, want %v", condition.String(), parsed, condition)
		}
	}
	if _, err := service.ParseCondition("eventually"); err == nil {
		t.Error("ParseCondition accepted an unknown condition")
	}
}
