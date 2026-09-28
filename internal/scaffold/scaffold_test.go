package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HalxDocs/dashdev/internal/config"
)

// write creates a file under dir, making parents as needed.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func serviceByName(report Report, name string) (Service, bool) {
	for _, service := range report.Services {
		if service.Name == name {
			return service, true
		}
	}
	return Service{}, false
}

// loadRoundTrip renders a report and proves the configuration package
// accepts the result, so the generator cannot drift from the schema.
func loadRoundTrip(t *testing.T, dir string, report Report) {
	t.Helper()
	rendered, err := Render(report)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	path := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(path, rendered, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("the generated file does not load: %v\n%s", err, rendered)
	}
	if len(loaded.Specs()) != len(report.Services) {
		t.Errorf("loaded %d services, want %d", len(loaded.Specs()), len(report.Services))
	}
}

func TestGoCommandsBecomeServices(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/app\n\ngo 1.26.0\n")
	write(t, dir, "cmd/api/main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "cmd/worker/main.go", "package main\n\nfunc main() {}\n")

	report, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Services) != 2 {
		t.Fatalf("found %d services, want 2: %+v", len(report.Services), report.Services)
	}
	api, ok := serviceByName(report, "api")
	if !ok {
		t.Fatalf("no api service: %+v", report.Services)
	}
	joined := strings.Join(api.Command, " ")
	if joined != "go run ./cmd/api" {
		t.Errorf("api command = %q, want %q", joined, "go run ./cmd/api")
	}
	if api.Directory != "" {
		t.Errorf("api directory = %q, want empty (the root)", api.Directory)
	}
	loadRoundTrip(t, dir, report)
}

func TestNestedModuleKeepsItsDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/root\n\ngo 1.26.0\n")
	write(t, dir, "api/go.mod", "module example.com/api\n\ngo 1.26.0\n")
	write(t, dir, "api/cmd/serve/main.go", "package main\n\nfunc main() {}\n")

	report, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	serve, ok := serviceByName(report, "api")
	if !ok {
		t.Fatalf("no api service: %+v", report.Services)
	}
	if serve.Directory != "api" {
		t.Errorf("directory = %q, want api", serve.Directory)
	}
	if strings.Join(serve.Command, " ") != "go run ./cmd/serve" {
		t.Errorf("command = %q", strings.Join(serve.Command, " "))
	}
	loadRoundTrip(t, dir, report)
}

func TestNodeScriptsBecomeServices(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "web/package.json", `{"name": "shop", "scripts": {"dev": "vite", "start": "node server.js"}}`)

	report, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	shop, ok := serviceByName(report, "web")
	if !ok {
		t.Fatalf("no web service: %+v", report.Services)
	}
	if strings.Join(shop.Command, " ") != "npm run dev" {
		t.Errorf("command = %q, want the dev script first", strings.Join(shop.Command, " "))
	}
	if shop.Directory != "web" {
		t.Errorf("directory = %q, want web", shop.Directory)
	}
	loadRoundTrip(t, dir, report)
}

func TestNamesStayUnique(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/root\n\ngo 1.26.0\n")
	write(t, dir, "cmd/api/main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "api/go.mod", "module example.com/api\n\ngo 1.26.0\n")
	write(t, dir, "api/cmd/api/main.go", "package main\n\nfunc main() {}\n")

	report, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, service := range report.Services {
		if seen[service.Name] {
			t.Errorf("duplicate service name %q", service.Name)
		}
		seen[service.Name] = true
	}
	if len(report.Services) != 2 {
		t.Fatalf("found %d services, want 2", len(report.Services))
	}
	loadRoundTrip(t, dir, report)
}

func TestEmptyDirectoryFindsNothing(t *testing.T) {
	report, err := Scan(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Services) != 0 {
		t.Errorf("found %+v, want nothing", report.Services)
	}
}

func TestHintsMentionComposeAndEnv(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  db:\n    image: postgres\n")
	write(t, dir, ".env.example", "PORT=8080\n")

	report, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(report.Hints, "\n")
	if !strings.Contains(joined, "compose") {
		t.Errorf("no compose hint: %q", joined)
	}
	if !strings.Contains(joined, ".env") {
		t.Errorf("no .env hint: %q", joined)
	}
	rendered, err := Render(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "compose") {
		t.Error("the hints did not travel into the rendered file")
	}
}
