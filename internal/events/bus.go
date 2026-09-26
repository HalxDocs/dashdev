package events

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed reports that a subscription has been closed and drained.
var ErrClosed = errors.New("events: subscription is closed")

// DefaultBuffer is how many events a subscription may fall behind by before the
// bus starts discarding.
const DefaultBuffer = 4096

// Bus fans events out to subscribers.
//
// Emit never blocks. A subscriber that cannot keep up loses droppable events
// first and counts every loss, so a slow terminal can degrade a dashboard's log
// view but can never stall a service.
type Bus struct {
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	closed bool
}

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{subs: make(map[*Subscription]struct{})} }

var _ Sink = (*Bus)(nil)

// Subscribe registers a subscriber that may fall behind by at most buffer
// events. Non-positive values use DefaultBuffer.
func (b *Bus) Subscribe(buffer int) *Subscription {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	sub := &Subscription{
		bus:   b,
		limit: buffer,
		wake:  make(chan struct{}, 1),
	}
	b.mu.Lock()
	if b.closed {
		sub.closed = true
	} else {
		b.subs[sub] = struct{}{}
	}
	b.mu.Unlock()
	if sub.closed {
		close(sub.wake)
	}
	return sub
}

// Emit implements Sink. It is safe to call from any goroutine and never blocks.
func (b *Bus) Emit(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for sub := range b.subs {
		sub.deliver(e)
	}
}

// Close detaches every subscriber. Subscribers drain what they already have and
// then read ErrClosed.
func (b *Bus) Close() {
	b.mu.Lock()
	subs := make([]*Subscription, 0, len(b.subs))
	for sub := range b.subs {
		subs = append(subs, sub)
	}
	b.subs = make(map[*Subscription]struct{})
	b.closed = true
	b.mu.Unlock()

	for _, sub := range subs {
		sub.Close()
	}
}

// Subscribers reports how many subscribers are attached.
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

func (b *Bus) detach(sub *Subscription) {
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
}

// Subscription is one subscriber's view of the bus.
type Subscription struct {
	bus   *Bus
	limit int
	wake  chan struct{}

	mu      sync.Mutex
	pending []Event
	dropped uint64
	closed  bool
}

// Pop blocks until at least one event is available and returns everything
// currently queued. The returned slice is owned by the caller; the next call to
// Pop returns a different slice.
func (s *Subscription) Pop(ctx context.Context) ([]Event, error) {
	for {
		s.mu.Lock()
		if len(s.pending) > 0 {
			batch := s.pending
			s.pending = nil
			s.mu.Unlock()
			return batch, nil
		}
		closed := s.closed
		wake := s.wake
		s.mu.Unlock()

		if closed {
			return nil, ErrClosed
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Dropped reports how many events this subscriber missed.
func (s *Subscription) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Close detaches the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	s.mu.Lock()
	alreadyClosed := s.closed
	s.closed = true
	s.mu.Unlock()
	if alreadyClosed {
		return
	}
	if s.bus != nil {
		s.bus.detach(s)
	}
	// Waking a blocked Pop lets it observe the closed flag.
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscription) deliver(e Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	for len(s.pending) >= s.limit {
		s.evictLocked()
	}
	s.pending = append(s.pending, e)
	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// evictLocked makes room by discarding the oldest droppable event, falling back
// to the oldest event of any kind when every queued event is a lifecycle fact.
// Blocking the publisher is not an option, and a stale lifecycle fact is worse
// than a gap in the log, so the fallback is a deliberate last resort.
func (s *Subscription) evictLocked() {
	index := -1
	for i, queued := range s.pending {
		if Droppable(queued) {
			index = i
			break
		}
	}
	if index < 0 {
		index = 0
	}
	copy(s.pending[index:], s.pending[index+1:])
	var zero Event
	s.pending[len(s.pending)-1] = zero
	s.pending = s.pending[:len(s.pending)-1]
	s.dropped++
}
