package buildinfo

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fieldError is a stand-in used only to prove errors.AsType compiles.
type fieldError struct{ Path string }

func (e *fieldError) Error() string { return e.Path }

// TestToolchainFeatures pins the standard library surface this project depends
// on. It is a compile-time assertion first and a behavioral test second: if the
// toolchain is ever downgraded, this file fails to build rather than failing
// somewhere deep in the process manager.
func TestToolchainFeatures(t *testing.T) {
	t.Run("context cancellation with cause", func(t *testing.T) {
		cause := errors.New("cause")
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		cancel(cause)
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), cause) {
			t.Fatalf("context.Cause = %v, want %v", context.Cause(ctx), cause)
		}
	})

	t.Run("wait group go", func(t *testing.T) {
		var wg sync.WaitGroup
		var ran bool
		wg.Go(func() { ran = true })
		wg.Wait()
		if !ran {
			t.Fatal("sync.WaitGroup.Go did not run the function")
		}
	})

	t.Run("synthetic time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			time.Sleep(time.Hour)
			if got := time.Since(start); got != time.Hour {
				t.Fatalf("synctest elapsed = %v, want 1h", got)
			}
		})
	})

	t.Run("sorted map keys", func(t *testing.T) {
		// maps.Keys returns an iterator that slices.Sorted consumes.
		got := slices.Sorted(maps.Keys(map[string]int{"b": 1, "a": 2}))
		want := []string{"a", "b"}
		if !slices.Equal(got, want) {
			t.Fatalf("sorted keys = %v, want %v", got, want)
		}
	})

	t.Run("typed error extraction", func(t *testing.T) {
		_, ok := errors.AsType[*fieldError](nil)
		_ = ok
	})
}
