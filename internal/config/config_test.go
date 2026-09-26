package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HalxDocs/dashdev/internal/config"
	"github.com/HalxDocs/dashdev/internal/service"
)

// write lays out a configuration file in a fresh directory and returns its
// path. Relative paths inside the file are resolved against that directory,
// which is what several of these tests are about.
func write(t *testing.T, content string) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, config.FileName)
	if err := os.WriteFile(path, []byte(dedent(content)), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	return path
}

// writeWithDir also creates a subdirectory so that a service can point at one.
func writeWithDir(t *testing.T, content string, directories ...string) string {
	t.Helper()
	path := write(t, content)
	for _, directory := range directories {
		if err := os.MkdirAll(filepath.Join(filepath.Dir(path), directory), 0o750); err != nil {
			t.Fatalf("creating %s: %v", directory, err)
		}
	}
	return path
}

// dedent removes the leading newline and the common indentation of a test
// fixture, so that the YAML in the test reads like YAML.
//
// It strips whichever white space the fixture happens to be indented with,
// spaces or tabs, because the Go source around it uses tabs while YAML's own
// indentation is written with spaces. YAML rejects a tab used for indentation,
// so getting this wrong turns every fixture into a syntax error rather than a
// meaningful test failure.
func dedent(content string) string {
	lines := strings.Split(strings.Trim(content, "\n"), "\n")

	prefix := ""
	first := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		whitespace := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if first {
			prefix, first = whitespace, false
			continue
		}
		prefix = commonPrefix(prefix, whitespace)
	}

	for i, line := range lines {
		// Trailing white space is stripped as well: the last line of a raw
		// string literal is the indentation before the closing backtick, and
		// YAML rejects a line of pure indentation.
		lines[i] = strings.TrimRight(strings.TrimPrefix(line, prefix), " \t")
	}
	return strings.Join(lines, "\n") + "\n"
}

func commonPrefix(first, second string) string {
	limit := min(len(first), len(second))
	index := 0
	for index < limit && first[index] == second[index] {
		index++
	}
	return first[:index]
}

func TestLoadResolvesASimpleConfiguration(t *testing.T) {
	path := write(t, `
		version: 1
		services:
		  - name: api
		    command: npm run dev
		  - name: web
		    command: [go, run, ./cmd/web]
	`)

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	specs := loaded.Specs()
	if len(specs) != 2 {
		t.Fatalf("loaded %d services, want 2", len(specs))
	}
	if got := specs[0].Argv; len(got) != 3 || got[0] != "npm" || got[2] != "dev" {
		t.Errorf("api command = %v, want npm run dev split into arguments", got)
	}
	if got := specs[1].Argv; len(got) != 3 || got[2] != "./cmd/web" {
		t.Errorf("web command = %v, want the list taken verbatim", got)
	}
	// A relative service directory is resolved against the file, not against
	// wherever dashdev happens to be run from.
	if specs[0].Dir != loaded.Dir {
		t.Errorf("api directory = %q, want the configuration's directory %q", specs[0].Dir, loaded.Dir)
	}
	if loaded.Names()[1] != "web" {
		t.Errorf("Names() = %v, want api then web", loaded.Names())
	}
}

func TestLoadResolvesDirectoriesAgainstTheConfiguration(t *testing.T) {
	path := writeWithDir(t, `
		version: 1
		services:
		  - name: api
		    command: go run .
		    directory: backend
		  - name: web
		    command: go run .
		    directory: ../elsewhere
	`, "backend")

	// A sibling of the configuration's directory, which is what the second
	// service points at.
	elsewhere := filepath.Join(filepath.Dir(filepath.Dir(path)), "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o750); err != nil {
		t.Fatalf("creating %s: %v", elsewhere, err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	specs := loaded.Specs()
	wantAPI := filepath.Join(filepath.Dir(path), "backend")
	if specs[0].Dir != wantAPI {
		t.Errorf("api directory = %q, want %q", specs[0].Dir, wantAPI)
	}
	if specs[1].Dir != elsewhere {
		t.Errorf("web directory = %q, want %q", specs[1].Dir, elsewhere)
	}
}

func TestLoadAppliesDefaultsAndServiceOverrides(t *testing.T) {
	path := withDirectory(t, `
		version: 1
		defaults:
		  directory: shared
		  env:
		    - name: LOG_LEVEL
		      value: info
		    - name: REGION
		      value: eu
		  restart:
		    policy: on-failure
		    backoff:
		      base: 100ms
		services:
		  - name: api
		    command: go run .
		    env:
		      - name: LOG_LEVEL
		        value: debug
		  - name: web
		    command: go run .
	`, "shared")

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	specs := loaded.Specs()

	api := specs[0]
	if got := environment(api); got["LOG_LEVEL"] != "debug" {
		t.Errorf("LOG_LEVEL = %q, want the service's override", got["LOG_LEVEL"])
	}
	if got := environment(api); got["REGION"] != "eu" {
		t.Errorf("REGION = %q, want the inherited default", got["REGION"])
	}
	if api.Restart.Policy != service.RestartOnFailure {
		t.Errorf("api restart policy = %v, want the inherited on-failure", api.Restart.Policy)
	}
	if api.Restart.Backoff.Base != 100*time.Millisecond {
		t.Errorf("api backoff base = %v, want the inherited 100ms", api.Restart.Backoff.Base)
	}
	// An inherited backoff is raised without restating the whole policy.
	if specs[1].Restart.Policy != service.RestartOnFailure {
		t.Errorf("web restart policy = %v, want the inherited on-failure", specs[1].Restart.Policy)
	}
	if !strings.HasSuffix(api.Dir, "shared") {
		t.Errorf("api directory = %q, want it to inherit shared", api.Dir)
	}
}

func TestLoadHonoursShellAndDirectorySettings(t *testing.T) {
	path := write(t, `
		version: 1
		services:
		  - name: shellish
		    command: echo one && echo two
		    shell: true
		  - name: direct
		    command: echo one && echo two
	`)

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	specs := loaded.Specs()

	// With a shell, the command reaches it whole, so the shell is what decides
	// how to read it.
	if got := specs[0].Argv; len(got) != 3 || got[len(got)-1] != "echo one && echo two" {
		t.Errorf("shell command = %v, want the whole string handed to a shell", got)
	}
	// Without one, the line is only tokenised: the ampersands are ordinary
	// arguments, which is the difference the setting exists to make explicit.
	direct := specs[1].Argv
	want := []string{"echo", "one", "&&", "echo", "two"}
	if len(direct) != len(want) {
		t.Fatalf("direct command = %v, want %v", direct, want)
	}
	for i := range want {
		if direct[i] != want[i] {
			t.Errorf("direct command = %v, want %v", direct, want)
			break
		}
	}
}

func TestLoadReadsDependenciesInBothForms(t *testing.T) {
	path := write(t, `
		version: 1
		services:
		  - name: db
		    command: postgres
		  - name: cache
		    command: redis-server
		  - name: api
		    command: go run .
		    depends_on:
		      - db
		  - name: web
		    command: go run .
		    depends_on:
		      cache: healthy
		      db: started
	`)

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	specs := loaded.Specs()

	api := specs[2]
	if len(api.DependsOn) != 1 || api.DependsOn[0].Name != "db" {
		t.Fatalf("api dependencies = %v, want a list form dependency on db", api.DependsOn)
	}
	if api.DependsOn[0].Condition != service.ReadyWhenStarted {
		t.Errorf("a bare dependency = %v, want the started condition", api.DependsOn[0].Condition)
	}

	web := specs[3]
	if len(web.DependsOn) != 2 {
		t.Fatalf("web dependencies = %v, want two", web.DependsOn)
	}
	// Document order is kept, so the plan is reproducible.
	if web.DependsOn[0].Name != "cache" || web.DependsOn[0].Condition != service.ReadyWhenHealthy {
		t.Errorf("first dependency = %+v, want cache then healthy", web.DependsOn[0])
	}
	if web.DependsOn[1].Name != "db" || web.DependsOn[1].Condition != service.ReadyWhenStarted {
		t.Errorf("second dependency = %+v, want db then started", web.DependsOn[1])
	}
}

func TestLoadReadsHealthChecksAndRestartPolicy(t *testing.T) {
	path := write(t, `
		version: 1
		services:
		  - name: api
		    command: go run .
		    monitor: true
		    health:
		      type: http
		      target: http://127.0.0.1:8080/health
		      interval: 2s
		      timeout: 500ms
		      retries: 5
		      start_period: 1s
		    restart:
		      policy: always
		      max_attempts: 4
		      backoff:
		        base: 1s
		        max: 30s
		        factor: 3
	`)

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := loaded.Specs()[0]

	if !spec.Monitor {
		t.Error("monitor was not read")
	}
	if spec.Health == nil {
		t.Fatal("health check was not read")
	}
	if spec.Health.Kind != service.ProbeHTTP || spec.Health.Target != "http://127.0.0.1:8080/health" {
		t.Errorf("health = %+v, want an http probe of the given target", *spec.Health)
	}
	if spec.Health.Interval != 2*time.Second || spec.Health.Timeout != 500*time.Millisecond {
		t.Errorf("health timing = %v/%v, want 2s/500ms", spec.Health.Interval, spec.Health.Timeout)
	}
	if spec.Health.Retries != 5 || spec.Health.StartPeriod != time.Second {
		t.Errorf("health retries/start period = %d/%v, want 5/1s", spec.Health.Retries, spec.Health.StartPeriod)
	}
	if spec.Restart.Policy != service.RestartAlways || spec.Restart.MaxAttempts != 4 {
		t.Errorf("restart = %+v, want always with four attempts", spec.Restart)
	}
	if spec.Restart.Backoff.Factor != 3 {
		t.Errorf("backoff factor = %v, want 3", spec.Restart.Backoff.Factor)
	}
	// The delays follow from the curve, which is what the manager uses.
	if got := spec.Restart.Delay(1); got != time.Second {
		t.Errorf("first delay = %v, want 1s", got)
	}
	if got := spec.Restart.Delay(4); got != 27*time.Second {
		t.Errorf("fourth delay = %v, want 27s", got)
	}
	if got := spec.Restart.Delay(9); got != 30*time.Second {
		t.Errorf("a late delay = %v, want the 30s ceiling", got)
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	path := write(t, `
		version: 7
		services:
		  - name: api
		    command: go run .
		    directory: missing
		    depends_on:
		      - nowhere
		  - name: api
		    command: ""
		  - name: bad name!
		    command: go run .
	`)

	_, err := config.Load(path)
	if !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("Load error = %v, want ErrInvalid", err)
	}

	message := err.Error()
	for _, want := range []string{
		"version",
		"services[1].name",
		"services[2].name",
		"directory",
		"does not exist",
		"depends_on[0]",
		"does not exist",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error does not mention %q:\n%s", want, message)
		}
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := write(t, `
		version: 1
		services:
		  - name: api
		    commandd: go run .
	`)

	if _, err := config.Load(path); err == nil {
		t.Fatal("an unknown field was accepted, which would silently start nothing")
	} else if !strings.Contains(err.Error(), "field") {
		t.Errorf("error = %v, want it to name the unknown field", err)
	}
}

func TestLoadRejectsUnusableServiceDefinitions(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "no services",
			content: `
				version: 1
				services: []
			`,
			want: "at least one service",
		},
		{
			name: "empty command",
			content: `
				version: 1
				services:
				  - name: api
			`,
			want: "command must not be empty",
		},
		{
			name: "shell with an argument list",
			content: `
				version: 1
				services:
				  - name: api
				    command: [go, run, .]
				    shell: true
			`,
			want: "single command string",
		},
		{
			name: "duplicate environment variable",
			content: `
				version: 1
				services:
				  - name: api
				    command: go run .
				    env:
				      - name: A
				        value: "1"
				      - name: A
				        value: "2"
			`,
			want: "already set",
		},
		{
			name: "dependency on itself",
			content: `
				version: 1
				services:
				  - name: api
				    command: go run .
				    depends_on:
				      - api
			`,
			want: "cannot depend on itself",
		},
		{
			name: "dependency cycle",
			content: `
				version: 1
				services:
				  - name: a
				    command: a
				    depends_on: [b]
				  - name: b
				    command: b
				    depends_on: [a]
			`,
			want: "cycle",
		},
		{
			name: "unknown condition",
			content: `
				version: 1
				services:
				  - name: db
				    command: db
				  - name: api
				    command: api
				    depends_on:
				      db: whenever
			`,
			want: "unknown condition",
		},
		{
			name: "health check without a type",
			content: `
				version: 1
				services:
				  - name: api
				    command: api
				    health:
				      target: http://localhost:1
			`,
			want: "type is required",
		},
		{
			name: "negative backoff",
			content: `
				version: 1
				services:
				  - name: api
				    command: api
				    restart:
				      policy: always
				      max_attempts: -1
			`,
			want: "must not be negative",
		},
		{
			name: "unterminated quote",
			content: `
				version: 1
				services:
				  - name: api
				    command: "go run 'unterminated"
			`,
			want: "unterminated quote",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := write(t, test.content)
			_, err := config.Load(path)
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %v, want it to mention %q", err, test.want)
			}
			if !errors.Is(err, config.ErrInvalid) {
				t.Errorf("error %v is not an ErrInvalid", err)
			}
		})
	}
}

func TestLoadRejectsAnEmptyFile(t *testing.T) {
	path := write(t, "")
	if _, err := config.Load(path); err == nil {
		t.Fatal("an empty file was accepted")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %v, want it to say the file is empty", err)
	}
}

func TestLoadRejectsASecondDocument(t *testing.T) {
	path := write(t, `
		version: 1
		services:
		  - name: api
		    command: go run .
		---
		version: 1
	`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("a second document was accepted")
	} else if !strings.Contains(err.Error(), "single YAML document") {
		t.Errorf("error = %v, want it to mention the document count", err)
	}
}

func TestLoadReportsAMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := config.Load(missing); err == nil {
		t.Fatal("a missing file was accepted")
	} else if !errors.Is(err, config.ErrInvalid) {
		t.Errorf("error = %v, want an ErrInvalid", err)
	}
}

func TestFindSearchesUpwards(t *testing.T) {
	root := withDirectory(t, `
		version: 1
		services:
		  - name: api
		    command: go run .
	`, filepath.Join("nested", "deeper"))
	deep := filepath.Join(filepath.Dir(root), "nested", "deeper")

	found, err := config.Find(deep)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found != root {
		t.Errorf("Find = %q, want %q", found, root)
	}

	if _, err := config.Find(t.TempDir()); !errors.Is(err, config.ErrNotFound) {
		t.Errorf("Find in an empty tree = %v, want ErrNotFound", err)
	}
}

func TestSpecsKeepDeclarationOrderRegardlessOfDependencies(t *testing.T) {
	path := write(t, `
		version: 1
		services:
		  - name: web
		    command: web
		    depends_on: [api]
		  - name: api
		    command: api
	`)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names := loaded.Names()
	if len(names) != 2 || names[0] != "web" || names[1] != "api" {
		t.Errorf("names = %v, want declaration order preserved for the manager to sort out", names)
	}
}

// withDirectory writes a configuration and creates one directory beside it.
func withDirectory(t *testing.T, content, directory string) string {
	t.Helper()
	return writeWithDir(t, content, directory)
}

func environment(spec service.Spec) map[string]string {
	values := make(map[string]string, len(spec.Env))
	for _, env := range spec.Env {
		values[env.Name] = env.Value
	}
	return values
}
