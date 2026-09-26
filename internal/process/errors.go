package process

import (
	"errors"
	"fmt"
)

// Sentinel errors identify failure classes so callers can branch with
// errors.Is instead of matching on message text.
var (
	// ErrNotFound reports a request naming an unknown service.
	ErrNotFound = errors.New("service not found")
	// ErrAlreadyRunning reports a start request for a live service.
	ErrAlreadyRunning = errors.New("service is already running")
	// ErrNotRunning reports a stop or restart request for a dead service.
	ErrNotRunning = errors.New("service is not running")
	// ErrUnsupportedHandle reports a Handle produced by a different runner.
	ErrUnsupportedHandle = errors.New("unsupported process handle")
	// ErrStopped reports a request made after the manager shut down.
	ErrStopped = errors.New("manager has stopped")
	// ErrInvalidOptions reports a manager that was constructed wrongly.
	ErrInvalidOptions = errors.New("invalid manager options")
)

// NotFoundError names the service that could not be found.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("service %q not found", e.Name) }

// Is lets errors.Is(err, ErrNotFound) succeed.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }
