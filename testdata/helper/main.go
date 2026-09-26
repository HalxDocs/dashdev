// Command helper is a service that integration tests can describe exactly.
//
// It lives under testdata so that it is not part of the module's buildable
// packages; the integration test compiles it on demand and points a
// configuration at the result. Everything it does is visible from the outside,
// which is what makes it useful: it prints to both streams, it can fail on
// purpose, and it runs until it is stopped.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	mode := flag.String("mode", "serve", "behaviour: serve, exit, stderr, chatty, silent, crash")
	code := flag.Int("code", 0, "exit code for the exit and crash modes")
	label := flag.String("label", "helper", "name to print so that tests can tell instances apart")
	trigger := flag.String("trigger", "", "crash mode: file that must exist for the process to stay alive")
	flag.Parse()

	switch *mode {
	case "exit":
		fmt.Printf("%s: exiting with %d\n", *label, *code)
		os.Exit(*code)

	case "stderr":
		fmt.Fprintf(os.Stderr, "%s: a message on the error stream\n", *label)
		os.Exit(0)

	case "chatty":
		// Ten thousand lines is enough to prove the log buffer is bounded
		// without making the test slow.
		for i := range 10000 {
			fmt.Printf("%s: line %d\n", *label, i)
		}
		os.Exit(0)

	case "silent":
		// Runs until it is stopped, printing nothing.
		select {}

	case "crash":
		// Serves until the trigger file is removed, then fails with the given
		// code. It is a file rather than a timer so that the test decides when
		// the service dies: a dependency that fails at an unpredictable moment
		// is exactly what a process manager cannot be tested with. Removing
		// and recreating the file fails and revives the same service.
		if *trigger == "" {
			fmt.Fprintln(os.Stderr, "crash mode needs -trigger")
			os.Exit(2)
		}
		fmt.Printf("%s: waiting for its trigger\n", *label)
		for {
			if _, err := os.Stat(*trigger); err != nil {
				fmt.Printf("%s: failing with %d\n", *label, *code)
				os.Exit(*code)
			}
			fmt.Printf("%s: heartbeat\n", *label)
			time.Sleep(25 * time.Millisecond)
		}

	default:
		fmt.Printf("%s: ready\n", *label)
		if err := os.Stdout.Sync(); err != nil {
			// Not fatal: a redirected stream may not support syncing, and the
			// test only needs the line to reach the pipe.
			_ = err
		}
		heartbeat := time.NewTicker(25 * time.Millisecond)
		defer heartbeat.Stop()
		for range heartbeat.C {
			fmt.Printf("%s: heartbeat\n", *label)
		}
	}
}
