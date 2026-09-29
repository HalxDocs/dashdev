# dashdev

One command to understand and control the local service system.

`dashdev` reads a single `dashdev.yaml`, starts every service it declares at the
same time, keeps track of what each one is doing, captures its output, and shows
the whole thing in a terminal dashboard. Stop `dashdev` and every service it
started stops too — no orphans, no half-dead processes.

The process manager is the product; the dashboard is just one way to watch it.

![dashdev dashboard — a service list with keyway and demo-sso running and beta
exited; the selected service's health, uptime and log output, and the footer
of bound keys are all visible.](docs/dashboard.png)

## Install

Grab a ready binary from the [releases page](https://github.com/HalxDocs/dashdev/releases),
or in one command:

Windows (PowerShell — downloads the latest release, no admin needed):

```powershell
$tag = (Invoke-RestMethod https://api.github.com/repos/HalxDocs/dashdev/releases/latest).tag_name
$ver = $tag.TrimStart('v')
$zip = "$env:TEMP\dashdev.zip"
Invoke-WebRequest "https://github.com/HalxDocs/dashdev/releases/download/$tag/dashdev_${ver}_windows_amd64.zip" -OutFile $zip
New-Item -ItemType Directory -Path "$env:LOCALAPPDATA\dashdev" -Force | Out-Null
Expand-Archive $zip -DestinationPath "$env:LOCALAPPDATA\dashdev" -Force
& "$env:LOCALAPPDATA\dashdev\dashdev.exe" version
```

Then either add `%LOCALAPPDATA%\dashdev` to your PATH or call the binary by
its full path. In Git Bash the same binary runs as `dashdev.exe up`.

macOS / Linux (drops the binary into `~/.local/bin`, already on PATH on
most systems — pick the `darwin` or `linux` archive and `amd64` or `arm64`
to match your machine):

```sh
tag=$(curl -fsSL https://api.github.com/repos/HalxDocs/dashdev/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
ver=${tag#v}
mkdir -p ~/.local/bin
curl -fsSL "https://github.com/HalxDocs/dashdev/releases/download/$tag/dashdev_${ver}_linux_amd64.tar.gz" | tar xz -C ~/.local/bin dashdev
dashdev version
```

With Go installed, instead:

```sh
go install github.com/HalxDocs/dashdev/cmd/dashdev@latest
```

Android (via [Termux](https://termux.dev) — `dashdev` ships an `android/arm64`
binary since v0.1.2; the dashboard is cramped on small screens, so `--headless`
is recommended, and grab a Termux wakelock so Android doesn't kill long runs):

```sh
pkg install curl tar
curl -fsSL https://github.com/HalxDocs/dashdev/releases/download/v0.1.2/dashdev_0.1.2_android_arm64.tar.gz | tar xz
./dashdev version
```

## Build and run

Requires Go 1.26 or newer. The module pins to Go 1.26 and the toolchain is set to
Go 1.26.6, so a recent Go is enough to build and run without an extra toolchain
install.

```sh
go build -o bin/dashdev ./cmd/dashdev
bin/dashdev up
```

`dashdev` looks for `dashdev.yaml` in the current directory and then in each
parent directory, so a single file at the root of a monorepo works from anywhere
inside it. Use `--config PATH` to point at a specific file.

The example is shipped as [`dashdev.example.yaml`](dashdev.example.yaml) — copy
it to `dashdev.yaml` and edit it. A minimal run for screenshots and a quick smoke
test is bundled as [`docs/demo.yaml`](docs/demo.yaml), which defines two services:

- `alpha` serves until stopped (a long-running service, via `go run`
  `./testdata/helper`), and
- `beta` exits cleanly at once (a one-shot service).

```sh
cp docs/demo.yaml dashdev.yaml
bin/dashdev up
```

## Command line

```sh
dashdev up [--config PATH] [--headless] [--grace DURATION]
dashdev init [--config PATH] [--force]
dashdev version [--json]
dashdev help
```

- `--headless` runs without the dashboard and prints lifecycle changes and log
  lines instead. This is what a script or an integration test wants.
- `--grace` is how long a service has to stop cleanly before it is killed
  (default `5s`).
- `init` writes a starting `dashdev.yaml` from the current directory: Go
  commands and Node `dev`/`start` scripts become services, one level of
  subdirectories included. Anything it cannot describe is printed as a hint
  instead of guessed. Refuses to overwrite without `--force`.

Exit codes are part of the contract: `0` success, `1` a configuration or runtime
failure, `2` a command line `dashdev` does not understand.

## The dashboard

| Key | Action |
| --- | --- |
| `↑` / `k`, `↓` / `j` | move the selection |
| `pgup` / `ctrl+u`, `pgdown` / `ctrl+d` | scroll the log pane |
| `home`, `end` | jump to the start or end of the output |
| `r` | restart the selected service |
| `s` | stop the selected service |
| `enter` | start the selected service |
| `q` / `ctrl+c` | quit, stopping every service |

## Configuration

A `dashdev.yaml` declares a `version`, optional `defaults`, and a list of
`services`. Unknown fields are rejected rather than ignored, so a typo such as
`commandd:` fails loudly instead of silently starting nothing.

```yaml
version: 1

defaults:
  directory: .          # base directory for every service unless overridden
  env:
    - name: LOG_LEVEL
      value: info

services:
  - name: db
    command: [postgres, -D, ./data]
    directory: ./db
    health:
      type: tcp
      target: 127.0.0.1:5432
      interval: 2s

  - name: api
    command: [go, run, ./cmd/api]
    directory: ./api
    shell: false
    depends_on:
      db: healthy
    env:
      - name: DATABASE_URL
        value: postgres://localhost:5432/app
    restart:
      policy: on-failure
      max_attempts: 5
      backoff:
        base: 500ms
        max: 30s
        factor: 2
    health:
      type: http
      target: http://127.0.0.1:8080/healthz
```

### Command semantics

`command` is either a **list of arguments** or a **single string**, and the two
are not the same thing:

- A list is executed verbatim with `execve` — there is no shell, no quoting, and
  no globbing. `command: [echo, "hello world"]` passes one argument containing a
  space. This is the default and it is what you want almost always.
- A single string is split with shell-like quoting rules into an argument list,
  but it is still executed directly, not through a shell.
- Set `shell: true` (with a single-string command) to run through
  `sh -c` on Unix and `cmd /c` on Windows. Use it only when you actually need
  pipes, redirection or shell builtins; it changes how signals are delivered.

### Directories

`directory` — on a service or in `defaults` — is resolved relative to the
**directory containing `dashdev.yaml`**, never against the directory you
happened to invoke `dashdev` from. `dashdev up` therefore does the same thing
from anywhere. `dashdev` checks that each directory exists before starting
anything.

### Lifecycle

A service is always in exactly one of five states. Health and dependency status
are separate fields, so a service is never both "running" and "crashed".

| State | Meaning |
| --- | --- |
| `starting` | a start was requested, or the service is waiting for dependencies |
| `running` | the process is alive and, if configured, healthy |
| `exited` | the process finished on its own with exit code 0 |
| `crashed` | a non-zero exit, a signal, or a failure to start |
| `stopped` | `dashdev` stopped it on purpose |

### Restart policy

| `policy` | Behaviour |
| --- | --- |
| `never` (default) | start once |
| `on-failure` | restart unless the service exited cleanly or was stopped on purpose |
| `always` | restart whatever ended it |

`max_attempts` (0 = unlimited) caps the retries and the dashboard then shows
`restart limit reached`. `backoff` uses exponential delay with `base`, `max` and
`factor` (defaults `250ms`, `30s`, `2`).

### Dependencies

`depends_on` accepts a list of service names, or a mapping from a name to the
condition that must hold:

```yaml
depends_on: [db, cache]        # only requires them to be running
depends_on:
  db: healthy                  # requires the health check to pass
```

Services are ordered with a topological sort, so independent services start
together and `dashdev` refuses to start a configuration that contains a cycle —
reported as a configuration error before anything runs. A dependent is released
as soon as its dependencies are ready, whatever order the file lists them in.

A dependency is a claim about a *running* service, not only about the order it
starts in. When a dependency ends for good — a crash that no restart policy is
bringing back — the services running against it are stopped too, and the reason
says which one:

```
api   stopped    dependency "db" is crashed
web   stopped    dependency "api" is stopped
```

Three things deliberately do *not* take a dependent down: a dependency that is
restarting (stopping its dependents would turn a recovery into an outage), a
dependency that exited cleanly (the pattern of running a migration once and then
serving), and a dependency that is merely unhealthy (health is not death — an
unhealthy dependency makes dependents wait, but it does not stop one that is
already running).

Being stopped this way is a bench, not a failure. A service that was stood down
was never broken, so when the dependency starts again — by hand from the
dashboard, or because its own dependency came back — its dependents come back
with it, in the same order they started the first time. A service whose *other*
dependency is still down stays stopped rather than being started against it or
reported as a crash.

### Health checks

| `type` | `target` |
| --- | --- |
| `http` | a URL; a 2xx response is healthy |
| `tcp` | a `host:port` address |
| `exec` | a command that must exit 0 |

`interval` (default `10s`), `timeout` (default `2s`), `retries` (default `3`) and
`start_period` control the timing.

## Development

### Dependencies and releases

The dependency graph flows one way: the CLI is thin, the config layer resolves the
YAML into service definitions, the domain model is a leaf package, and the process
manager owns the lifecycle. The operating-system boundary is isolated so the rest
of the tree can be tested without real processes.

Releases are built with [goreleaser](https://github.com/goreleaser/goreleaser).
Run `goreleaser check` to validate the config (`.goreleaser.yml`) and
`goreleaser build --snapshot` to produce snapshot binaries locally. Tagged releases
are cut from `master` and publish drafts on GitHub; the changelog is filtered to
exclude pure `docs:`, `test:` and `chore:` commits so a release notes stays about
user-visible changes.

Dependencies are restored the ordinary Go way:

```sh
go mod download
go mod tidy
```

CI runs `go build ./...`, `go vet ./...`, `gofmt -l .`, `staticcheck` and
`govulncheck` on Linux, plus `go test -race` on Linux and plain `go test` on
Windows and macOS.

### Ordinary run

```sh
go build ./...
go vet ./...
go test ./...
staticcheck ./...
gofmt -l .
```

Race testing needs cgo and a C compiler, which is not always available on
Windows. The unit suite passes without it; CI runs `-race` on Linux.

### Layout

The dependencies point one way, and the process manager is the centre:

```
cmd/dashdev        the binary
internal/cli       argument parsing and the run loop
internal/config    dashdev.yaml -> resolved service definitions
internal/service   the domain model: states, specs, plans (leaf package)
internal/process   the manager: one owner goroutine, runners, probes, scheduling
internal/platform  the operating system boundary: signals, stats, shells
internal/logs      bounded per-service output buffers
internal/events    the lifecycle event stream
internal/tui       the dashboard
internal/buildinfo version and provenance
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to build, test, and raise changes.
The repo is Apache-2.0; see [LICENSE](LICENSE). Security issues are handled
privately — see [SECURITY.md](SECURITY.md).

## License

Apache-2.0, see [LICENSE](LICENSE).
