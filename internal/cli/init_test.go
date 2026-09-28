package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HalxDocs/dashdev/internal/config"
)

// writeFile creates a file under dir, making parents as needed.
func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// goProject is a directory with a Go command init can find.
func goProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.26.0\n")
	writeFile(t, dir, "cmd/api/main.go", "package main\n\nfunc main() {}\n")
	return dir
}

func TestInitWritesAConfiguration(t *testing.T) {
	dir := goProject(t)
	code, stdout, _ := run(t, dir, "init")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(stdout, "api") {
		t.Errorf("the report does not name the service:\n%s", stdout)
	}
	path := filepath.Join(dir, config.FileName)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("the written file does not load: %v", err)
	}
	if len(loaded.Specs()) != 1 {
		t.Fatalf("loaded %d services, want 1", len(loaded.Specs()))
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	dir := goProject(t)
	if code, _, _ := run(t, dir, "init"); code != ExitOK {
		t.Fatal("the first init failed")
	}
	code, _, stderr := run(t, dir, "init")
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("the failure does not mention --force:\n%s", stderr)
	}
	code, _, _ = run(t, dir, "init", "--force")
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d for --force", code, ExitOK)
	}
}

func TestInitWithNothingToFindFails(t *testing.T) {
	code, _, stderr := run(t, t.TempDir(), "init")
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "nothing to scaffold") {
		t.Errorf("the failure does not say what was looked for:\n%s", stderr)
	}
}

func TestInitRejectsExtraArguments(t *testing.T) {
	code, _, _ := run(t, t.TempDir(), "init", "extra")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}
