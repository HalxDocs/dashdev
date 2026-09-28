package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HalxDocs/dashdev/internal/config"
	"github.com/HalxDocs/dashdev/internal/scaffold"
)

// runInit writes a starting dashdev.yaml generated from the directory.
//
// Detection only ever produces services it has actually seen, so the
// written file always loads. Anything merely noticed (a compose file, a
// .env, an ecosystem without a detector yet) is reported on stdout
// instead of guessed into the file.
func runInit(args []string, env Env) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	configPath := flags.String("config", "", "where to write the configuration (default: dashdev.yaml in the working directory)")
	force := flags.Bool("force", false, "overwrite an existing configuration file")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "dashdev: unexpected argument %q\n", flags.Arg(0))
		return ExitUsage
	}

	path := *configPath
	if path == "" {
		path = filepath.Join(env.WorkingDir, config.FileName)
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(env.WorkingDir, path)
	}

	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(env.Stderr, "dashdev: %s already exists (use --force to overwrite it)\n", path)
		return ExitFailure
	}

	report, err := scaffold.Scan(filepath.Dir(path))
	if err != nil {
		reportProblems(env.Stderr, "dashdev: cannot scan the directory", err)
		return ExitFailure
	}
	if len(report.Services) == 0 {
		fmt.Fprintf(env.Stderr, "dashdev: nothing to scaffold in %s\n", filepath.Dir(path))
		fmt.Fprintf(env.Stderr, "dashdev: looked for Go modules (go.mod with ./cmd or a main package), Node packages (package.json with dev/start/serve scripts), and one level of subdirectories\n")
		return ExitFailure
	}

	rendered, err := scaffold.Render(report)
	if err != nil {
		reportProblems(env.Stderr, "dashdev: cannot render the configuration", err)
		return ExitFailure
	}
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		reportProblems(env.Stderr, "dashdev: cannot write the configuration", err)
		return ExitFailure
	}
	if _, err := config.Load(path); err != nil {
		reportProblems(env.Stderr, "dashdev: the generated configuration does not load", err)
		return ExitFailure
	}

	names := make([]string, 0, len(report.Services))
	for _, service := range report.Services {
		names = append(names, service.Name)
	}
	fmt.Fprintf(env.Stdout, "dashdev: wrote %s with %d services (%s)\n", path, len(names), strings.Join(names, ", "))
	fmt.Fprintf(env.Stdout, "dashdev: review it, then run `dashdev up`\n")
	for _, hint := range report.Hints {
		fmt.Fprintf(env.Stdout, "dashdev: note: %s\n", hint)
	}
	return ExitOK
}
