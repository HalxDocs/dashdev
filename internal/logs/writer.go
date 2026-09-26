package logs

import (
	"bytes"
	"errors"
	"strings"
	"sync"

	"github.com/HalxDocs/dashdev/internal/platform"
)

// ErrClosed reports a write to a writer that has already been closed.
var ErrClosed = errors.New("logs: writer is closed")

// Writer turns a byte stream into log lines and hands each one to a sink. It
// implements io.Writer, so it can be attached directly to a process's output.
//
// A line is bounded by MaxLineBytes: anything beyond that is discarded and the
// line is marked truncated. One output line always produces at most one log
// line, which keeps the dashboard's view of a service's output faithful.
type Writer struct {
	service string
	stream  Stream
	clock   platform.Clock
	sink    func(Line)

	mu         sync.Mutex
	partial    []byte
	truncating bool
	closed     bool
}

// NewWriter returns a writer that reports lines for one service stream. The
// sink is called synchronously from Write and must not block.
func NewWriter(service string, stream Stream, clock platform.Clock, sink func(Line)) *Writer {
	return &Writer{
		service: service,
		stream:  stream,
		clock:   clock,
		sink:    sink,
		partial: make([]byte, 0, 256),
	}
}

// Write implements io.Writer. It always reports the full length of p: output
// that was truncated by the line bound is an intentional outcome, not a write
// error, and a short write would make the process's own I/O look broken.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, ErrClosed
	}

	n := len(p)
	for {
		index := bytes.IndexByte(p, '\n')
		if index < 0 {
			w.appendLocked(p)
			return n, nil
		}
		w.appendLocked(p[:index])
		w.flushLocked()
		p = p[index+1:]
	}
}

// Close flushes any partial line and rejects further writes. It is safe to call
// more than once.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if len(w.partial) > 0 || w.truncating {
		w.flushLocked()
	}
	w.closed = true
	return nil
}

func (w *Writer) appendLocked(p []byte) {
	if len(p) == 0 {
		return
	}
	room := MaxLineBytes - len(w.partial)
	if room > 0 {
		take := min(room, len(p))
		w.partial = append(w.partial, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		w.truncating = true
	}
}

func (w *Writer) flushLocked() {
	message := string(bytes.TrimSuffix(w.partial, []byte("\r")))
	line := Line{
		Service:   w.service,
		Stream:    w.stream,
		Message:   strings.ToValidUTF8(message, "\uFFFD"),
		Time:      w.clock.Now(),
		Truncated: w.truncating,
	}
	w.partial = w.partial[:0]
	w.truncating = false
	w.sink(line)
}
