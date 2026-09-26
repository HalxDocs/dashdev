package service_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/HalxDocs/dashdev/internal/service"
)

func TestPlanOrdersServicesAfterTheirDependencies(t *testing.T) {
	specs := []service.Spec{
		serviceFor("web", "cache"),
		serviceFor("cache", "db"),
		serviceFor("db"),
		serviceFor("worker"),
	}

	stages, err := service.Plan(specs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	want := [][]string{{"db", "worker"}, {"cache"}, {"web"}}
	if len(stages) != len(want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
	for i, stage := range stages {
		if !slices.Equal(stage, want[i]) {
			t.Errorf("stage %d = %v, want %v", i, stage, want[i])
		}
	}
}

func TestPlanKeepsEveryServiceInExactlyOneStage(t *testing.T) {
	specs := []service.Spec{
		serviceFor("a"), serviceFor("b", "a"), serviceFor("c", "a", "b"),
	}
	stages, err := service.Plan(specs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	seen := make(map[string]int)
	for _, stage := range stages {
		for _, name := range stage {
			seen[name]++
		}
	}
	if len(seen) != len(specs) {
		t.Errorf("planned %d services, want %d", len(seen), len(specs))
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("%s appears %d times in the plan, want once", name, count)
		}
	}
}

func TestPlanReportsCycleWithItsPath(t *testing.T) {
	specs := []service.Spec{
		serviceFor("a", "c"),
		serviceFor("b", "a"),
		serviceFor("c", "b"),
	}

	_, err := service.Plan(specs)
	if !errors.Is(err, service.ErrCycle) {
		t.Fatalf("Plan error = %v, want ErrCycle", err)
	}

	var cycle *service.CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("error %v is not a CycleError", err)
	}
	if len(cycle.Path) < 3 {
		t.Errorf("cycle path = %v, want the services that form the loop", cycle.Path)
	}
	if cycle.Path[0] != cycle.Path[len(cycle.Path)-1] {
		t.Errorf("cycle path = %v, want it to return to where it started", cycle.Path)
	}
}

func TestPlanReportsUnknownDependency(t *testing.T) {
	_, err := service.Plan([]service.Spec{serviceFor("web", "db")})
	if !errors.Is(err, service.ErrUnknownDependency) {
		t.Fatalf("Plan error = %v, want ErrUnknownDependency", err)
	}
	var missing *service.UnknownDependencyError
	if !errors.As(err, &missing) {
		t.Fatalf("error %v is not an UnknownDependencyError", err)
	}
	if missing.Service != "web" || missing.Dependency != "db" {
		t.Errorf("error names %s depending on %s, want web depending on db", missing.Service, missing.Dependency)
	}
}

func TestPlanRefusesADuplicateName(t *testing.T) {
	_, err := service.Plan([]service.Spec{serviceFor("api"), serviceFor("api")})
	if !errors.Is(err, service.ErrDuplicateName) {
		t.Fatalf("Plan error = %v, want ErrDuplicateName", err)
	}
}

func TestPlanRefusesAnEmptyConfiguration(t *testing.T) {
	if _, err := service.Plan(nil); !errors.Is(err, service.ErrNoServices) {
		t.Fatalf("Plan error = %v, want ErrNoServices", err)
	}
}

func TestPlanIsStableAcrossRuns(t *testing.T) {
	specs := []service.Spec{
		serviceFor("z"), serviceFor("y"), serviceFor("x"), serviceFor("w", "x"),
	}
	first, err := service.Plan(specs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for range 20 {
		again, err := service.Plan(specs)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		for i := range first {
			if !slices.Equal(first[i], again[i]) {
				t.Fatalf("plan is not stable: %v then %v", first, again)
			}
		}
	}
}

// serviceFor builds a definition with no dependencies or the named ones.
func serviceFor(name string, dependencies ...string) service.Spec {
	spec := service.Spec{
		Name:    name,
		Argv:    []string{name},
		Restart: service.DefaultRestartConfig(),
	}
	for _, dependency := range dependencies {
		spec.DependsOn = append(spec.DependsOn, service.Dependency{
			Name:      dependency,
			Condition: service.ReadyWhenStarted,
		})
	}
	return spec
}
