# Security Policy

## Supported versions

Weftly is pre-1.0; only the latest minor release line receives security
fixes. Once 1.x ships, this table will call out the exact supported
versions.

| Version | Supported          |
|---------|--------------------|
| 0.x     | :white_check_mark: |

## Reporting a vulnerability

**Please do not open a public GitHub issue for security problems.**

Report privately via GitHub's
[Security Advisories](https://github.com/bkum/weftly/security/advisories/new)
so a fix can land before disclosure. Include:

- a clear description of the issue and its impact,
- a minimal reproduction (a workflow YAML + steps to trigger),
- affected version(s) / commit SHA,
- your suggested severity and any suggested mitigation.

We will acknowledge receipt within **3 business days** and aim to publish
a fix and an advisory within **30 days** of a confirmed report. If the
issue is particularly severe we will coordinate a shorter disclosure
window with you.

## Scope

Weftly is a workflow engine that executes shell commands and HTTP
requests as directed by a workflow author. The trust boundary is
**"who can author or catalogue a workflow"** — the same posture as
`.github/workflows` in a GitHub-hosted CI environment. In-scope
vulnerabilities include:

- Any way to escape the workspace `SafeJoin` guard from a `template`
  `dest:` or `upload` `path:`.
- Any way a secret registered via `secret: true` is written unmasked to
  a log line, `state.json`, `report.html`, or an artifact.
- Any way inline `${{ ... }}` interpolation into a `run:` body under
  `--strict` reaches the shell.
- Any way an action bypasses the `Emit` event boundary and writes
  directly to stdout/stderr (which the masking pipeline never sees).
- Injection or memory-safety issues in the expression engine surface.

Out of scope: workflow authors intentionally shelling out to malicious
commands they wrote themselves — that is the delegated trust boundary,
not a Weftly bug.

## Coordinated disclosure

Fixes ship as a patch release with a GitHub Security Advisory that
credits the reporter (opt-out available on request).
