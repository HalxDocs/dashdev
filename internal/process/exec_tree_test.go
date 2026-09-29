package process

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/service"
)

// treeCommand returns a command whose child spawns a grandchild that appends
// to file forever: killing only the direct child leaves the grandchild
// writing, which is exactly the orphaned-port problem in miniature.
func treeCommand(t *testing.T, file string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/c", "ping -t 127.0.0.1 > " + file}
	}
	return []string{"sh", "-c", "yes >> " + file}
}

// waitForFileGrows waits until file has content, proving the grandchild runs.
func waitForFileGrows(t *testing.T, file string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(file); err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the grandchild never started writing")
}

// fileFrozen asserts the grandchild stopped writing: size is sampled twice
// with enough gap for several more writes to have landed if it were alive.
func fileFrozen(t *testing.T, file string) {
	t.Helper()
	first, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(3 * time.Second)
	second, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if second.Size() != first.Size() {
		t.Errorf("grandchild still writing: %d -> %d bytes", first.Size(), second.Size())
	}
}

func TestStopKillsTheWholeProcessTree(t *testing.T) {
	runner := NewExecRunner()
	file := filepath.Join(t.TempDir(), "tree.log")
	spec := service.Spec{Name: "tree", Argv: treeCommand(t, file), Dir: t.TempDir()}

	ctx := context.Background()
	handle, err := runner.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFileGrows(t, file)

	// The manager always reaps concurrently with stopping, so the test
	// mirrors it: without a Wait in flight, nothing observes the exit and
	// Signal would wait for an observation that never comes.
	type outcome struct {
		info ExitInfo
		err  error
	}
	reaped := make(chan outcome, 1)
	go func() {
		info, err := runner.Wait(ctx, handle)
		reaped <- outcome{info, err}
	}()

	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := runner.Signal(stopCtx, handle, 5*time.Second); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	select {
	case result := <-reaped:
		if result.err != nil {
			t.Fatalf("Wait: %v", result.err)
		}
	case <-stopCtx.Done():
		t.Fatal("the process was never reaped")
	}
	fileFrozen(t, file)
}
