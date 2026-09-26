package logs

import (
	"slices"
	"sync"
)

// Store holds one bounded buffer per service. Buffers are created on first use,
// so the store does not need to know the service list up front.
type Store struct {
	mu       sync.RWMutex
	capacity int
	buffers  map[string]*Buffer
}

// NewStore returns a store whose buffers each hold at most capacity lines.
func NewStore(capacity int) *Store {
	if capacity < 1 {
		capacity = 1
	}
	return &Store{capacity: capacity, buffers: make(map[string]*Buffer)}
}

// Capacity reports the per-service line bound.
func (s *Store) Capacity() int { return s.capacity }

// Buffer returns the buffer for service, creating it if this is the first
// request.
func (s *Store) Buffer(service string) *Buffer {
	s.mu.RLock()
	buffer, ok := s.buffers[service]
	s.mu.RUnlock()
	if ok {
		return buffer
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if buffer, ok := s.buffers[service]; ok {
		return buffer
	}
	buffer = NewBuffer(s.capacity)
	s.buffers[service] = buffer
	return buffer
}

// Services reports the services with a buffer, sorted by name.
func (s *Store) Services() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.buffers))
	for name := range s.buffers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Clear empties every buffer.
func (s *Store) Clear() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, buffer := range s.buffers {
		buffer.Clear()
	}
}
