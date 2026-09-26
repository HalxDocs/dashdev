package service

import (
	"slices"
)

// Stage is a group of services that have no dependency between them and may
// therefore be started at the same time.
type Stage []string

// Plan orders specs so that every service appears in a stage after the services
// it depends on. It is the single source of truth for startup order, so the
// configuration loader and the manager cannot disagree about what a
// dependency graph means.
//
// Plan reports ErrCycle with the offending path when the graph cannot be
// ordered, and ErrUnknownDependency when a service names a dependency that does
// not exist.
func Plan(specs []Spec) ([]Stage, error) {
	if len(specs) == 0 {
		return nil, ErrNoServices
	}

	index := make(map[string]int, len(specs))
	for i, spec := range specs {
		if _, exists := index[spec.Name]; exists {
			return nil, &DuplicateNameError{Name: spec.Name}
		}
		index[spec.Name] = i
	}

	// dependents[j] lists the services that must wait for service j.
	dependents := make([][]int, len(specs))
	indegree := make([]int, len(specs))
	for i, spec := range specs {
		for _, dep := range spec.DependsOn {
			j, exists := index[dep.Name]
			if !exists {
				return nil, &UnknownDependencyError{Service: spec.Name, Dependency: dep.Name}
			}
			dependents[j] = append(dependents[j], i)
			indegree[i]++
		}
	}

	var stages []Stage
	remaining := len(specs)
	ready := make([]int, 0, len(specs))
	for i := range specs {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}

	// Kahn's algorithm, one level at a time. Sorting each level by declaration
	// index keeps the plan stable across runs, which is what makes it usable in
	// tests and in a rendered dashboard.
	for len(ready) > 0 {
		slices.Sort(ready)
		stage := make(Stage, 0, len(ready))
		next := make([]int, 0, len(ready))
		for _, i := range ready {
			stage = append(stage, specs[i].Name)
			for _, dependent := range dependents[i] {
				indegree[dependent]--
				if indegree[dependent] == 0 {
					next = append(next, dependent)
				}
			}
		}
		remaining -= len(ready)
		stages = append(stages, stage)
		ready = next
	}

	if remaining > 0 {
		return nil, findCycle(specs, index, indegree)
	}
	return stages, nil
}

// findCycle walks the part of the graph Kahn's algorithm could not drain and
// returns the services that form the cycle, in order. Reporting the path rather
// than just "there is a cycle" is the difference between a usable error and a
// puzzle.
func findCycle(specs []Spec, index map[string]int, indegree []int) error {
	const (
		unvisited = iota
		onStack
		done
	)
	state := make([]int, len(specs))
	var stack []string

	var visit func(i int) []string
	visit = func(i int) []string {
		state[i] = onStack
		stack = append(stack, specs[i].Name)
		for _, dep := range specs[i].DependsOn {
			j, exists := index[dep.Name]
			if !exists {
				continue
			}
			switch state[j] {
			case onStack:
				start := slices.Index(stack, specs[j].Name)
				if start < 0 {
					start = 0
				}
				cycle := slices.Clone(stack[start:])
				return append(cycle, specs[j].Name)
			case unvisited:
				if cycle := visit(j); cycle != nil {
					return cycle
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = done
		return nil
	}

	for i := range specs {
		if indegree[i] == 0 || state[i] != unvisited {
			continue
		}
		if cycle := visit(i); cycle != nil {
			return &CycleError{Path: cycle}
		}
	}
	return &CycleError{}
}
