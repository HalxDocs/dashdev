// Command dashdev runs a set of local services and shows what they are doing.
package main

import (
	"context"
	"os"

	"github.com/HalxDocs/dashdev/internal/cli"
)

func main() {
	workingDir, err := os.Getwd()
	if err != nil {
		// Without a working directory relative configuration paths cannot be
		// resolved, so this is fatal rather than something to paper over.
		workingDir = "."
	}

	code := cli.Run(context.Background(), os.Args[1:], cli.Env{
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		WorkingDir: workingDir,
	})
	os.Exit(code)
}
