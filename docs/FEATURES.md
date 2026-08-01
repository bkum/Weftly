# Weftly — Supported Features

A complete record of what Weftly does today, verified against the source
tree rather than against older design documents. Each entry names the
package that implements it so a reader can go straight to the code.

**Status legend** — ✅ shipped · 🟡 partial (scope noted) · ❌ not implemented

---

## 1. Workflow model

| Feature | Status | Notes | Implementation |
|---|---|---|---|
| YAML workflow schema | ✅ | `name`, `description`, `inputs`, `env`, `defaults`, `steps`, `cleanup`, `outputs`, `requires` | `internal/schema` |
| Static validation | ✅ | `weftly validate` — ids, action keys, `needs` references, `needs` cycles, retry bounds | `internal/schema/validate.go` |
| Compile to IR | ✅ | Workflow → topologically-schedulable `[]StepNode` | `internal/compile`, `internal/ir` |
| Step ids | ✅ | Must match `[a-z0-9_]+`. **Underscores only — hyphens are rejected** because `resolve-id` parses as subtraction inside an expression | `internal/schema/validate.go` |
| `requires:` preflight | ✅ | Named host tools checked on `PATH` before any step runs | `internal/engine` |

### Step modifiers

| Modifier | Status | Notes |
|---|---|---|
| `id:` / `name:` | ✅ | `summary`, `upload`, `assert` may omit `id` |
| `if:` | ✅ | Expression gate; accepts bare or `${{ }}`-wrapped form |
| `needs:` | ✅ | Explicit DAG edges; absent means "the previous named step" |
| `env:` | ✅ | Per-step environment, expression-interpolated |
| `continue-on-error:` | ✅ | Failure becomes `failed-continued`, run proceeds |
| `timeout:` | ✅ | Per-step deadline → `timed-out` status |
| `shell:` | ✅ | Per-step override of `defaults.shell` |
| `container:` | ✅ | Run the step inside an image (`run` steps only) |
| `retry:` | ✅ | `attempts` (2–20), `delay`, `backoff: ""\|linear\|exponential`, `on: [failed, timed-out]` |
| `for-each:` | ✅ | Expression → list; runs the step body once per element |
| `outputs:` | ✅ | Declared output mapping for `http` / `template` |

---

## 2. Actions

| Action | Status | Purpose |
|---|---|---|
| `run` | ✅ | Shell script. Outputs via `$WEFTLY_OUTPUT` (`key=value` lines / heredoc) |
| `http` | ✅ | Declarative request with inline `assert:`, response available as `response.*` |
| `template` | ✅ | Render a template file into the workspace |
| `assert` | ✅ | Standalone expression assertion |
| `summary` | ✅ | Markdown block collected into `report.html` |
| `upload` | ✅ | Collect workspace files as run artifacts |
| `prompt` | ✅ | Interactive `text` / `password` / `confirm` / `select`; `--yes` auto-answers |
| `wait` | ✅ | Poll until a condition holds or a budget expires |
| `parse` | ✅ | Extract structure from text (JSON / regex named groups) into outputs |
| `notify` | ✅ | POST a Slack-shaped or custom payload to a webhook |
| `include` | ✅ | Step-level workflow composition — see §4 |

`include_outputs` is an internal synthesized action, not authored directly.

---

## 3. Expressions

`${{ ... }}` interpolation over [expr-lang](https://expr-lang.org).

| Namespace | Status | Contents |
|---|---|---|
| `inputs.*` | ✅ | Resolved workflow inputs (scope-aware inside includes) |
| `steps.<id>.outputs.*` / `.status` | ✅ | Prior step results (scope-aware inside includes) |
| `env.*` | ✅ | Workflow env merged with `--var` overrides |
| `secrets.*` | ✅ | Inputs declared `secret: true` |
| `run.id` / `run.workspace` / `run.status` / `run.cancelled` | ✅ | Run metadata |
| `response.*` | ✅ | Set by the `http` action for its own `outputs:` mapping |
| `each.value` / `each.index` | ✅ | Inside a `for-each` iteration |
| `workflow.dir` | ✅ | Directory of the YAML file that authored this step — **where the code lives** |
| `workspace.dir` | ✅ | This step's working directory — **where this run's data goes** |

`workflow.dir` vs `workspace.dir` is the distinction composed workflows
most often get wrong. `workflow.dir` is read-only and shared by every
run (use it to reach a library's bundled assets); `workspace.dir` is
writable and per-scope (use it to place output).

### Status functions

| Function | Status | Semantics |
|---|---|---|
| `success()` | ✅ | Run has not failed so far |
| `failure()` | ✅ | Run status is `failed` or `timed-out` |
| `always()` | ✅ | Always true |
| `cancelled()` | ✅ | Run context was cancelled |

Populated meaningfully during the `cleanup:` pass, which is what the
helpers exist for. **Scope-relative evaluation is not implemented** —
they report the *run's* aggregate status, not an enclosing include's.

### Built-in helpers

`default(v, fallback)`, `fromJSON`, `toJSON`. `contains`, `startsWith`,
`endsWith` are expr-lang native operators, used as `s contains "x"`.

---

## 4. Workflow composition (`include:`)

Two distinct mechanisms share the keyword.

### 4.1 Top-level `include:` — prelude merge

```yaml
include:
  - ../lib/common.yml     # steps prepended, env merged
```

Expanded at parse time by `schema.Load`. The included file's `name`,
`inputs`, and `description` are discarded — this is a step library, not
a callable unit. Cycles detected.

### 4.2 Step-level `include:` — parameterised call

```yaml
- id: edi
  include: ../lib/edi-generate.yml
  with:
    transaction: "${{ inputs.transaction }}"
  with_if_set:
    parties_json: "${{ inputs.parties_json }}"
```

| Aspect | Status | Behaviour |
|---|---|---|
| Compile-time expansion | ✅ | Child steps inlined as `<include-id>.<child-id>`; the scheduler never learns composition exists |
| Scope-based resolution | ✅ | `inputs.*` / `steps.*` resolve through an `ir.Scope` chain. **Expression strings are never rewritten**, so errors and `--dry-run` plans match the source file |
| `with:` | ✅ | Bind a child input unconditionally |
| `with_if_set:` | ✅ | Bind **only when non-empty** — an unset caller input leaves the child's `default:` in force. `nil` and `""` count as empty; `0` and `false` do not |
| Workflow-level `outputs:` | ✅ | The child's declared output contract, surfaced at `steps.<include-id>.outputs.*` |
| Per-scope workspaces | ✅ | Each include gets `workspace/<prefix>/`, so the same fragment used twice can't overwrite its own output |
| Artifact qualification | ✅ | Artifacts from a scope are named `<prefix>__<file>`, **identically in the local artifacts dir and in S3** |
| Interpolated defaults | ✅ | A child's `default:` may use `${{ workflow.dir }}` so libraries carry their own assets |
| Nesting | ✅ | Depth capped at 5; scope chain recurses |
| Cycle detection | ✅ | Resolved-path chain, error lists the cycle |
| Path confinement | ✅ | Symlinks resolved, then confined — see §7 |
| Modifier propagation | ✅ | `if:` conjoined into each child; `continue-on-error:` propagated; `needs:` applied to the first child |
| Rejected modifiers | ✅ | `env:`, `timeout:`, `retry:`, `for-each:` on an include step are validation errors naming the alternative |
| `finally:` (scope teardown) | ❌ | Only run-level `cleanup:` exists |
| `library: true` | ❌ | Fragments are not yet excluded from the catalogue — see §10 |
| `type: path` outputs | ❌ | No automatic rebasing of path-typed outputs |

---

## 5. Execution

| Feature | Status | Notes |
|---|---|---|
| DAG scheduler | ✅ | Honours `needs:`; independent branches run concurrently |
| `--parallel N` | ✅ | Global concurrency cap, default 4 |
| Cascade skip | ✅ | Downstream of a fatal failure is `skipped`, not run |
| `cleanup:` | ✅ | Run-level teardown after the main graph, whatever the outcome; sees real run status via status functions |
| `--resume <run-id>` | ✅ | Replays successful steps from `state.json`, re-runs the rest |
| Cancellation | ✅ | SIGINT / `DELETE /runs/{id}`; cleanup still runs via a detached context |
| Per-run workspace | ✅ | `.weftly/runs/<run-id>/workspace` (+ per-scope subdirectories) |
| Secret masking | ✅ | Applied at the event boundary; recurses into maps and slices |

### Resume semantics — known gap

`for-each` iterations are **not** individually resumable. `--resume`
operates at step granularity, so a partially-failed loop re-runs in
full. Fine for idempotent bodies, wasteful otherwise.

---

## 6. CLI

| Command | Status | Purpose |
|---|---|---|
| `weftly run <file>` | ✅ | Execute a workflow |
| `weftly validate <file>` | ✅ | Static checks, non-zero exit on failure |
| `weftly list <dir>` | ✅ | Enumerate a catalogue |
| `weftly server` | ✅ | REST + SSE + SPA — see §7 |
| `weftly init` | ✅ | Scaffold a valid starter workflow |
| `weftly fmt <file>` | ✅ | Canonical formatting (idempotent) |
| `weftly diff <a> <b>` | ✅ | Structural workflow diff, non-zero exit on difference |
| `weftly import-gha <file>` | ✅ | Convert a GitHub Actions workflow |
| `weftly mcp` | ✅ | Model Context Protocol server over stdio |
| `weftly version` | ✅ | Version / commit / build date |
| `weftly schema` | ❌ | JSON Schema emission |
| `weftly docs` | ❌ | Generate workflow documentation |
| `weftly test` | ❌ | Workflow unit-test harness |

### `run` flags

`--input k=v` · `--input-file` · `--var k=v` · `--dry-run` · `--json` ·
`--no-color` · `--strict` · `--yes` · `--parallel N` · `--resume` ·
`--ci` · `--otel-endpoint`

`--dry-run` prints the **expanded** plan with qualified ids, so an
include's children are visible before anything executes.

`--ci` emits GitHub-Actions `::group::` / `::endgroup::` markers.

---

## 7. Server

| Feature | Status | Notes |
|---|---|---|
| REST API | ✅ | `/workflows`, `/runs`, `/schedules`, `/audit`, `/healthz`, `/reload` |
| SSE event stream | ✅ | `GET /runs/{id}/events`, with `Last-Event-ID` reconnect dedupe |
| Embedded SPA | ✅ | Catalogue, run form, live run view, history, schedules |
| Catalogue-only execution | ✅ | Arbitrary submitted YAML is never runnable |
| Bearer token auth | ✅ | `--token` / `WEFTLY_TOKEN`; warns loudly when unset |
| RBAC | ✅ | `--auth-file` maps tokens → principals → roles → workflow allowlists |
| Run history | ✅ | `GET /runs`, optional `?workflow=` filter |
| Cancel a run | ✅ | `DELETE /runs/{id}` |
| Artifact download | ✅ | Local-first, S3 fallback |
| S3 artifact mirroring | ✅ | Any S3-compatible endpoint (AWS, MinIO, R2, Spaces) |
| Cron schedules | ✅ | `--schedules`; 5-field cron plus `@`-descriptors; SIGHUP / `POST /reload` |
| Audit log | ✅ | Append-only JSON-lines of mutating requests; in-memory tail at `GET /audit` |
| Catalogue hot-reload | ✅ | `POST /reload` (admin) or SIGHUP |
| OpenTelemetry | ✅ | `--otel-endpoint`, OTLP/HTTP, `workflow.run` + `workflow.step` spans |

### Include confinement

Two separate boundaries, deliberately:

- `--dir` — the **catalogue**: which workflows can be *run*.
- `--include-root` — the **trust boundary**: which files an include can
  *reach*. Defaults to `--dir`.

For a project laying out `workflows/` and `lib/` as siblings:

```
weftly server --dir ./workflows --include-root .
```

Rules: paths resolve relative to the *including file*; absolute paths
and URLs rejected; symlinks resolved *before* the confinement check;
`..` allowed only while the result stays under the root.

---

## 8. Observability

| Feature | Status | Notes |
|---|---|---|
| Typed event bus | ✅ | Every renderer is a subscriber; nothing writes to stdout directly |
| TTY renderer | ✅ | Per-step grouping, status glyphs, `[step-id]` prefix when parallel |
| JSON renderer | ✅ | `--json`, one typed envelope per line |
| `state.json` | ✅ | Per-run persisted state, flushed on every event |
| `report.html` | ✅ | Self-contained: per-step table, summaries, artifacts |

---

## 9. Distribution

| Feature | Status | Notes |
|---|---|---|
| Single static binary | ✅ | `CGO_ENABLED=0`, no runtime dependencies |
| Cross-platform | ✅ | linux/darwin amd64+arm64, windows/amd64 |
| Release archives | ✅ | Include a runnable `workflows/` directory and `examples/` |
| CI release verification | ✅ | Every PR builds a snapshot, inspects the tarball, and runs the shipped binary against the shipped workflow |

---

## 10. Not implemented

Tracked so the gap is explicit rather than discovered.

| Item | Why it matters |
|---|---|
| `finally:` — scope-level teardown | Run-level `cleanup:` exists, but a failed include can't tear down just its own work |
| Scope-relative status functions | `success()` / `failure()` report run status, not enclosing-scope status — the prerequisite for `finally:` |
| `library: true` | **Authorisation gap.** Library fragments are ordinary catalogue entries: independently runnable by any principal with `workflows: "*"`, and schedulable |
| `type: path` outputs | Path outputs crossing an include boundary are not rebased automatically |
| `$WEFTLY_OUTPUT_JSON` / `outputs_from:` | `Outputs` is already `map[string]any`, but a `run` step can only emit `key=value` strings |
| Typed input constraints | `enum` ships; `int` / `min` / `max` / `pattern` do not |
| `presets:` | No named input bundles, and no SPA affordance for them |
| Per-iteration `for-each` resume | See §5 |
| `weftly schema` / `docs` / `test` | Adoption tooling |
| Run diffing | `weftly diff` compares *workflows*; comparing two *runs* is not implemented |

---

## 11. Explicit non-goals

- **Runtime multi-format parsing.** `import-gha` is a compile-time
  convert-and-review seam, not a GHA-compatible runtime. Converted
  workflows are ordinary Weftly YAML thereafter.
- **Remote / URL includes.** An include is a reviewed dependency; a URL
  makes a reviewed workflow a moving target. Would require digest
  pinning to reconsider.
- **Hyphenated step ids.** Unambiguously parseable ids beat familiarity
  with GitHub Actions here.
