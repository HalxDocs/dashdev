//go:build !windows

package platform_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/platform"
)

// syncBuffer collects a child's output so that the parent can look for a line
// while the child is still running. A plain bytes.Buffer would be a data race
// between the copying goroutine and the reader.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// waitForLine polls until the collected output contains text.
func waitForLine(output *syncBuffer, text string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), text) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return strings.Contains(output.String(), text)
}

// terminatorChildEnv marks the re-executed copy of this test binary that plays
// the part of dashdev receiving a signal.
const terminatorChildEnv = "DASHDEV_TERMINATOR_CHILD"

// TestTerminatorReportsASignalAsATerminationRequest exercises the real signal
// path end to end, in a child process.
//
// It cannot be done in this process: interrupting ourselves would take the test
// runner down with it, which is the failure it did in fact produce the first
// time. A child also lets the test assert the thing that matters, that Wait
// returns ErrTerminated rather than something the caller has to interpret.
func TestTerminatorReportsASignalAsATerminationRequest(t *testing.T) {
	if os.Getenv(terminatorChildEnv) == "1" {
		terminator := platform.NewTerminator()
		defer terminator.Close()

		ready()
		err := terminator.Wait(context.Background())
		if !errors.Is(err, platform.ErrTerminated) {
			t.Fatalf("Wait = %v, want an error wrapping ErrTerminated", err)
		}
		return
	}

	command := exec.Command(os.Args[0], "-test.run=TestTerminatorReportsASignalAsATerminationRequest", "-test.v")
	command.Env = append(os.Environ(), terminatorChildEnv+"=1")
	output := &syncBuffer{}
	command.Stdout = output
	command.Stderr = output

	if err := command.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}

	// The child prints a line once its handler is installed, so the signal is
	// never sent to a process that is not yet listening.
	if !waitForLine(output, "listening for signals", 10*time.Second) {
		_ = command.Process.Kill()
		t.Fatalf("the child never reported that it was ready:\n%s", output.String())
	}

	if err := interruptChild(command.Process); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("interrupting the child: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the child did not treat the interrupt as a termination request: %v\n%s", err, output.String())
		}
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("the child did not exit after being interrupted:\n%s", output.String())
	}
}

// ready prints a line the parent waits for. Writing it here rather than using
// the test log keeps the parent independent of the child's logging format.
func ready() {
	os.Stdout.WriteString("child: listening for signals\n")
}
