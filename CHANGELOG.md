# Changelog

All notable changes to dashdev are recorded here.

## 0.1.0

The first working release: a process manager you can watch.

### Added

- `dashdev up` reads a `dashdev.yaml` (searched for upwards from the working
  directory, or named with `--config`) and starts every service it declares
  concurrently.
- Explicit command semantics: an argument list is executed verbatim, a single
  string is split with shell quoting, and `shell: true` opts into a real shell.
- Service `directory` values resolve relative to the configuration file, so a
  run is reproducible from anywhere.
- A five-state lifecycle (`starting`, `running`, `exited`, `crashed`,
  `stopped`) with a checked transition table; health and dependency status are
  orthogonal.
- Restart policies (`never`, `on-failure`, `always`) with exponential backoff,
  an optional `max_attempts` limit, and a visible "restart limit reached".
- Dependency scheduling with a topological sort, per-dependency conditions
  (`started`, `healthy`), and a configuration-time cycle check. A dependent is
  released as soon as its dependencies are ready wherever it sits in the file,
  and a service whose dependency dies for good is stopped with the dependency
  named as the reason — while a dependency that is restarting, that exited
  cleanly, or that is merely unhealthy leaves its dependents alone. A service
  stopped that way is restored, in dependency order, when the dependency that
  took it down starts again.
- HTTP, TCP and exec health checks with interval, timeout, retry and start-period
  tuning.
- Bounded per-service output buffers and a lifecycle event stream that never
  drops lifecycle events.
- A Bubble Tea dashboard with service list, detail pane, log viewport, actions
  and a dropped-event indicator; `--headless` prints the same events instead.
- `dashdev version` with `--json` build information.
- Graceful shutdown: one cancellation stops the manager, the services and the
  dashboard together, leaving no orphaned processes.
- Windows process liveness uses `WaitForSingleObject`, so a terminated process
  is never reported as still running.
