# Loom — Design Document

**A single-binary, workflow-driven utility & runbook engine**

| | |
|---|---|
| **Status** | Draft / RFC |
| **Version** | 0.1 |
| **Author** | Baijnath |
| **Date** | 2026-07-22 |
| **Codename** | `loom` *(placeholder — rename freely; binary name `loom` used throughout)* |
| **Audience** | Internal engineering; candidate for open-sourcing |

> **One-line pitch:** Turn operational procedures — onboarding, diagnostics, config generation, migration checks — into versioned, runnable, self-documenting workflows that stream live logs and emit a report, from a single Go binary with no external dependencies.

---

## Table of contents

1. [Motivation](#1-motivation)
2. [Goals and non-goals](#2-goals-and-non-goals)
3. [Use cases](#3-use-cases)
4. [Concepts and terminology](#4-concepts-and-terminology)
5. [Workflow schema specification](#5-workflow-schema-specification)
6. [Built-in action catalog](#6-built-in-action-catalog)
7. [Shell execution — deep dive](#7-shell-execution--deep-dive)
8. [Architecture](#8-architecture)
9. [Execution model and lifecycle](#9-execution-model-and-lifecycle)
10. [Expression engine](#10-expression-engine)
11. [Data flow between steps](#11-data-flow-between-steps)
12. [Output, logging and reporting](#12-output-logging-and-reporting)
13. [State and idempotency](#13-state-and-idempotency)
14. [CLI design](#14-cli-design)
15. [Server and UI mode](#15-server-and-ui-mode)
16. [Security model](#16-security-model)
17. [Extensibility](#17-extensibility)
18. [Distribution and operations](#18-distribution-and-operations)
19. [Roadmap and phasing](#19-roadmap-and-phasing)
20. [Open questions](#20-open-questions)
21. [Appendix A — Flagship workflow: webMethods getting-started helper](#appendix-a--flagship-workflow-webmethods-getting-started-helper)
22. [Appendix B — Comparison with existing tools](#appendix-b--comparison-with-existing-tools)

---

## 1. Motivation

Every engineering organisation accumulates operational knowledge that lives in the wrong places: a wiki page nobody trusts, a senior engineer's head, a `notes.txt`, a pile of half-documented shell scripts. Onboarding a new trading partner, validating a fresh environment, generating a config bundle, running a health diagnostic — these are *procedures*, and today they are executed by copy-pasting commands and eyeballing output.

CI/CD systems (GitHub Actions, GitLab CI) proved that expressing procedures as **declarative, versioned workflow files** with **live logs** and **artifacts** is enormously valuable. But those systems are built for *building and shipping software on a hosted runner fleet*. They are the wrong tool for a developer who just wants to run an operational procedure locally, or offer a "getting started" helper to a colleague, without a Postgres database, a Kubernetes cluster, or a Docker daemon.

There is a genuine gap: **no widely-adopted open-source tool ships as a single self-contained binary that (a) defaults to a CLI, (b) can flip to a server + UI mode, (c) executes GitHub-Actions-style step workflows, and (d) treats summary and artifact generation as first-class outputs.** `nektos/act` is the closest but is a GitHub-Actions *local runner* — CLI-only, Docker-dependent, single-format. `Dagger` is SDK-first and container-bound. `Windmill`/`Kestra` are full self-hosted platforms requiring a database.

**Loom fills this gap as a runbook/utility engine**, not a CI competitor. Its differentiator is deliberately narrow: *one binary, no database, no container runtime required, CLI-first with an optional UI.*

---

## 2. Goals and non-goals

### Goals

- **Single static binary.** Download and run. No Postgres, no Docker requirement, no runtime dependencies beyond the host OS shell and whatever tools a workflow explicitly invokes.
- **CLI-first, default mode CLI.** `loom run workflow.yml` is the primary path. Server/UI is an optional mode selected by parameter.
- **Declarative workflow files** with a clean, single native schema — GitHub-Actions-*inspired* but not GitHub-Actions-*compatible*.
- **A small, curated set of built-in actions** compiled into the binary: `run` (shell), `http`, `template`, `prompt`, `assert`, `summary`, `upload`.
- **Shell as a first-class escape hatch** for arbitrary data transformation between steps (see §7).
- **Live, streamed logs** in the CLI, identical event stream reused by the UI.
- **First-class reporting**: `summary` blocks and `upload` artifacts produced at the end of a run.
- **Idempotent, re-runnable workflows** — critical for "getting started" and onboarding helpers.
- **Internal representation discipline**: steps compile to a DAG/IR, so a future multi-format front-end is cheap to add *if ever needed*.

### Non-goals (v1)

- **Not a CI/CD replacement.** Loom does not manage build/test/deploy pipelines for shipping software, does not integrate with SCM webhooks as a merge gate, and does not aim to replace GitHub Actions.
- **Not multi-format input.** v1 ships exactly one parser. GitHub/GitLab/CircleCI ingestion is explicitly deferred.
- **No plugin marketplace / `uses:` ecosystem.** Custom logic escapes to `run:`. There is no third-party action registry in v1.
- **No container-based isolation** in v1. Execution is native. Containerised executors are a later, optional pluggable backend.
- **Not a distributed/clustered scheduler.** Single-host execution. No worker fleet.
- **Not a general-purpose scripting language.** The expression engine is intentionally small and safe; arbitrary logic belongs in `run:`.

Non-goals are load-bearing. They are what keep this a shippable weekend-to-weeks engine rather than a multi-year platform.

---

## 3. Use cases

The flagship and the reason for the project:

**U1 — webMethods B2B getting-started helper.** A new engineer or a customer points Loom at a Trading Networks environment. The workflow health-checks the environment, idempotently creates a sample trading partner, submits a sample X12 850, verifies the document flowed, and emits an HTML onboarding report. See [Appendix A](#appendix-a--flagship-workflow-webmethods-getting-started-helper).

Others that fall out naturally:

- **U2 — B2B System Health Diagnostic.** Express the existing `WmB2BSHDT` diagnostic logic as a Loom workflow: probe IS packages, TN config, DB connectivity, queue depths; emit a health report artifact.
- **U3 — Trading partner onboarding.** Bulk-create partners, agreements, and processing rules from an input file; idempotent, resumable, with a summary of what was created vs. already present.
- **U4 — Environment validation / pre-flight.** Before a release, run a workflow that asserts config parity, endpoint reachability, cert expiry, and version alignment across environments.
- **U5 — Config generation.** Template out environment-specific configuration bundles and upload them as artifacts.
- **U6 — Migration checks.** Run a series of read-only assertions against source and target systems and produce a go/no-go report.

The common shape across all of these: *a sequence of probes and mutations against REST/CLI surfaces, with transformation between steps, ending in a human-readable report and downloadable artifacts.*

---

## 4. Concepts and terminology

| Term | Meaning |
|---|---|
| **Workflow** | A YAML file declaring `inputs` and an ordered list of `steps`. The unit a user runs. |
| **Step** | A single unit of execution. Has an `id`, an action, optional `if:`, `env:`, `needs:`, and produces outputs. |
| **Action** | The *kind* of a step — `run`, `http`, `template`, etc. Exactly one action per step. |
| **Input** | A declared parameter to the workflow, supplied via flag, file, env, or interactive prompt. May be `secret`. |
| **Output** | A named value a step exposes for downstream steps: `steps.<id>.outputs.<key>`. |
| **Expression** | A `${{ ... }}` template resolved by the engine against the run context. |
| **Run** | A single execution of a workflow, with a unique run-id and its own workspace and state. |
| **Workspace** | A per-run directory shared by all steps; files written by one step are visible to the next and to `upload`. |
| **Summary** | A markdown block emitted to the report and console. |
| **Artifact** | A file collected by `upload` and surfaced as a downloadable output of the run. |

---

## 5. Workflow schema specification

A workflow is a YAML document. This is the *single native schema*.

```yaml
name: string                       # required, human name
description: string                # optional

inputs:                            # optional map of declared inputs
  <name>:
    description: string
    required: bool                 # default false
    default: any                   # default value if not supplied
    secret: bool                   # default false; masked in logs
    type: string|number|bool       # default string; used for validation & coercion

env:                               # optional workflow-level env, available to all steps
  <KEY>: <value or ${{ expr }}>

defaults:                          # optional
  shell: bash                      # default shell for run steps
  http:
    timeout: 30s
    headers: { ... }               # merged into every http step

steps:
  - id: string                     # required, unique; used for outputs & needs
    name: string                   # optional display label
    if: ${{ expr }}                # optional; step skipped if false
    needs: [id, ...]               # optional explicit dependency; else implicit order
    env: { KEY: value }            # optional step env (merged over workflow env)
    continue-on-error: bool        # default false
    timeout: 60s                   # optional per-step timeout

    # --- exactly ONE action key per step ---
    run: | ...                     # shell (see §7)
    http: { ... }                  # http action (see §6)
    template: { ... }
    prompt: { ... }
    assert: ${{ expr }}
    summary: | ...
    upload: { path: ... }

    outputs:                       # optional explicit output mapping (for http/template)
      <key>: ${{ expr }}
```

### Validation rules

- Exactly one action key per step; the validator rejects steps with zero or multiple actions.
- `id` values are unique and match `[a-z0-9_-]+`.
- `needs` references must exist and must not form a cycle.
- Referenced inputs/outputs in expressions are resolvable (static analysis where possible; runtime error otherwise).
- Required inputs without defaults must be satisfiable at run time (flag/file/env/prompt).

`loom validate <file>` runs all of the above without executing anything.

---

## 6. Built-in action catalog

Seven actions ship compiled into the binary. This closed set is deliberate — it is the product surface, and it keeps custom logic funnelled through `run:`.

### 6.1 `run` — shell execution

The universal escape hatch. Full treatment in §7.

```yaml
- id: transform
  run: |
    echo "count=$(cat file | wc -l)" >> "$LOOM_OUTPUT"
  shell: bash            # optional override of defaults.shell
  env: { SRC: "${{ inputs.src }}" }
```

### 6.2 `http` — HTTP request with assertions and extraction

The workhorse for webMethods (everything is REST to TN/IS).

```yaml
- id: create
  http:
    POST: "${{ inputs.env_url }}/rest/partners"     # method as key: GET/POST/PUT/DELETE/PATCH
    headers:
      Authorization: "Bearer ${{ inputs.api_token }}"
      Content-Type: application/json
    body:                                            # object (JSON) or string
      name: "${{ inputs.partner_name }}"
    timeout: 30s
  assert: response.status == 201                      # optional inline assertion
  outputs:
    id: "${{ response.body.partnerId }}"              # extract from parsed response
```

`response` context inside an `http` step exposes `.status`, `.headers`, `.body` (parsed if JSON, raw string otherwise), and `.raw`. Extraction handles simple paths. Anything that needs real transformation (filtering a list, joining, reshaping) is *deliberately* pushed to a following `run:` step — this is the key division of labour (§7).

### 6.3 `template` — render a file from a template

```yaml
- id: gen-config
  template:
    src: ./templates/tn.properties.tmpl
    dest: ./out/tn.properties        # written into the workspace
    vars:
      host: "${{ inputs.host }}"
      partner_id: "${{ steps.create.outputs.id }}"
```

Go `text/template` semantics. Produces a workspace file that a later `upload` can collect.

### 6.4 `prompt` — interactive input

```yaml
- id: confirm
  prompt:
    message: "Proceed against ${{ inputs.env_url }}?"
    type: confirm            # confirm | text | password | select
    options: [yes, no]       # for select
  # value available as steps.confirm.outputs.value
```

Only engages when running in a TTY; in non-interactive mode a `prompt` for a value already supplied via flags is skipped, and an unsatisfiable required prompt fails fast. This is how one workflow serves both the automation path and the wizard experience.

### 6.5 `assert` — checkpoint

```yaml
- id: check-health
  assert: ${{ steps.health.outputs.status == "UP" }}
  # optional: message on failure
```

Fails the run (unless `continue-on-error`) with a clear diagnostic. Assertions are how a diagnostic/validation workflow expresses its pass/fail criteria.

### 6.6 `summary` — report block

```yaml
- summary: |
    ## Onboarding complete
    - Environment: ${{ inputs.env_url }}
    - Partner: ${{ inputs.partner_name }} (`${{ steps.create.outputs.id }}`)
    - Sample doc: ${{ steps.sample.outputs.doc_id }}
```

Markdown, expression-interpolated. Accumulated across the run and rendered to console at the end and into the report artifact.

### 6.7 `upload` — collect artifacts

```yaml
- upload:
    path: ./out/onboarding-report.html    # file or glob within workspace
    name: onboarding-report               # optional label
```

Collects workspace files as run artifacts. In CLI mode they are copied to `./.loom/runs/<id>/artifacts/` and their paths printed; in server mode they are served for download. Paths are validated to stay within the workspace (no traversal — see §16).

---

## 7. Shell execution — deep dive

This section answers the central question: **should we support shell like GitHub does, and how?** The answer is an emphatic yes, and the *how* is where the real design lives.

### 7.1 Why shell is mandatory, not optional

Declarative actions cover the common 80%: make a call, extract a field, assert, template. But you can never anticipate the transformations a real procedure needs — filter a list to active records, join IDs, compute a delta, base64-decode a payload, reshape one system's response into another system's request. `http` extraction deliberately handles only trivial field access. Everything else belongs in shell.

This is exactly the GitHub Actions philosophy: declarative `uses:`/action steps for the common path, `run:` for arbitrary logic. Without a shell escape hatch, the engine forces every unanticipated need into a new built-in action — an unbounded treadmill. With `run:`, the built-in set stays small and closed, and users reshape data with tools they already know (`curl`, `jq`, `sed`, `awk`).

The scenario you raised — *manipulate an HTTP response body before passing it downstream* — is the canonical case:

```yaml
- id: fetch
  run: |
    resp=$(curl -sf "$ENV_URL/rest/partners")
    ids=$(echo "$resp" | jq -r '[.partners[]|select(.status=="ACTIVE").id]|join(",")')
    echo "active_ids=$ids" >> "$LOOM_OUTPUT"
  env: { ENV_URL: "${{ inputs.env_url }}" }

- id: bulk
  http:
    POST: "${{ inputs.env_url }}/rest/bulk"
    body: { partners: "${{ steps.fetch.outputs.active_ids }}" }
```

### 7.2 The output mechanism — file, not stdout

Steps pass data forward via an **output file**, mirroring GitHub's `$GITHUB_OUTPUT` (and pointedly *not* the deprecated `::set-output::` stdout-command style, which was removed for injection reasons):

1. Before a `run` step, the engine creates a temp file and exposes its path as `$LOOM_OUTPUT`.
2. The script appends `key=value` lines. Multiline values use a heredoc delimiter:
   ```bash
   {
     echo "json<<__EOF__"
     echo "$payload"
     echo "__EOF__"
   } >> "$LOOM_OUTPUT"
   ```
3. On exit code 0, the engine parses the file into `steps.<id>.outputs.<key>`.
4. Downstream steps reference `${{ steps.<id>.outputs.<key> }}`.

Outputs come *only* from this file. Stdout/stderr are treated purely as logs. This cleanly separates "what the step said" (logs) from "what the step produced" (outputs), and closes the stdout-injection hole.

### 7.3 Passing values IN — env vars, not string interpolation (security-critical)

There are two ways to get an expression value into a script:

```yaml
# UNSAFE — interpolates the value into the script text
run: |
  echo "Hello ${{ inputs.partner_name }}"

# SAFE — passes the value as an environment variable
run: |
  echo "Hello $PARTNER_NAME"
env: { PARTNER_NAME: "${{ inputs.partner_name }}" }
```

If `inputs.partner_name` is `"; rm -rf / #`, the first form executes it. This is the well-known **GitHub Actions script-injection** vulnerability class. Loom's policy:

- The engine **prefers env-var passing**. Documentation and examples default to it.
- When an expression *is* interpolated directly into a `run:` body, the compiler emits a **warning** and the value is shell-quoted where feasible.
- A strict mode (`--no-inline-expr` or a workflow-level flag) can make direct interpolation into `run:` bodies a hard error.

### 7.4 Shell selection and portability

- Per-step `shell:` overrides the workflow `defaults.shell`.
- Default `bash`; falls back to `sh` if bash is absent. On Windows, `pwsh` then `cmd`.
- The engine invokes the host shell — it does **not** bundle one. This preserves the single-binary promise; the host already has a shell in every realistic ops context.

### 7.5 Host tool dependency — honest treatment

`run:` steps depend on whatever tools they call (`curl`, `jq`, …). Loom does not bundle these. For an internal ops engine this is correct and expected — the operator already has a shell and these tools. To fail fast and clearly rather than mid-run:

```yaml
requires: [curl, jq]     # optional workflow-level preflight
```

The engine checks these are on `PATH` before executing and aborts with an actionable message if not.

### 7.6 Streaming, capture, masking, exit codes

- **Streaming:** stdout/stderr are read line-by-line and emitted as `StepLog` events for live display; the UI later consumes the same events.
- **Masking:** any value registered as a secret (secret inputs, explicit `mask:` values) is redacted by the renderer *before* a line is displayed or persisted, so tokens in `curl` commands or echoed responses never leak.
- **Working directory:** each step runs with `cwd` = the run workspace, so files persist across steps and feed `upload`.
- **Exit code = status:** non-zero fails the step unless `continue-on-error: true`. Timeouts kill the process group and fail the step.

### 7.7 The trust boundary this introduces

Shell = arbitrary code execution as the engine's user. For **CLI/local** use this is a non-issue: the operator already has shell on that host, and workflow files are code reviewed via pull request exactly like any script. The trust boundary equals "who can author/approve a workflow," identical to who can edit `.github/workflows`.

**Server mode changes this** and is addressed in §16: a web trigger that let a less-privileged user submit an arbitrary workflow would be privilege escalation. The mitigation is that server mode runs *only curated, version-controlled workflows* — the UI triggers "run this reviewed workflow with these inputs," never "execute this arbitrary YAML."

---

## 8. Architecture

### 8.1 Component overview

```
                    ┌─────────────────────────────────────────────┐
                    │                  cmd/loom                    │
                    │        mode dispatch (cli | server)          │
                    └───────────────┬──────────────┬──────────────┘
                                    │              │
                        ┌───────────▼──────┐   ┌───▼───────────────┐
                        │   CLI front-end  │   │  Server front-end │  (phase 2)
                        │  flags · TTY     │   │  REST · SSE · UI  │
                        └───────────┬──────┘   └───┬───────────────┘
                                    │              │
                                    └──────┬───────┘
                                           │  (both drive the same core)
                    ┌──────────────────────▼───────────────────────┐
                    │                    engine                     │
                    │  ┌────────┐  ┌──────────┐  ┌───────────────┐  │
                    │  │ schema │→ │ compile  │→ │   IR / DAG    │  │
                    │  │ parse  │  │ (1 parser)│  │  (step nodes) │  │
                    │  └────────┘  └──────────┘  └──────┬────────┘  │
                    │                                   │           │
                    │            ┌──────────────────────▼────────┐  │
                    │            │        scheduler / runner     │  │
                    │            │  order · if · timeout · state │  │
                    │            └───────────┬───────────────────┘  │
                    │                        │ dispatch             │
                    │            ┌───────────▼───────────────────┐  │
                    │            │           actions             │  │
                    │            │ run·http·template·prompt·...  │  │
                    │            └───────────┬───────────────────┘  │
                    │                        │ emit                 │
                    │            ┌───────────▼───────────────────┐  │
                    │            │          event bus            │  │
                    │            └──┬────────────┬───────────────┘  │
                    └───────────────│────────────│──────────────────┘
                          ┌─────────▼──┐   ┌─────▼────────┐
                          │ TTY render │   │ SSE render   │  (phase 2)
                          └────────────┘   └──────────────┘
                          ┌────────────┐   ┌──────────────┐
                          │  report    │   │    state     │
                          │ summary/   │   │ run-state    │
                          │ artifacts  │   │ (filesystem) │
                          └────────────┘   └──────────────┘
       cross-cutting: expr (expression engine) · secrets (masking) · events (types)
```

### 8.2 Go package layout

```
loom/
  cmd/loom/                 main; mode dispatch (default cli)
  internal/
    schema/                 YAML types, unmarshal, validate
    compile/                schema → IR  (THE single parser; future: parsers/*)
    ir/                     DAG, StepNode, edges, topo order
    engine/                 scheduler, run lifecycle, orchestration
    actions/
      action.go             Action interface, registry
      run.go http.go template.go prompt.go assert.go summary.go upload.go
    expr/                   ${{ }} evaluation (wraps expr-lang/expr)
    events/                 event types + bus
    render/
      tty/                  live CLI renderer
      sse/                  server renderer (phase 2)
    report/                 summary accumulation + artifact collection
    state/                  run-state persistence (filesystem)
    secrets/                secret registry + masking
    workspace/              per-run workspace dir mgmt
  server/                   phase 2: REST API + embedded SPA
  pkg/loom/                 public embedding API (optional)
```

### 8.3 The Action interface

The single extension point. Every built-in implements it; a future custom-action mechanism would too.

```go
type Action interface {
    // Type returns the schema key that selects this action ("run", "http", ...).
    Type() string
    // Validate checks the step's action config statically.
    Validate(cfg StepConfig) error
    // Run executes and returns named outputs.
    Run(ctx context.Context, sc *StepContext) (Outputs, error)
}

type StepContext struct {
    StepID   string
    Config   StepConfig           // parsed action config for this step
    Inputs   map[string]any       // resolved workflow inputs
    Steps    map[string]StepResult// prior step outputs
    Env      map[string]string    // merged workflow+step env (already expr-resolved)
    Workdir  string               // run workspace
    Secrets  *secrets.Registry    // for masking + registration
    Emit     func(events.Event)   // stream logs/events
    Expr     *expr.Evaluator      // for actions that resolve expressions internally
}

type Outputs map[string]string
```

`Emit` is how every action streams live output through the one event bus; `render` and `report` are just subscribers. This single seam is what lets the CLI and the future UI share 100% of execution and differ only in presentation.

---

## 9. Execution model and lifecycle

A run proceeds through fixed phases:

1. **Load & validate.** Parse YAML into `schema`, run all validation rules. Abort on any error.
2. **Resolve inputs.** Merge from flags → input file → env → interactive prompt (TTY only). Coerce/validate types. Register secret inputs with the masking registry.
3. **Compile to IR.** Build the DAG: nodes = steps, edges = `needs` (or implicit sequential order). Detect cycles. Compute execution order (topological; v1 executes sequentially in that order — parallelism is a later optimisation).
4. **Execute steps.** For each node in order:
   - Evaluate `if:`; if false, mark **skipped**, emit `StepFinished{skipped}`, continue.
   - Resolve `env:` and action config expressions against the current context.
   - Dispatch to the action's `Run`, streaming events.
   - On success, parse outputs, record to state, expose as `steps.<id>.outputs`.
   - On error: if `continue-on-error`, mark **failed-continued**; else mark **failed**, halt remaining steps (except always-run reporting steps).
5. **Finalise.** Run accumulated `summary` and `upload` actions, render the final report, persist state, compute aggregate status, exit with the corresponding code.

Aggregate exit codes: `0` success, `1` a step failed, `2` validation error, `3` input resolution error.

### Step status values

`pending → running → success | failed | failed-continued | skipped | timed-out`

---

## 10. Expression engine

`${{ ... }}` expressions are resolved by the engine, not by any shell. The evaluation context:

| Namespace | Contents |
|---|---|
| `inputs.<name>` | Resolved workflow inputs |
| `steps.<id>.outputs.<key>` | Prior step outputs |
| `steps.<id>.status` | Prior step status |
| `env.<KEY>` | Environment values |
| `secrets.<name>` | Secret values (masked in any log rendering) |
| `run.id`, `run.workspace` | Run metadata |

Supported operations (kept deliberately small): comparison (`==`, `!=`, `<`, `>`), boolean (`&&`, `||`, `!`), and a handful of functions — `contains()`, `startsWith()`, `endsWith()`, `default(v, fallback)`, `fromJSON()`, `toJSON()`. Inside an `http` step, an additional `response` object is in scope.

**Implementation:** wrap [`expr-lang/expr`](https://github.com/expr-lang/expr) — a mature, sandboxed Go expression evaluator — mapping the namespaces above into its environment. This avoids hand-rolling a parser and keeps the surface safe. The engine does **not** embed a general scripting language; anything beyond simple predicates and field access belongs in `run:`.

---

## 11. Data flow between steps

Three channels, by design:

1. **Outputs** — the primary channel. `run` steps write to `$LOOM_OUTPUT`; `http`/`template` steps declare an `outputs:` mapping. All become `steps.<id>.outputs.<key>`.
2. **The workspace filesystem** — steps share a directory. One step writes `./out/report.html`; a later `template` or `upload` reads it. This is how binary/large data moves between steps without stuffing it into string outputs.
3. **Environment** — workflow/step `env:` and secrets, injected into `run` steps and available to expressions.

The `http`-then-`run` handoff (transform a response body) uses channel 1: `http` extracts what it trivially can, or a following `run` pulls the raw response through `curl` and reshapes with `jq`, writing the result to `$LOOM_OUTPUT` for the next step.

---

## 12. Output, logging and reporting

### Event stream

Execution emits a typed event stream; everything user-facing is a subscriber.

```
RunStarted{workflow, run_id}
StepStarted{step_id, name}
StepLog{step_id, stream: stdout|stderr, line}
StepOutput{step_id, key, value}
StepFinished{step_id, status, duration}
SummaryEmitted{markdown}
ArtifactUploaded{name, path, size}
RunFinished{status, duration}
```

### Renderers

- **TTY renderer (v1):** grouped, live per-step output with status glyphs (`✓ ✗ ⊘`), timing, and a final summary block. Secrets masked. Respects `--json` for machine-readable event output (for piping into other tools).
- **SSE/WebSocket renderer (phase 2):** serialises the identical events to the browser. No divergence in what CLI vs UI users see.

### Reporting

The `report` package accumulates `SummaryEmitted` markdown and `ArtifactUploaded` entries. At run end it:
- prints the assembled summary to console,
- writes a self-contained HTML report to the workspace,
- lists artifact locations (CLI) or exposes download links (server).

---

## 13. State and idempotency

State is **filesystem-only** — no database. This is a core differentiator and is deliberately preserved.

```
./.loom/runs/<run-id>/
  state.json        # step statuses + outputs, for resume/inspection
  workspace/        # shared step working dir
  artifacts/        # collected uploads
  report.html       # rendered report
```

**Idempotency** is a first-class concern for onboarding/getting-started helpers, which are re-run constantly after partial failures. Two mechanisms:

1. **`if:` on prior state** — express "create only if absent":
   ```yaml
   - id: lookup
     http: { GET: "${{ inputs.env_url }}/rest/partners?name=${{ inputs.partner_name }}" }
     outputs: { exists: "${{ response.body.total > 0 }}" }
   - id: create
     if: ${{ !steps.lookup.outputs.exists }}
     http: { POST: "...", body: { ... } }
   ```
2. **Resume** (phase 2) — `loom run --resume <run-id>` skips steps already recorded `success` in `state.json`, re-executing from the first incomplete step.

The engine does not attempt to make actions *automatically* idempotent — that is the workflow author's responsibility, expressed through `if:` and lookups, which keeps the engine simple and honest.

---

## 14. CLI design

Default mode is CLI. `loom` with a workflow argument runs it.

```
loom run <workflow.yml> [flags]      Execute a workflow (default verb)
  --input k=v            supply an input (repeatable)
  --input-file <f>       supply inputs from a YAML/JSON file
  --var k=v              override workflow env (repeatable)
  --dry-run              compile, validate, print plan; execute nothing
  --json                 emit the event stream as JSON (machine-readable)
  --no-color             plain output
  --resume <run-id>      (phase 2) resume a prior run
  --strict               treat inline expr-in-run as an error

loom validate <workflow.yml>         Static validation, no execution
loom list                            Discover workflows in ./workflows
loom server [--port 8080] [--dir]    (phase 2) start server + UI
loom version
```

Mode dispatch in `cmd/loom`: absence of the `server` subcommand ⇒ CLI. This satisfies the "default mode CLI, server via parameter" requirement.

---

## 15. Server and UI mode

**Phase 2.** The server is a *second front-end over the identical engine core* — it adds no execution capability, only presentation and triggering.

- **API:** `POST /runs` (start a run of a *catalogued* workflow with inputs), `GET /runs/:id/events` (SSE stream of the event bus), `GET /runs/:id/artifacts/*`, `GET /workflows` (the curated catalogue).
- **UI:** a small SPA embedded via Go `embed` — workflow catalogue, an input form generated from each workflow's `inputs` schema, a live log view fed by SSE, and a report/artifacts panel. No external asset hosting; still one binary.
- **Catalogue:** server mode serves workflows from a configured, version-controlled directory only. Users pick a workflow and provide inputs; they cannot submit arbitrary YAML. This is the security linchpin (§16).
- **Auth:** internal SSO/token; TLS terminated at or before the binary.

Because both front-ends drive the same event bus, a run looks identical whether launched from a terminal or a browser.

---

## 16. Security model

| Concern | Treatment |
|---|---|
| **Arbitrary shell execution** | Accepted for CLI (operator already has shell; workflows are PR-reviewed code). For server mode, only curated/version-controlled workflows run — no arbitrary YAML submission. |
| **Script injection** (`${{ }}` into `run:`) | Env-var passing is the default and documented pattern; direct interpolation warns and shell-quotes; `--strict` makes it an error (§7.3). |
| **Secret handling** | Secret inputs and `mask:` values registered with a masking registry; redacted in every rendered/persisted log line before display. Secrets never written to `state.json` in plaintext. |
| **Artifact path traversal** | `upload`/`template` `dest` paths validated to resolve *inside* the run workspace; `..` escapes rejected. |
| **Server privilege escalation** | Server runs workflows as its own user; because only reviewed workflows are catalogued, the trigger surface cannot inject new code. Input values are passed as data (env/args), never as workflow structure. |
| **Transport** | Server mode requires TLS; tokens/SSO for auth; per-workflow authorisation possible in a later RBAC phase. |
| **Supply chain** | Single statically-linked binary; dependencies minimal and vendored; reproducible builds. |

The guiding principle: **the trust boundary is "who can author a catalogued workflow," identical to GitHub Actions' "who can edit `.github/workflows`."** Everything else is defence-in-depth around that boundary.

---

## 17. Extensibility

- **New built-in actions** implement the `Action` interface and register themselves. This is how the catalogue grows without touching the engine.
- **IR discipline for future multi-format.** Even though v1 ships one parser, steps compile through `compile → ir`. If multi-format ingestion is ever justified, it is a new package under `parsers/` producing the same IR — the scheduler, actions, events, and renderers are untouched. This is the near-zero-cost future-proofing noted in the non-goals: keep the seam, don't build the parsers you don't need.
- **Custom logic today** escapes to `run:`; there is intentionally no third-party plugin registry in v1.
- **Remote artifact stores** (S3, JFrog Artifactory) slot behind the `upload`/`report` interface in a later phase without schema changes.

---

## 18. Distribution and operations

- **Single static binary** per platform (`linux/amd64`, `linux/arm64`, `darwin/arm64`, `windows/amd64`), built with `CGO_ENABLED=0`. `go install` and release archives.
- **No runtime dependencies** beyond the host shell and whatever tools individual workflows call.
- **Config:** convention-over-configuration; optional `.loomrc` for defaults (default env, catalogue dir, server port).
- **Versioning:** semantic; the workflow schema carries no version field in v1 but the engine records the schema shape it supports, leaving room for a `schemaVersion` if breaking changes arrive.

---

## 19. Roadmap and phasing

**Phase 1 — v0.1 (CLI core, the proof).**
Schema + validate; `compile → IR`; sequential executor; actions `run`, `http`, `template`, `assert`, `summary`, `upload`; expression engine; TTY renderer; filesystem run-state; the CLI. **Flagship deliverable:** the webMethods getting-started helper (Appendix A) running end-to-end and emitting an HTML report.

**Phase 2 — v0.2 (interactivity + server).**
`prompt` action; DAG parallelism and `needs`; `--resume`; server mode with embedded UI and SSE; workflow catalogue/discovery.

**Phase 3 — v0.3+ (breadth).**
Remote artifact stores (S3/JFrog); scheduled/triggered runs; RBAC on the server; optional containerised executor backend; *if justified*, a second input parser via the IR seam.

---

## 20. Open questions

1. **Expression library** — adopt `expr-lang/expr`, or hand-roll a minimal evaluator for tighter control over the `${{ }}` surface? (Leaning `expr-lang/expr`.)
2. **Windows support scope** — first-class, or Linux/macOS-first with best-effort Windows? webMethods servers are predominantly Linux; a Linux-first stance may be pragmatic for v1.
3. **How much idempotency help** to bake in — stay fully author-driven (`if:` + lookups), or add opt-in "ensure" semantics to `http` (check-then-create) as sugar?
4. **Server auth** — reuse internal IBM SSO directly, or a pluggable auth interface with SSO as the first implementation?
5. **DAG vs sequential in v1** — is sequential-only acceptable for the flagship use cases (it is for onboarding/diagnostics), deferring parallelism cleanly to Phase 2?

---

## Appendix A — Flagship workflow: webMethods getting-started helper

```yaml
name: b2b-getting-started
description: Validate a webMethods B2B/TN environment and onboard a sample partner.

requires: [curl, jq]

inputs:
  env_url:      { description: "TN base URL", required: true }
  api_token:    { description: "API bearer token", required: true, secret: true }
  partner_name: { description: "Sample partner name", default: "Acme Corp" }

defaults:
  http:
    timeout: 30s
    headers:
      Authorization: "Bearer ${{ inputs.api_token }}"
      Content-Type: application/json

steps:
  - id: health
    name: Check B2B environment health
    http: { GET: "${{ inputs.env_url }}/rest/health" }
    assert: response.status == 200
    outputs: { status: "${{ response.body.status }}" }

  - id: lookup
    name: Look up existing partner (idempotency)
    http:
      GET: "${{ inputs.env_url }}/rest/partners?name=${{ inputs.partner_name }}"
    outputs: { exists: "${{ response.body.total > 0 }}" }

  - id: create
    name: Create trading partner if absent
    if: ${{ !steps.lookup.outputs.exists }}
    http:
      POST: "${{ inputs.env_url }}/rest/partners"
      body: { name: "${{ inputs.partner_name }}" }
    outputs: { id: "${{ response.body.partnerId }}" }

  - id: resolve-id
    name: Resolve partner id (created or existing)
    run: |
      if [ -n "$CREATED_ID" ]; then
        echo "id=$CREATED_ID" >> "$LOOM_OUTPUT"
      else
        id=$(curl -sf "$ENV_URL/rest/partners?name=$NAME" \
             -H "Authorization: Bearer $TOKEN" | jq -r '.partners[0].id')
        echo "id=$id" >> "$LOOM_OUTPUT"
      fi
    env:
      CREATED_ID: "${{ steps.create.outputs.id }}"
      ENV_URL:    "${{ inputs.env_url }}"
      NAME:       "${{ inputs.partner_name }}"
      TOKEN:      "${{ inputs.api_token }}"

  - id: sample
    name: Submit sample X12 850 and verify flow
    run: |
      doc_id=$(./scripts/submit-sample.sh "$PARTNER_ID" | jq -r '.documentId')
      echo "doc_id=$doc_id" >> "$LOOM_OUTPUT"
    env:
      PARTNER_ID: "${{ steps.resolve-id.outputs.id }}"

  - id: report
    name: Render onboarding report
    template:
      src: ./templates/onboarding-report.html.tmpl
      dest: ./out/onboarding-report.html
      vars:
        env: "${{ inputs.env_url }}"
        partner: "${{ inputs.partner_name }}"
        partner_id: "${{ steps.resolve-id.outputs.id }}"
        doc_id: "${{ steps.sample.outputs.doc_id }}"

  - summary: |
      ## ✅ B2B onboarding complete
      - **Environment:** ${{ inputs.env_url }} (health: ${{ steps.health.outputs.status }})
      - **Partner:** ${{ inputs.partner_name }} (`${{ steps.resolve-id.outputs.id }}`)
      - **Sample document:** `${{ steps.sample.outputs.doc_id }}`

  - upload:
      path: ./out/onboarding-report.html
      name: onboarding-report
```

This one workflow exercises every core mechanism: declarative `http`, idempotent `if:`, the `http`→`run`/`jq` transformation handoff (the exact reshape case), env-var-safe secret passing, `template` generation, `summary`, and `upload`.

---

## Appendix B — Comparison with existing tools

| Capability | **Loom** | GitHub Actions | nektos/act | Dagger | Windmill |
|---|---|---|---|---|---|
| Single self-contained binary | ✅ | — (hosted) | ✅ | ⚠️ needs container runtime | ❌ server+worker+Postgres |
| No database required | ✅ | n/a | ✅ | ✅ | ❌ Postgres |
| No container runtime required | ✅ | n/a | ❌ Docker | ❌ BuildKit | ⚠️ |
| CLI-first, default CLI | ✅ | ❌ | ✅ | ✅ | ❌ UI-first |
| Optional server + UI | ✅ (phase 2) | n/a | ❌ | ❌ | ✅ |
| Declarative YAML workflows | ✅ | ✅ | ✅ (GHA only) | ❌ SDK/code | ⚠️ YAML or low-code |
| Shell escape hatch (`run:`) | ✅ | ✅ | ✅ | via code | ✅ |
| First-class summary + artifacts | ✅ | ⚠️ (summary + artifacts, CI-shaped) | ⚠️ | ⚠️ | ✅ |
| Positioned as | runbook/utility engine | CI/CD | GHA local runner | programmable CI | internal-tools platform |

Loom's niche is the intersection no incumbent occupies: *single binary, no DB, no container requirement, CLI-first with optional UI, purpose-built for operational runbooks rather than software delivery.*
```
