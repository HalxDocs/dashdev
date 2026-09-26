// Package platform is the single boundary between dashdev and the operating
// system. Everything that differs between Windows and Unix lives here behind
// build tags, so no other package needs to know how a process is signalled, how
// time is read, or how resource usage is sampled.
package platform

import "time"

// Clock abstracts wall-clock time so that backoff and grace periods can be
// driven deterministically in tests.
type Clock interface {
	// Now reports the current time.
	Now() time.Time
	// After returns a channel that receives the current time after d has
	// elapsed, mirroring time.After.
	After(d time.Duration) <-chan time.Time
}

// System returns the clock backed by the operating system.
func System() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
