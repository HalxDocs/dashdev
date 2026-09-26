package logs_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform/platformtest"
)

func TestBufferKeepsLinesInOrder(t *testing.T) {
	buffer := logs.NewBuffer(8)
	for i := range 5 {
		buffer.Append(logs.Line{Message: fmt.Sprintf("line %d", i)})
	}

	snapshot := buffer.Snapshot()
	if len(snapshot) != 5 {
		t.Fatalf("buffer holds %d lines, want 5", len(snapshot))
	}
	for i, line := range snapshot {
		if want := fmt.Sprintf("line %d", i); line.Message != want {
			t.Errorf("line %d = %q, want %q", i, line.Message, want)
		}
		if line.Seq != uint64(i+1) {
			t.Errorf("line %d has sequence %d, want %d", i, line.Seq, i+1)
		}
	}
}

func TestBufferAssignsTheSequenceNumberItStores(t *testing.T) {
	buffer := logs.NewBuffer(4)
	stored := buffer.Append(logs.Line{Message: "hello"})
	if stored.Seq != 1 {
		t.Errorf("stored sequence = %d, want 1", stored.Seq)
	}
	if snapshot := buffer.Snapshot(); snapshot[0].Seq != stored.Seq {
		t.Errorf("the buffer kept sequence %d but reported %d", snapshot[0].Seq, stored.Seq)
	}
}

func TestBufferEvictsTheOldestLineWhenFull(t *testing.T) {
	buffer := logs.NewBuffer(3)
	for i := range 10 {
		buffer.Append(logs.Line{Message: fmt.Sprintf("line %d", i)})
	}

	snapshot := buffer.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("buffer holds %d lines, want its capacity of 3", len(snapshot))
	}
	want := []string{"line 7", "line 8", "line 9"}
	for i, line := range snapshot {
		if line.Message != want[i] {
			t.Errorf("line %d = %q, want %q", i, line.Message, want[i])
		}
	}
	if dropped := buffer.Dropped(); dropped != 7 {
		t.Errorf("dropped = %d, want 7", dropped)
	}
	if last := buffer.LastSeq(); last != 10 {
		t.Errorf("last sequence = %d, want 10: sequence numbers keep counting", last)
	}
}

func TestBufferStaysBoundedUnderALotOfPressure(t *testing.T) {
	const capacity = 64
	buffer := logs.NewBuffer(capacity)
	for i := range 100_000 {
		buffer.Append(logs.Line{Message: strings.Repeat("x", i%17)})
	}
	if got := buffer.Len(); got != capacity {
		t.Fatalf("buffer holds %d lines, want %d", got, capacity)
	}
	if got := buffer.Dropped(); got != 100_000-capacity {
		t.Fatalf("dropped = %d, want %d", got, 100_000-capacity)
	}
}

func TestBufferCapacityHasAMinimum(t *testing.T) {
	buffer := logs.NewBuffer(0)
	if buffer.Capacity() < 1 {
		t.Fatalf("capacity = %d, want at least one line", buffer.Capacity())
	}
	buffer.Append(logs.Line{Message: "first"})
	if got := buffer.Len(); got != 1 {
		t.Fatalf("buffer holds %d lines, want 1", got)
	}
}

func TestBufferClearKeepsSequenceNumbersIncreasing(t *testing.T) {
	buffer := logs.NewBuffer(4)
	buffer.Append(logs.Line{Message: "before"})
	buffer.Clear()
	if got := buffer.Len(); got != 0 {
		t.Fatalf("buffer holds %d lines after clearing, want 0", got)
	}
	if last := buffer.LastSeq(); last != 1 {
		t.Errorf("last sequence = %d after clearing, want 1", last)
	}
	if stored := buffer.Append(logs.Line{Message: "after"}); stored.Seq != 2 {
		t.Errorf("sequence after clearing = %d, want 2", stored.Seq)
	}
}

func TestBufferIsSafeToUseFromManyGoroutines(t *testing.T) {
	buffer := logs.NewBuffer(128)
	var writers sync.WaitGroup
	for writer := range 8 {
		writers.Go(func() {
			for i := range 500 {
				buffer.Append(logs.Line{Message: fmt.Sprintf("w%d-%d", writer, i)})
			}
		})
	}
	writers.Wait()
	if got := buffer.Len(); got != 128 {
		t.Fatalf("buffer holds %d lines, want 128", got)
	}
	if last := buffer.LastSeq(); last != 4000 {
		t.Fatalf("last sequence = %d, want 4000", last)
	}
}

func TestWriterSplitsAStreamIntoLines(t *testing.T) {
	clock := platformtest.NewClock()
	var lines []logs.Line
	writer := logs.NewWriter("api", logs.StreamStdout, clock, func(line logs.Line) {
		lines = append(lines, line)
	})

	write := func(text string) {
		t.Helper()
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatalf("Write(%q): %v", text, err)
		}
	}

	write("first\nsecond\n")
	if len(lines) != 2 {
		t.Fatalf("published %d lines after a complete write, want 2", len(lines))
	}

	// A partial line is withheld until it is finished.
	write("third")
	if len(lines) != 2 {
		t.Fatalf("published %d lines, want the partial line withheld", len(lines))
	}
	write(" part\n")
	if len(lines) != 3 || lines[2].Message != "third part" {
		t.Fatalf("lines = %v, want a joined third line", lines)
	}

	// Windows line endings do not leave a carriage return in the message.
	write("fourth\r\n")
	if lines[3].Message != "fourth" {
		t.Errorf("message = %q, want the carriage return removed", lines[3].Message)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := writer.Write([]byte("after close\n")); !errors.Is(err, logs.ErrClosed) {
		t.Errorf("Write after Close = %v, want ErrClosed", err)
	}
}

func TestWriterFlushesAnUnfinishedLineOnClose(t *testing.T) {
	var lines []logs.Line
	writer := logs.NewWriter("api", logs.StreamStderr, platformtest.NewClock(), func(line logs.Line) {
		lines = append(lines, line)
	})
	if _, err := writer.Write([]byte("no newline at the end")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("published %d lines before closing, want 0", len(lines))
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(lines) != 1 || lines[0].Message != "no newline at the end" {
		t.Fatalf("lines after close = %v, want the partial line", lines)
	}
	if lines[0].Stream != logs.StreamStderr {
		t.Errorf("stream = %v, want stderr", lines[0].Stream)
	}
	if err := writer.Close(); err != nil {
		t.Errorf("second Close = %v, want it to be harmless", err)
	}
}

func TestWriterBoundsALineThatNeverEnds(t *testing.T) {
	var lines []logs.Line
	writer := logs.NewWriter("api", logs.StreamStdout, platformtest.NewClock(), func(line logs.Line) {
		lines = append(lines, line)
	})

	// A service that prints a megabyte without a newline must not be able to
	// grow the pipeline without limit.
	chunk := strings.Repeat("x", 4096)
	for range 256 {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if len(lines) != 0 {
		t.Fatalf("published %d lines before the line ended, want 0", len(lines))
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("published %d lines, want exactly one bounded line", len(lines))
	}
	if !lines[0].Truncated {
		t.Error("the line was not marked truncated")
	}
	if got := len(lines[0].Message); got > logs.MaxLineBytes {
		t.Errorf("message is %d bytes, want at most %d", got, logs.MaxLineBytes)
	}
}

func TestWriterReportsEveryByteAsWritten(t *testing.T) {
	writer := logs.NewWriter("api", logs.StreamStdout, platformtest.NewClock(), func(logs.Line) {})
	payload := []byte("a line that is longer than any buffer\n")
	written, err := writer.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if written != len(payload) {
		t.Errorf("Write reported %d bytes, want %d: a short write would look like a broken pipe", written, len(payload))
	}
}

func TestStoreCreatesAndListsBuffers(t *testing.T) {
	store := logs.NewStore(16)
	store.Buffer("web").Append(logs.Line{Message: "web"})
	store.Buffer("api").Append(logs.Line{Message: "api"})
	store.Buffer("web")

	services := store.Services()
	if len(services) != 2 || services[0] != "api" || services[1] != "web" {
		t.Fatalf("services = %v, want api then web", services)
	}
	first, second := store.Buffer("api"), store.Buffer("api")
	if first != second {
		t.Error("asking twice for one service returned two different buffers")
	}
	store.Clear()
	if got := store.Buffer("api").Len(); got != 0 {
		t.Errorf("api holds %d lines after clearing, want 0", got)
	}
}
