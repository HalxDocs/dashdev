package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/HalxDocs/dashdev/internal/events"
	"github.com/HalxDocs/dashdev/internal/logs"
	"github.com/HalxDocs/dashdev/internal/platform"
	"github.com/HalxDocs/dashdev/internal/process"
	"github.com/HalxDocs/dashdev/internal/tui"
)

// runUp starts the service system and blocks until it stops.
//
// The shape of this function is the shape of the whole design: the manager runs
// on its own goroutine, the dashboard reads a stream the manager publishes, and
// stopping is one cancellation that everything else observes.
func runUp(ctx context.Context, args []string, env Env) int {
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	configPath := flags.String("config", "", "configuration file to read")
	headless := flags.Bool("headless", false, "run without the dashboard")
	grace := flags.Duration("grace", process.DefaultGracePeriod, "how long a service has to stop cleanly")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "dashdev: unexpected argument %q\n", flags.Arg(0))
		return ExitUsage
	}

	loaded, err := loadConfiguration(*configPath, env.WorkingDir)
	if err != nil {
		reportProblems(env.Stderr, "dashdev: cannot read the configuration", err)
		return ExitFailure
	}

	bus := events.NewBus()
	store := logs.NewStore(process.DefaultLogCapacity)
	clock := platform.System()

	manager, err := process.NewManager(loaded.Specs(), process.Options{
		Runner:      process.NewExecRunner(),
		Sink:        bus,
		Store:       store,
		Clock:       clock,
		Prober:      process.NewProber(),
		Sampler:     platform.NewSampler(),
		GracePeriod: *grace,
	})
	if err != nil {
		reportProblems(env.Stderr, "dashdev: cannot run these services", err)
		return ExitFailure
	}

	// The run context is separate from the caller's so that a signal and a
	// dashboard quit both arrive at the same place: one cancellation, observed
	// by the manager and by the event stream.
	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancel(nil)

	terminator := platform.NewTerminator()
	defer terminator.Close()
	go func() {
		if cause := terminator.Wait(runCtx); cause != nil {
			cancel(cause)
		}
	}()

	stopped := make(chan error, 1)
	go func() { stopped <- manager.Run(runCtx) }()

	fmt.Fprintf(env.Stderr, "dashdev: %d services from %s\n", len(loaded.Specs()), loaded.Path)

	runErr := runFrontend(runCtx, frontendOptions{
		headless: *headless,
		bus:      bus,
		store:    store,
		manager:  manager,
		clock:    clock,
		env:      env,
	})

	// Whatever ended the frontend, the services are stopped by cancelling the
	// run context and waiting for the manager to reap every one of them.
	cancel(runErr)
	if err := <-stopped; err != nil {
		fmt.Fprintf(env.Stderr, "dashdev: %v\n", err)
		return ExitFailure
	}
	return ExitOK
}

// frontendOptions is the shared plumbing of the two ways to watch a run.
type frontendOptions struct {
	headless bool
	bus      *events.Bus
	store    *logs.Store
	manager  *process.Manager
	clock    platform.Clock
	env      Env
}

// runFrontend shows the dashboard, or prints lifecycle changes when the
// dashboard would get in the way of a script.
func runFrontend(ctx context.Context, options frontendOptions) error {
	if options.headless {
		return runHeadless(ctx, options)
	}
	return tui.Run(ctx, tui.Options{
		Subscription: options.bus.Subscribe(0),
		Store:        options.store,
		Controller:   options.manager,
		Clock:        options.clock,
		LogCapacity:  process.DefaultLogCapacity,
		// Colour is left to the terminal's own profile detection; a terminal
		// that cannot show it renders the plain layout.
		Coloured: true,
	})
}

// runHeadless reports lifecycle changes and log lines without taking over the
// terminal, which is what a script or an integration test wants. It runs until
// the context is done.
func runHeadless(ctx context.Context, options frontendOptions) error {
	subscription := options.bus.Subscribe(0)
	defer subscription.Close()

	for {
		batch, err := subscription.Pop(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, events.ErrClosed) {
				return nil
			}
			return err
		}
		for _, event := range batch {
			switch event := event.(type) {
			case events.ServiceAdded:
				fmt.Fprintf(options.env.Stderr, "%-16s %s\n", event.Status.Name, event.Status.State)
			case events.StateChanged:
				fmt.Fprintf(options.env.Stderr, "%-16s %s -> %s (%s)\n",
					event.Status.Name, event.From, event.To, event.Reason)
			case events.StatusUpdated:
				fmt.Fprintf(options.env.Stderr, "%-16s %s: %s\n",
					event.Status.Name, event.Status.State, event.Reason)
			case events.LogEmitted:
				fmt.Fprintf(options.env.Stdout, "%s %s [%s] %s\n",
					event.Line.Time.Format(time.TimeOnly), event.Line.Service,
					event.Line.Stream, event.Line.Message)
			}
		}
	}
}
