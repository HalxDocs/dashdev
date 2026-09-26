package logs

import "sync"

// Buffer is the bounded, in-memory log buffer for a single service.
//
// It is the one type in the process pipeline that guards its state with a
// mutex, for a reason: reader goroutines append to it while the dashboard reads
// from it. Every other piece of service state is owned by a single goroutine.
// The bound is the point of the type; a noisy service must not be able to grow
// memory without limit.
type Buffer struct {
	mu       sync.Mutex
	capacity int
	lines    []Line
	head     int
	size     int
	lastSeq  uint64
	dropped  uint64
}

// NewBuffer returns a buffer holding at most capacity lines. Capacities below
// one are raised to one, because a buffer that stores nothing would make the
// dashboard useless rather than thrifty.
func NewBuffer(capacity int) *Buffer {
	if capacity < 1 {
		capacity = 1
	}
	return &Buffer{capacity: capacity, lines: make([]Line, 0, capacity)}
}

// Capacity reports the maximum number of lines retained.
func (b *Buffer) Capacity() int { return b.capacity }

// Append stores line, assigning it the next sequence number. When the buffer is
// full the oldest line is evicted to make room. The stored copy is returned so
// that the caller can publish a line that carries its final sequence number.
func (b *Buffer) Append(line Line) Line {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lastSeq++
	line.Seq = b.lastSeq

	if b.size < b.capacity {
		b.lines = append(b.lines, line)
		b.size++
		return line
	}
	b.lines[b.head] = line
	b.head = (b.head + 1) % b.capacity
	b.dropped++
	return line
}

// Snapshot returns the retained lines in chronological order. The result is a
// copy; mutating it does not affect the buffer.
func (b *Buffer) Snapshot() []Line {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]Line, 0, b.size)
	for i := range b.size {
		out = append(out, b.lines[(b.head+i)%len(b.lines)])
	}
	return out
}

// Len reports how many lines are currently retained.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

// Dropped reports how many lines have been evicted because the buffer was full.
func (b *Buffer) Dropped() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// LastSeq reports the sequence number of the most recently stored line, or zero
// if the buffer is empty.
func (b *Buffer) LastSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSeq
}

// Clear empties the buffer. Sequence numbers keep increasing, so a caller that
// tracks LastSeq is not fooled into thinking history was rewound.
func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = b.lines[:0]
	b.head = 0
	b.size = 0
}
