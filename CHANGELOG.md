# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added — Phase 2

- **`prompt` action** replaces the Phase 1 stub. Supports `text`,
  `password`, `confirm`, and `select` types; TTY detection via
  `x/term`; passwords read without echo; `--yes` / `-y` auto-answers
  every `type: confirm` prompt for CI-style unattended runs;
  non-interactive sessions use `default:` or fail fast.
- **DAG scheduler with `needs` and bounded parallelism.** Steps
  without `needs:` still chain to the previous named step (GHA-style)
  so existing workflows keep their order. Declaring `needs:` opts
  into parallelism. `--parallel N` (default 4) caps concurrency.
  Cascade-skip propagates through a fatal step's dependents.
- **`--resume <run-id>`.** Reloads state.json, skips previously-
  successful steps, and re-emits their events (marked
  `resumed: true`) so the renderer + report stay coherent. Downstream
  steps see the cached outputs exactly as they would on a fresh run.
- **Server mode.** `weftly server` starts a REST + SSE + embedded-SPA
  front-end backed by a curated catalogue directory. Endpoints:
  `GET /workflows`, `GET /workflows/{id}`, `POST /runs`,
  `GET /runs/{id}`, `GET /runs/{id}/events` (SSE),
  `GET /runs/{id}/artifacts/{name}`, `POST /reload` (SIGHUP too).
  Bearer-token auth via `Authorization` header or `?token=` (for
  EventSource); constant-time compare; body cap; structured access
  log. Catalogue-only enforcement keeps the trust boundary at
  "who commits to the catalogue" (spec §16).
- **Embedded SPA** at `/`, styled to the Loom design mockup (dark
  oklch palette, IBM Plex font stack with system fallback). Vanilla
  ES module, no build step. Views: catalogue with search, workflow
  form generated from `inputs:` schema, live-run view with expandable
  per-step logs and connection-lost banner, history placeholder.
- **TTY renderer** disambiguates parallel step output by prefixing
  log lines with `[step-id]` whenever more than one step is in flight;
  single-active runs stay uncluttered.

### Changed

- **Minimum Go version bumped to 1.25** so the standard library ships
  with fixes for the CVEs `govulncheck` reports on older toolchains
  (GO-2026-4946 crypto/x509, GO-2026-4918 net/http HTTP/2, GO-2026-4870
  crypto/tls KeyUpdate). None of these were in Weftly code; the change
  is the correct remediation.
- `run` action: kill the whole process group with SIGKILL on timeout
  and set `cmd.WaitDelay` so an orphaned grandchild holding an inherited
  stdio pipe cannot keep the step running past its deadline.
- CI matrix reduced from (Go 1.23, 1.24) to (Go 1.25.x) to match the
  new floor; still crosses ubuntu + macos.

### Added

- Project OSS scaffolding: LICENSE (Apache-2.0), NOTICE, SECURITY.md,
  CONTRIBUTING.md, CODE_OF_CONDUCT.md, CHANGELOG.md.
- GitHub Actions: CI matrix (lint + vet + test on Linux and macOS,
  Go 1.23/1.24), CodeQL scanning, `govulncheck` weekly.
- Dependabot configuration for Go modules and GitHub Actions.
- Release pipeline: tag-driven GoReleaser build producing linux/darwin/
  windows on amd64/arm64, checksums, and a GitHub Release with
  auto-generated notes.
- Issue templates (bug / feature) and pull request template.
- New flagship example built around the public
  [Spring PetClinic](https://github.com/spring-petclinic) REST domain
  (`examples/petclinic-onboarding.yml`) and a matching in-process mock
  under `internal/mockpetclinic`.

### Changed

- Replaced the internal TN-shaped example with the generic PetClinic
  workflow so the repository has no hard dependency on a proprietary
  domain.

## [0.1.0] — 2026-07-22

Initial Phase 1 release.

### Added

- Workflow schema + validator (`weftly validate`).
- Compile → IR (ordered graph; DAG parallelism deferred).
- Expression engine wrapping `expr-lang/expr` with `${{ ... }}`
  interpolation.
- Event bus + TTY renderer (grouped per-step output with glyphs and
  timing) and JSON event stream (`--json`).
- Built-in actions: `run`, `http`, `template`, `assert`, `summary`,
  `upload` (plus a `prompt` stub reserving the name).
- Filesystem-only state (`.weftly/runs/<id>/state.json`) and
  self-contained `report.html`.
- CLI: `run`, `validate`, `list`, `version`.

[Unreleased]: https://github.com/bkum/weftly/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/bkum/weftly/releases/tag/v0.1.0
