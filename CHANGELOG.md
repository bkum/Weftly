# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] — 2026-07-23

Phase 3: multi-tenant server. RBAC, run history, remote artifact store,
cron-driven schedules, opt-in container executor, GitHub Actions
ingestion, cancel-run endpoint + Schedules SPA page. Under the hood a
race in the shell-step log path (`StdoutPipe` + Wait) that intermittently
dropped lines under `GOMAXPROCS=1 -race` on CI is fixed by moving to
`cmd.Stdout = *lineWriter`.

### Added — Phase 3

- **RBAC.** `--auth-file weftly.yaml` maps opaque tokens → named
  principals → roles → workflow allowlists (or `workflows: "*"`).
  Roles can be flagged `admin: true` to unlock `POST /reload`.
  All catalogue / run endpoints filter by the principal's allowlist,
  so a caller never sees workflows or runs they can't touch.
- **Run history.** `GET /runs` returns every run persisted on disk
  (newest first, optional `?workflow=<id>` filter). The SPA gained
  a Run History page and each workflow-detail page shows its recent
  runs strip.
- **Remote artifact store.** `--s3-endpoint / --s3-bucket / --s3-prefix /
  --s3-access-key / --s3-secret-key / --s3-plaintext` mirror every
  `upload` action's output to an S3-compatible bucket (AWS, MinIO, R2,
  Spaces). `GET /runs/{id}/artifacts/{name}` transparently falls back
  to the bucket when the local file is absent (after retention pruning).
  Remote-store failures are logged, never fatal — the local copy is
  authoritative.
- **Scheduled runs.** `weftly server --schedules schedules.yaml` starts
  a per-minute cron scheduler that dispatches catalogue workflows on
  their cadence. Ships with an in-tree cron parser (5 fields + `@hourly
  / @daily / @weekly / @monthly / @yearly` descriptors) so no runtime
  dep is added. New endpoints: `GET /schedules`, `GET /schedules/{id}`,
  `POST /schedules/{id}/trigger`. `POST /reload` + `SIGHUP` also re-read
  `schedules.yaml`. Bad cron on one entry surfaces as `parse_error` on
  that entry, not a whole-file reject.
- **Container executor.** New `container: <image>` field on a `run:`
  step wraps the shell in `podman run` / `docker run` (podman preferred,
  docker fallback). Workspace, script, and `$WEFTLY_OUTPUT` are
  bind-mounted; env vars validated as POSIX identifiers before
  `-e KEY=VAL`; `--network=none` by default. No engine on `$PATH` →
  actionable error, never silent fallback to host exec.
- **`weftly import-gha`** ingests a GitHub Actions workflow YAML and
  emits an equivalent weftly workflow to stdout (or `--out`).
  Translation notes for every unsupported construct (`uses:`, `matrix:`,
  `on:`, GHA-only expression helpers, ...) go to stderr; the emitted
  YAML is always re-validated against `schema.Validate` before writing.
- **`DELETE /runs/{id}`** cancels an in-flight run. Bound to a Cancel
  button in the SPA that appears while the run is live and hides on
  RunFinished. Cancelling a completed run is idempotent (200 with
  `already_finished:true`).
- **Schedules page** in the SPA — sidebar entry + per-schedule row with
  id, workflow, cron, next-fire, last-error, and a Trigger-now button.

### Fixed — Phase 3

- `actions/run`: dropped `StdoutPipe` + external scanner goroutines in
  favor of `cmd.Stdout = *lineWriter`. The previous pattern raced
  `cmd.Wait` closing the pipe against our reader — under
  `GOMAXPROCS=1 + -race` on CI, whole log lines could vanish, which
  made `TestRunActionMasksSecretsInLogs` flake intermittently on both
  ubuntu and macOS runners.
- `actions/run` timeout path already forced `SIGKILL` to the process
  group with a `cmd.WaitDelay` guard; that stays in place for the new
  writer path.

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

[Unreleased]: https://github.com/bkum/weftly/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/bkum/weftly/releases/tag/v0.3.0
[0.1.0]: https://github.com/bkum/weftly/releases/tag/v0.1.0
