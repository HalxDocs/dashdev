package platform_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/platform/platformtest"
)

func TestSystemClockReportsRealTime(t *testing.T) {
	clock := platform.System()
	before := time.Now()
	now := clock.Now()
	if now.Before(before) || now.Sub(before) > time.Second {
		t.Errorf("Now() = %v, want a time in the vicinity of %v", now, before)
	}

	timer := clock.After(10 * time.Millisecond)
	select {
	case <-timer:
	case <-time.After(time.Second):
		t.Error("After did not fire")
	}
}

func TestFakeClockOnlyMovesWhenTold(t *testing.T) {
	clock := platformtest.NewClock()
	start := clock.Now()

	timer := clock.After(100 * time.Millisecond)
	select {
	case <-timer:
		t.Fatal("the timer fired without the clock being advanced")
	case <-time.After(20 * time.Millisecond):
	}

	clock.Advance(50 * time.Millisecond)
	select {
	case <-timer:
		t.Fatal("the timer fired before its deadline")
	case <-time.After(20 * time.Millisecond):
	}

	clock.Advance(50 * time.Millisecond)
	select {
	case fired := <-timer:
		if !fired.Equal(start.Add(100 * time.Millisecond)) {
			t.Errorf("timer reported %v, want %v", fired, start.Add(100*time.Millisecond))
		}
	case <-time.After(time.Second):
		t.Fatal("the timer did not fire once its deadline passed")
	}
	if clock.Pending() != 0 {
		t.Errorf("Pending() = %d, want 0", clock.Pending())
	}
}

func TestFakeClockFiresAnAlreadyElapsedTimerImmediately(t *testing.T) {
	clock := platformtest.NewClock()
	select {
	case <-clock.After(0):
	case <-time.After(time.Second):
		t.Fatal("a zero delay did not fire immediately")
	}
}

func TestFakeTerminatorWaitsToBeTriggered(t *testing.T) {
	terminator := platformtest.NewTerminator()
	done := make(chan error, 1)
	go func() { done <- terminator.Wait(context.Background()) }()

	select {
	case err := <-done:
		t.Fatalf("Wait returned early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	terminator.Trigger()
	select {
	case err := <-done:
		if !errors.Is(err, platform.ErrTerminated) {
			t.Errorf("Wait = %v, want ErrTerminated", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after being triggered")
	}
}

func TestFakeTerminatorReportsContextCancellation(t *testing.T) {
	terminator := platformtest.NewTerminator()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := terminator.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait = %v, want context.Canceled", err)
	}
}

func TestTerminatorStopsWaitingWhenClosed(t *testing.T) {
	terminator := platform.NewTerminator()
	done := make(chan error, 1)
	go func() { done <- terminator.Wait(context.Background()) }()
	terminator.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Wait = %v, want nil after the terminator was closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the terminator was closed")
	}
}

// TestAliveDistinguishesRunningFromFinishedProcesses checks the helper the
// integration tests rely on. It re-executes this test binary as a child, which
// avoids depending on any other program being installed.
func TestAliveDistinguishesRunningFromFinishedProcesses(t *testing.T) {
	if os.Getenv(helperEnv) == "1" {
		// Child mode: stay alive until the parent stops the process.
		time.Sleep(time.Minute)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=TestAliveDistinguishesRunningFromFinishedProcesses") //nolint:gosec // the binary is this test
	command.Env = append(os.Environ(), helperEnv+"=1")
	if err := command.Start(); err != nil {
		t.Fatalf("starting the helper: %v", err)
	}
	pid := command.Process.Pid

	if !platform.Alive(pid) {
		t.Errorf("a just-started process reported as gone (pid %d)", pid)
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatalf("killing the helper: %v", err)
	}
	if _, err := command.Process.Wait(); err != nil {
		t.Fatalf("reaping the helper: %v", err)
	}

	if platform.Alive(pid) {
		t.Errorf("a reaped process reported as alive (pid %d)", pid)
	}
	if platform.Alive(0) {
		t.Error("pid zero reported as alive")
	}
}

const helperEnv = "DASHDEV_PLATFORM_HELPER"
