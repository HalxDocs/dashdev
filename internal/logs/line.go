// Package logs captures service output into bounded, metadata-rich buffers.
//
// Every line carries the service that produced it, the stream it arrived on,
// when it arrived and a monotonic sequence number, so the dashboard can render
// and filter output without asking the process layer anything.
package logs

import (
	"fmt"
	"time"
)

// Stream identifies which of a process's output streams a line came from.
type Stream uint8

const (
	// StreamStdout is the process's standard output.
	StreamStdout Stream = iota
	// StreamStderr is the process's standard error.
	StreamStderr
)

// String implements fmt.Stringer.
func (s Stream) String() string {
	switch s {
	case StreamStdout:
		return "stdout"
	case StreamStderr:
		return "stderr"
	default:
		return "stream(" + fmt.Sprint(uint8(s)) + ")"
	}
}

// ParseStream is the inverse of String.
func ParseStream(s string) (Stream, error) {
	for _, candidate := range []Stream{StreamStdout, StreamStderr} {
		if candidate.String() == s {
			return candidate, nil
		}
	}
	return 0, fmt.Errorf("logs: unknown stream %q", s)
}

// MaxLineBytes bounds a single line. A service that never emits a newline would
// otherwise be able to grow a buffer entry without limit; anything longer is
// split and marked truncated.
const MaxLineBytes = 8192

// Line is one line of service output with the metadata needed to render it.
type Line struct {
	Service   string
	Stream    Stream
	Message   string
	Time      time.Time
	Seq       uint64
	Truncated bool
}

// String implements fmt.Stringer.
func (l Line) String() string {
	return l.Time.Format(time.TimeOnly) + " " + l.Service + "[" + l.Stream.String() + "] " + l.Message
}
