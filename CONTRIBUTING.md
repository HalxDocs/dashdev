# Contributing

Thanks for taking an interest in dashdev. This is a small project, so the
process is deliberately lightweight: a small, well-scoped pull request is
better than a big one, and the bar for landing a change is that the reviewer
can understand what it does and why.

## Before you start

- If you are fixing a bug, open an issue or comment on the existing one so
  the work is not duplicated and the reviewer understands the intended
  outcome.
- If you are adding a feature, make sure it fits the project's scope. This
  project is a single-service-system process manager with a terminal
  dashboard. Anything larger is a promise this version does not keep.

## Local setup

Requires Go 1.26 or newer.

```sh
go build ./...
go vet ./...
go test ./...
staticcheck ./...
gofmt -l .
```

`staticcheck` and `gofmt` are enforced in CI. `go test ./...` must pass
before a change lands. Race testing needs cgo and a C compiler; the unit
suite passes without it, and CI runs `-race` on Linux.

## Commit style

- Make atomic commits: one logical change per commit, with a message that
  explains *why* as much as *what*.
- Prefer `chore:`, `feat:`, `fix:`, `test:`, `docs:`, or `refactor:`
  prefixes, kept short and factual.
- Keep the author line as yourself; this project does not use a bot
  identity for commits.

## Pull requests

- Keep the diff small and reviewable.
- Include tests for new behaviour, and update goldens when the TUI changes.
- Update the README or the example config when the surface changes.
- The first line of the PR description should explain the user-visible
  outcome, not the implementation.

## Reviews

The reviewer will look for:

- Correctness of the lifecycle and scheduling logic.
- That new code is on the correct side of the dependency boundary.
- That tests actually exercise the behaviour they claim to (especially the
  dependency-scheduling tests, which have caught real bugs).
- Clean static analysis and formatting.

## Reporting a security issue

Do not open a public issue for a security problem. See [SECURITY.md](SECURITY.md).
