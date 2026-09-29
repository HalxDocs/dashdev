//go:build !windows

package platform

import (
	"os"
)

// Job contains a service's process tree. On Unix this is a no-op: children
// already lead their own process groups (see prepareProcess) and are
// signalled as a unit, so there is nothing to hold.
type Job struct{}

// NewJob returns the Unix containment, which holds nothing.
func NewJob() (Job, error) { return Job{}, nil }

// Assign is a no-op on Unix.
func (Job) Assign(_ *os.Process) error { return nil }

// Close is a no-op on Unix.
func (Job) Close() error { return nil }
