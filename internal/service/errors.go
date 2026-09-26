package service

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors identify failure classes so callers can branch with
// errors.Is instead of matching on message text.
var (
	// ErrNoServices reports that nothing was configured to run.
	ErrNoServices = errors.New("no services defined")
	// ErrInvalidSpec reports a service definition that cannot be run.
	ErrInvalidSpec = errors.New("invalid service definition")
	// ErrDuplicateName reports two services sharing a name.
	ErrDuplicateName = errors.New("duplicate service name")
	// ErrCycle reports a dependency graph that cannot be ordered.
	ErrCycle = errors.New("dependency cycle")
	// ErrUnknownDependency reports a dependency on a service that does not exist.
	ErrUnknownDependency = errors.New("unknown dependency")
)

// DuplicateNameError names the service defined twice.
type DuplicateNameError struct {
	Name string
}

func (e *DuplicateNameError) Error() string {
	return fmt.Sprintf("service %q defined more than once", e.Name)
}

// Is lets errors.Is(err, ErrDuplicateName) succeed.
func (e *DuplicateNameError) Is(target error) bool { return target == ErrDuplicateName }

// CycleError reports the services that form a dependency cycle, in the order
// they were visited.
type CycleError struct {
	Path []string
}

func (e *CycleError) Error() string {
	if len(e.Path) == 0 {
		return "dependency cycle"
	}
	return fmt.Sprintf("dependency cycle: %s", strings.Join(e.Path, " -> "))
}

// Is lets errors.Is(err, ErrCycle) succeed.
func (e *CycleError) Is(target error) bool { return target == ErrCycle }

// UnknownDependencyError names the missing service and the service that wanted
// it.
type UnknownDependencyError struct {
	Service    string
	Dependency string
}

func (e *UnknownDependencyError) Error() string {
	return fmt.Sprintf("service %q depends on unknown service %q", e.Service, e.Dependency)
}

// Is lets errors.Is(err, ErrUnknownDependency) succeed.
func (e *UnknownDependencyError) Is(target error) bool { return target == ErrUnknownDependency }
