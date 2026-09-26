// Package platformtest provides deterministic test doubles for the platform
// boundary. It exists as a normal package rather than a _test.go file so that
// several packages can share the same fakes.
package platformtest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/HalxDocs/dashdev/internal/platform"
)

// Terminator is a platform.Terminator driven by Trigger instead of the
// operating system.
type Terminator struct {
	triggered chan struct{}
	closed    chan struct{}
	once      sync.Once
}

// NewTerminator returns an untriggered Terminator.
func NewTerminator() *Terminator {
	return &Terminator{triggered: make(chan struct{}), closed: make(chan struct{})}
}

// Trigger simulates the user pressing Ctrl+C.
func (t *Terminator) Trigger() { t.once.Do(func() { close(t.triggered) }) }

// Wait implements platform.Terminator.
func (t *Terminator) Wait(ctx context.Context) error {
	select {
	case <-t.triggered:
		return platform.ErrTerminated
	case <-ctx.Done():
		return ctx.Err()
	case <-t.closed:
		return nil
	}
}

// Close implements platform.Terminator.
func (t *Terminator) Close() { close(t.closed) }

// Clock is a platform.Clock whose time only moves when Advance is called.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
}

type timer struct {
	deadline time.Time
	ch       chan time.Time
	fired    bool
}

// NewClock returns a Clock fixed at a stable, non-zero instant.
func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now implements platform.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After implements platform.Clock. The returned channel fires exactly once,
// when the clock has advanced past the deadline.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &timer{deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.fired = true
		t.ch <- c.now
		return t.ch
	}
	c.timers = append(c.timers, t)
	return t.ch
}

// Advance moves the clock forward and fires every timer that is now due.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	pending := c.timers[:0]
	fired := make([]*timer, 0, len(c.timers))
	for _, t := range c.timers {
		if t.fired {
			continue
		}
		if !t.deadline.After(now) {
			t.fired = true
			fired = append(fired, t)
			continue
		}
		pending = append(pending, t)
	}
	c.timers = pending
	c.mu.Unlock()

	sort.SliceStable(fired, func(i, j int) bool { return fired[i].deadline.Before(fired[j].deadline) })
	for _, t := range fired {
		t.ch <- now
	}
}

// Pending reports how many timers are waiting to fire.
func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.fired {
			n++
		}
	}
	return n
}
