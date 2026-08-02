# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Everything below shipped after 0.3.0 but was never written down — the
distributed binary reached 0.5.0 while this file still ended at 0.3.0.
Reconstructed from the source tree. See `docs/FEATURES.md` for the
complete current feature record.

### Added — Phase 4 (workflow expressiveness)

- **`retry:` step modifier.** `attempts` (2–20), `delay`, `backoff`
  (`""` constant / `linear` / `exponential`), and `on:` to choose which
  terminal statuses retry (`failed`, `timed-out`). A `StepRetry` event
  is emitted per attempt so renderers show the loop rather than a
  mysteriously long-running step.
- **Status functions.** `success()`, `failure()`, `always()`,
  `cancelled()` in expressions, populated meaningfully during the
  `cleanup:` pass.
- **`cleanup:` blocks.** Run-level teardown that executes after the main
  graph regardless of outcome, on a detached context so a cancelled run
  still tears down.
- **`for-each:` step modifier.** Expression → list; runs the step body
  once per element with `each.value` / `each.index` in scope.
- **`wait` action.** Poll until a condition holds or a budget expires.
- **`parse` action.** Extract structure from text — JSON flattening and
  regex named groups — into step outputs.
- **`notify` action.** POST a Slack-shaped or fully custom payload to a
  webhook; non-2xx is a step failure.
- **Audit trail.** Append-only JSON-lines log of mutating requests
  (`--audit-file`), with an in-memory tail at `GET /audit` (admin only).
- **MCP mode.** `weftly mcp` serves the Model Context Protocol over
  stdio (`initialize`, `tools/list`, `tools/call`), exposing a catalogue
  as callable tools.
- **CI mode.** `weftly run --ci` emits GitHub-Actions
  `::group::` / `::endgroup::` markers so CI viewers collapse
  successful steps.
- **Adoption tooling.** `weftly init` scaffolds a valid starter
  workflow; `weftly fmt` canonically formats (idempotent); `weftly diff`
  structurally compares two workflows and exits non-zero on difference.
- **OpenTelemetry tracing.** `--otel-endpoint` exports `workflow.run`
  and `workflow.step` spans over OTLP/HTTP. No-op when unset.

### Added — workflow composition

- **Step-level `include:`.** One workflow calls another as a step,
  passing inputs via `with:` and consuming the child's declared
  top-level `outputs:` at `steps.<include-id>.outputs.*`. Expanded at
  compile time into a flat DAG; the scheduler never learns composition
  exists. Resolution is scope-based rather than string-rewriting, so
  error messages and `--dry-run` plans always match the source file.
- **`with_if_set:`.** Binds a child input only when the expression
  resolves non-empty, leaving the child's own `default:` in force
  otherwise. Fixes the case where a caller forwarding its own unset
  optional input clobbers a sensible default with `""`.
- **Workflow-level `outputs:`.** The declared output contract a
  workflow exposes when included.
- **`workflow.dir` and `workspace.dir` expression namespaces.**
  `workflow.dir` is the directory of the YAML file that authored the
  step (where the *code* lives — read-only, shared across runs);
  `workspace.dir` is that step's working directory (where this run's
  *data* goes — writable, per-scope). Input defaults are interpolated,
  so a library can carry its own bundled assets via
  `default: "${{ workflow.dir }}/profiles/x12.json"`.
- **Per-scope workspaces.** Each include gets its own workspace
  subdirectory, so the same fragment used twice in one run cannot
  overwrite its own output. A caller that *wants* sharing passes
  `${{ workspace.dir }}/…` through `with:` — evaluated in the caller's
  scope, so both children cooperate on one directory.
- **Scope-qualified artifacts.** Artifacts collected inside an include
  are named `<scope>__<file>`, identically in the local artifacts
  directory and in the S3 object key. Qualifying one tier but not the
  other would separate siblings' artifacts on disk and re-collide them
  in the bucket.
- **`--include-root`.** Separates the *catalogue* boundary (`--dir`,
  which workflows are runnable) from the *trust* boundary (which files
  an include may reach). Defaults to `--dir`; widen it for projects
  laying out `workflows/` and `lib/` as siblings.

### Added — scope teardown and library fragments

- **`finally:` — scope teardown.** Steps that run after *their own
  workflow's* steps complete, whatever the outcome. Where `cleanup:` is
  run-level and fires once at the very end, `finally:` belongs to the
  workflow that declares it, so an included fragment can tear down just
  what it created without knowing anything about its caller. Teardown
  nodes are exempt from the scheduler's cascade-skip — that protection
  is right for downstream work and exactly wrong for teardown, which
  exists to run after failure. Nested fragments tear down
  innermost-first.
- **Scope-relative status functions.** Inside a step belonging to an
  include, `success()` / `failure()` now report *that scope's*
  aggregate status rather than the run's, so a fragment's `finally:`
  can ask "did **my** steps succeed" instead of "did anything anywhere
  fail". Top-level steps are unchanged. A `continue-on-error` failure
  counts as `failure()` for this purpose: `continue-on-error` is about
  run control flow, not about whether the work succeeded, and teardown
  cares only about the latter.
- **`library: true`.** Marks a fragment that may only be included,
  never run on its own. Excluded from `GET /workflows` and
  `GET /workflows/{id}`, rejected by `POST /runs`, and rejected at
  schedule **load** time (not fire time — a schedule that only fails
  when its cron next matches is a latent misconfiguration the operator
  discovers hours later). Still freely includable.

  This is an authorisation control rather than listing hygiene: without
  it every fragment in a toolkit is an ordinary catalogue entry,
  independently triggerable by any principal holding `workflows: "*"`
  and schedulable, despite being written to run only as part of a
  caller that supplies its inputs.

### Added — typed inputs and presets

- **Input type system.** `string` (default), `int`, `number`, `bool`,
  `enum`, `duration`, `json`, `path`, `list`, with constraints:
  `pattern` / `min_length` / `max_length` on strings, `min` / `max` on
  numerics and durations, `values` on enums, `items` / `min_items` /
  `max_items` on lists, `must_exist` on paths. Omitting `type:` means
  `string`, so every existing workflow is unchanged.

  Coercion is strict: `3.0` is not an `int`. Silent truncation is how
  `cases: 2500.7` becomes 2500 and someone loses an afternoon.

- **All input errors reported at once.** A form with three bad fields
  produces three errors in one report rather than forcing three round
  trips. Enum near-misses get a Levenshtein did-you-mean plus the full
  value list.

- **Secrets never echo their value.** A constraint violation on a
  `secret: true` input reports the constraint and nothing else — no
  value, no did-you-mean, no allowed-value list, since a suggestion
  leaks the credential a character at a time. `secret:` stays a flag
  rather than a type, so a secret can be a constrained string, a path,
  or anything else.

- **Expression defaults.** A default may reference other inputs,
  `env.*`, `run.*`, `workflow.dir`, and `workspace.dir` — but not
  `steps.*`, since inputs resolve before any step runs. Inputs resolve
  in dependency order; reference cycles are a compile-time error. A
  supplied value never triggers its own default's evaluation.

- **`presets:`.** Named bundles of input values, validated at
  `weftly validate` time: every key must name a declared input and every
  value must satisfy that input's constraints, so `domain: retial` fails
  in review rather than in front of a customer. **A preset may not
  supply a `secret: true` input** — presets live in committed YAML that
  `GET /workflows/{id}` exposes, so that would be a credential in
  version control. Applied with `--preset` or `POST /runs {"preset":...}`;
  `--input` still wins. One preset per run, no inheritance.

- **`weftly describe`.** Prints a workflow's inputs with type,
  constraints, default, and required/secret flags, plus its presets —
  the thing to run before writing `--input` flags.

- **`POST /runs` returns 400, not 500, for a bad input**, with a
  structured per-field error list the SPA can render against the
  offending control.

- **SPA typed controls.** `enum` → dropdown, `int`/`number` with bounds →
  bounded number field, `bool` → toggle, `duration` and `list` → format
  hints, `pattern` / length → native inline validation. Presets render
  as a row of buttons above the form that populate it.

### Fixed

- **Release archives were missing `workflows/` and `examples/`.**
  GoReleaser v2's bare-string globs matched nothing for `workflows/**/*`
  and warned rather than failing, so tarballs shipped with only the
  binary. Switched to the structured `src:`/`dst:` form and added a CI
  job that builds a snapshot, inspects the tarball, and runs the shipped
  binary against the shipped workflow.
- **Step errors were invisible in the SPA.** `StepFinished.Err` is a Go
  `error` interface and marshalled as `{}`. Added `MarshalJSON` on
  `StepFinished` and `StepRetry`, and taught the SPA to render it.
- **SSE reconnects duplicated the log.** The server replayed the whole
  event log on every reconnect. Added `id:` / `Last-Event-ID` dedupe and
  closed the `EventSource` on `RunFinished`.
- **Form fields lost their metadata.** `schema.Input` had only YAML
  tags, so JSON marshalling emitted Go-cased names and the SPA silently
  dropped `description` / `required` / `default` / `secret`. Added JSON
  tags and `enum` picklist rendering.
- **Windows release builds failed.** `syscall.Kill` and
  `SysProcAttr.Setpgid` are POSIX-only; a runtime `GOOS` check doesn't
  prevent compilation. Split into `run_unix.go` / `run_windows.go`.
- Secret masking now recurses into `map[string]any` and `[]any`.

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
