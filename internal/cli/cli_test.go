package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HalxDocs/dashdev/internal/config"
)

// run is a small helper that runs the command line with captured streams.
func run(t *testing.T, workingDir string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, Env{
		Stdout:     &stdout,
		Stderr:     &stderr,
		WorkingDir: workingDir,
	})
	return code, stdout.String(), stderr.String()
}

// writeConfig writes a dashdev.yaml into a fresh directory and returns the
// directory, so that tests never share configuration state.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	return dir
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	code, stdout, _ := run(t, t.TempDir())
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stdout, "usage:") {
		t.Errorf("the usage was not printed:\n%s", stdout)
	}
}

func TestUnknownCommandIsRejected(t *testing.T) {
	code, _, stderr := run(t, t.TempDir(), "frobnicate")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("the failure does not name the problem:\n%s", stderr)
	}
}

func TestHelpIsNotAFailure(t *testing.T) {
	for _, command := range []string{"help", "-h", "--help"} {
		code, stdout, _ := run(t, t.TempDir(), command)
		if code != ExitOK {
			t.Errorf("%s: exit code = %d, want %d", command, code, ExitOK)
		}
		if !strings.Contains(stdout, "dashdev up") {
			t.Errorf("%s: the usage does not describe up:\n%s", command, stdout)
		}
	}
}

func TestVersionReportsBuildInformation(t *testing.T) {
	code, stdout, _ := run(t, t.TempDir(), "version")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if !strings.HasPrefix(stdout, "dashdev ") {
		t.Errorf("the version line does not start with the program name: %q", stdout)
	}
}

func TestVersionJSONIsMachineReadable(t *testing.T) {
	code, stdout, _ := run(t, t.TempDir(), "version", "--json")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	var decoded struct {
		Version   string `json:"version"`
		GoVersion string `json:"goVersion"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("the version output is not JSON: %v\n%s", err, stdout)
	}
	if decoded.Version == "" || decoded.OS == "" || decoded.Arch == "" {
		t.Errorf("the JSON is missing fields: %+v", decoded)
	}
}

func TestUpWithoutAConfigurationFails(t *testing.T) {
	code, _, stderr := run(t, t.TempDir(), "up", "--headless")
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "configuration") {
		t.Errorf("the failure does not mention the configuration:\n%s", stderr)
	}
}

func TestUpReportsEveryConfigurationProblem(t *testing.T) {
	dir := writeConfig(t, `
version: 1
services:
  - name: broken
    command: ""
`)
	code, _, stderr := run(t, dir, "up", "--headless")
	if code != ExitFailure {
		t.Fatalf("exit code = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "command") {
		t.Errorf("the failure does not describe what is wrong:\n%s", stderr)
	}
}

func TestUpRejectsUnexpectedArguments(t *testing.T) {
	dir := writeConfig(t, `
version: 1
services:
  - name: web
    command: [echo, hi]
`)
	code, _, stderr := run(t, dir, "up", "extra")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "unexpected argument") {
		t.Errorf("the failure does not name the stray argument:\n%s", stderr)
	}
}

func TestRelativeConfigIsResolvedAgainstTheWorkingDirectory(t *testing.T) {
	dir := writeConfig(t, `
version: 1
services:
  - name: web
    command: [echo, hi]
`)
	// An explicitly named, relative configuration is looked for under the
	// working directory rather than the process's own directory.
	loaded, err := loadConfiguration(config.FileName, dir)
	if err != nil {
		t.Fatalf("loadConfiguration: %v", err)
	}
	if loaded.Path != filepath.Join(dir, config.FileName) {
		t.Errorf("loaded %q, want the file under the working directory", loaded.Path)
	}
}

func TestFlattenUnwrapsJoinedErrors(t *testing.T) {
	if got := flatten(nil); got != nil {
		t.Errorf("flatten(nil) = %v, want nil", got)
	}
	if got := flatten(errors.New("one")); len(got) != 1 || got[0] != "one" {
		t.Errorf("flatten of a plain error = %v, want [one]", got)
	}
	joined := errors.Join(config.ErrInvalid, errors.New("second problem"))
	got := flatten(joined)
	if len(got) != 2 {
		t.Fatalf("flatten of a joined error = %v, want two messages", got)
	}
	if !strings.Contains(strings.Join(got, " "), "second problem") {
		t.Errorf("flatten dropped a message: %v", got)
	}
}
