# Security

Thank you for taking the security of dashdev seriously. This document describes
how to report a security issue and what to expect.

## Reporting a vulnerability

Please report security issues privately and responsibly.

- Open a private vulnerability report on the GitHub repo, or email
  [kamsyejindu@gmail.com](mailto:kamsyejindu@gmail.com) with a description
  and reproduction steps.
- Include enough detail for the maintainer to reproduce the issue.
- Do **not** open a public issue for an unfixed vulnerability.

## What to expect

- The maintainer will acknowledge the report and work to understand the scope
  and impact.
- If a fix is planned, the maintainer will aim to coordinate a disclosure and
  a release in a reasonable timeframe, and will publish CVE details only after
  a fix is available.
- If the report is not actionable, the maintainer will say so and explain why.

## In scope

- The process manager's handling of configuration, process lifecycle,
  restart, dependencies, health checks, and the event stream.
- The TUI's rendering of untrusted process output.
- Dependency-pinned issues in the Charm stack, yaml.v3, and
  golang.org/x/sys, which should be reported upstream as well.

## Out of scope

- Issues in upstream dependencies that are already reported and fixed upstream.
- Threats that require physical access to the machine or that assume a
  compromised host environment beyond the usual operating assumptions of a
  developer workstation.
