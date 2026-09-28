// Package cli implements the dashdev command line.
//
// The surface is deliberately small: `up` to run the service system, `version`
// to say what is running. Anything larger would be a promise this version does
// not keep.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/HalxDocs/dashdev/internal/buildinfo"
	"github.com/HalxDocs/dashdev/internal/config"
)

// Exit codes. They are part of the command line's contract, so they are named.
const (
	ExitOK = 0
	// ExitFailure reports a problem with the configuration, the environment or
	// a service that could not be started.
	ExitFailure = 1
	// ExitUsage reports a command line dashdev does not understand.
	ExitUsage = 2
)

// usage is the help text, written as one block so it stays readable in source.
const usage = `dashdev — one command to understand and control the local service system

usage:
  dashdev up [flags]        start every service in dashdev.yaml
  dashdev init [flags]      write a starting dashdev.yaml from this directory
  dashdev version [flags]   print the version and build details
  dashdev help              print this message

flags for up:
  --config PATH   configuration file to read (default: search from the working
                  directory upwards for dashdev.yaml)
  --headless      run without the dashboard, printing lifecycle changes instead
  --grace DURATION  how long a service has to stop cleanly (default 5s)

flags for version:
  --json          print machine readable build information

flags for init:
  --config PATH   where to write the configuration (default: dashdev.yaml
                  in the working directory)
  --force         overwrite an existing configuration file
`

// Env carries everything the command line needs from the outside world, so that
// a test can run it without touching the real streams or the real clock.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	// WorkingDir is the directory a relative --config is resolved against and
	// where the default search starts.
	WorkingDir string
}

// Run executes a dashdev command and returns the process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stdout, usage)
		return ExitUsage
	}

	switch args[0] {
	case "up":
		return runUp(ctx, args[1:], env)
	case "init":
		return runInit(args[1:], env)
	case "version":
		return runVersion(args[1:], env)
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "dashdev: unknown command %q\n\n", args[0])
		fmt.Fprint(env.Stderr, usage)
		return ExitUsage
	}
}

func runVersion(args []string, env Env) int {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	asJSON := flags.Bool("json", false, "print machine readable build information")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}

	info := buildinfo.Get()
	if *asJSON {
		encoded, err := info.JSON()
		if err != nil {
			fmt.Fprintf(env.Stderr, "dashdev: could not encode build information: %v\n", err)
			return ExitFailure
		}
		fmt.Fprintln(env.Stdout, string(encoded))
		return ExitOK
	}
	fmt.Fprintln(env.Stdout, info.String())
	return ExitOK
}

// loadConfiguration finds and reads the configuration file.
func loadConfiguration(configPath, workingDir string) (*config.Loaded, error) {
	path := configPath
	if path == "" {
		found, err := config.Find(workingDir)
		if err != nil {
			return nil, err
		}
		path = found
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	return config.Load(path)
}

// reportProblems prints a failure in a shape a person can act on. Joined
// configuration errors are listed one per line, because a service file is
// usually edited in batches.
func reportProblems(writer io.Writer, prefix string, err error) {
	problems := flatten(err)
	if len(problems) == 1 && !errors.Is(err, config.ErrInvalid) {
		fmt.Fprintf(writer, "%s: %v\n", prefix, err)
		return
	}
	fmt.Fprintf(writer, "%s:\n", prefix)
	for _, problem := range problems {
		fmt.Fprintf(writer, "  %s\n", problem)
	}
}

// flatten unwraps a joined error chain into individual messages.
func flatten(err error) []string {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var messages []string
		for _, inner := range joined.Unwrap() {
			messages = append(messages, flatten(inner)...)
		}
		return messages
	}
	return []string{err.Error()}
}
