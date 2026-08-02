# Weftly

A single-binary, no-database, no-container **workflow / runbook engine**
for internal operational procedures — onboarding a partner, running a
diagnostic, generating a config bundle, checking a migration.

Weftly turns those procedures into versioned YAML files that stream live
logs, emit a summary + HTML report, and exit with a status code. It is
deliberately **not** a CI/CD replacement (see `spec.md` §2).

The CLI, the DAG scheduler, workflow composition (`include:`), typed
inputs, and the REST + SSE + UI server all ship today. See
[docs/FEATURES.md](docs/FEATURES.md) for the complete verified feature
record — including an explicit list of what is **not** implemented.

## Install / build

Requires Go 1.25+. The build is CGO-free and produces a static binary.

```
CGO_ENABLED=0 go build -o weftly ./cmd/weftly
./weftly version
```

## Quickstart

Validate a workflow without running it:

```
weftly validate workflows/petclinic-onboarding.yml
```

Run the flagship example against any [Spring PetClinic REST](https://github.com/spring-petclinic/spring-petclinic-rest)-shaped endpoint:

```
weftly run workflows/petclinic-onboarding.yml \
  --input env_url=http://localhost:9966/petclinic \
  --input api_key=$PETCLINIC_KEY \
  --input owner_last=Doe
```

The api key is registered as a secret and masked in every log line and
persisted state file. Run outputs land under `./.weftly/runs/<run-id>/`:

```
.weftly/runs/<run-id>/
  state.json     # machine-readable run state (statuses, outputs)
  report.html    # self-contained HTML report (summary + artifacts)
  workspace/     # per-run shared step working directory
  artifacts/     # files collected by `upload:`
```

## Writing a workflow

```yaml
name: my-workflow
description: What this does.

requires: [curl, jq]              # optional PATH preflight

inputs:
  env_url:
    required: true
  api_token:
    secret: true

defaults:
  http:
    timeout: 30s
    headers:
      Authorization: "Bearer ${{ inputs.api_token }}"
      Content-Type: application/json

steps:
  - id: health
    http: { GET: "${{ inputs.env_url }}/rest/health" }
    assert: response.status == 200
    outputs: { status: "${{ response.body.status }}" }

  - id: greet
    run: |
      echo "hello $NAME"
      echo "greeting=hi-$NAME" >> "$WEFTLY_OUTPUT"
    env:
      NAME: "${{ inputs.name }}"           # SAFE — env pass, not interp

  - summary: |
      ## Done — health = ${{ steps.health.outputs.status }}

  - upload:
      path: ./out/report.html
      name: report
```

### Step ids

Match `[a-z0-9_]+`. **Underscores only — no hyphens.** A hyphen in a
step id (`resolve-id`) parses as subtraction inside an expression
(`steps.resolve - id.outputs.x`) and cannot be referenced.

### Actions

| Action     | Purpose                                                     |
|------------|-------------------------------------------------------------|
| `run`      | Shell escape hatch. Values enter via `env:`; outputs leave via `$WEFTLY_OUTPUT` (`key=value` lines or `KEY<<DELIM ... DELIM` heredocs). Non-zero exit → step fails, outputs are discarded. |
| `http`     | Method-as-key (`GET:`, `POST:`, …). JSON bodies preserve types across `${{ }}`. Inline `assert:` runs against the response. `outputs: { k: "${{ response.body.field }}" }` extracts fields. |
| `template` | Go `text/template`. Either `src:` (a file path) or `inline:`. `dest:` must resolve inside the workspace. |
| `assert`   | Standalone boolean checkpoint. |
| `summary`  | Emits markdown into the final report. |
| `upload`   | Copies a workspace file/glob into `./.weftly/runs/<id>/artifacts/`. |
| `prompt`   | Interactive input: `text`, `password`, `confirm`, `select`. `--yes` auto-answers every `confirm`. |
| `wait`     | Polls until a condition holds or a time budget expires. |
| `parse`    | Extracts structure from text (JSON flattening, regex named groups) into step outputs. |
| `notify`   | POSTs a Slack-shaped or fully custom payload to a webhook. Non-2xx fails the step. |
| `include`  | Calls another workflow as a step, passing inputs via `with:` / `with_if_set:` and consuming its declared `outputs:`. See [docs/FEATURES.md](docs/FEATURES.md#4-workflow-composition-include). |

### Typed inputs and presets

```yaml
inputs:
  domain: { type: enum, values: [retail, healthcare], default: retail }
  cases:  { type: int, min: 100, max: 50000, default: 2500 }
  token:  { type: string, secret: true, min_length: 20 }

presets:
  qa_exceptions:
    description: Small corpus weighted toward error paths
    values: { domain: healthcare, cases: 500 }
```

Types: `string` (the default when `type:` is omitted), `int`, `number`,
`bool`, `enum`, `duration`, `json`, `path`, `list`. Coercion is strict —
`3.0` is not an `int`. Every bad field in one submission is reported
together, and an enum near-miss gets a did-you-mean.

A constraint violation on a `secret:` input reports the constraint and
never the value.

`weftly describe <workflow.yml>` prints the whole contract. Apply a
preset with `--preset <name>`; `--input` still wins over it.

### Teardown

`cleanup:` is **run-level** — it fires once after the whole graph,
whatever the outcome. `finally:` is **scope-level** — it belongs to the
workflow that declares it, so an included fragment tears down only what
it created, without knowing anything about its caller. Nested fragments
tear down innermost-first, and teardown steps are exempt from the
cascade-skip that stops ordinary downstream work after a failure.

Inside a fragment's `finally:`, `success()` / `failure()` report *that
fragment's* status, not the run's.

### Library fragments

`library: true` marks a workflow that may only be *included*, never run
on its own. It is excluded from the served catalogue, rejected as a
`POST /runs` target, and rejected when a schedule names it — but stays
freely includable.

This is an authorisation control. Without it, every fragment in a
toolkit is an ordinary catalogue entry that any principal holding
`workflows: "*"` can trigger directly or schedule, despite being written
to run only as part of a caller that supplies its inputs.

A complete, verified feature record — including what is **not**
implemented — lives in [docs/FEATURES.md](docs/FEATURES.md).

### Expressions

`${{ ... }}` spans are evaluated by [expr-lang/expr](https://github.com/expr-lang/expr).
Namespaces: `inputs.<name>`, `steps.<id>.outputs.<key>`,
`steps.<id>.status`, `env.<KEY>`, `secrets.<name>`,
`run.{id,workspace,status,cancelled}`, `each.{value,index}` (inside
`for-each`), `workflow.dir`, `workspace.dir`, and (inside `http`)
`response.{status,headers,body,raw}`.

Status functions `success()`, `failure()`, `always()`, and `cancelled()`
are available, and are what make `cleanup:` gates work.

`workflow.dir` is the directory of the YAML file that authored the step —
where the *code* lives, read-only and shared by every run. `workspace.dir`
is that step's working directory — where this run's *data* goes, writable
and per-scope. Reach a library's bundled assets with the former, place
output with the latter.

Helpers registered by weftly: `default(v, fb)`, `fromJSON(s)`,
`toJSON(v)`, `urlquery(s)`. String ops are expr-native operators:
`s contains "x"`, `s startsWith "x"`, `s endsWith "x"`.

### Security posture (spec §16)

- **Env pass, not interpolation.** Documented pattern is to feed values
  into `run:` bodies via `env:`. Inline `${{ }}` in a `run:` body warns
  by default and is a hard error under `--strict`.
- **Outputs come from `$WEFTLY_OUTPUT`, never stdout.** Stdout is logs.
- **Secrets are masked at the emit boundary.** Registered on secret
  input resolution; never written to `state.json` in the clear.
- **Path traversal is rejected** for `template dest:` and `upload path:`
  — both resolved via `workspace.SafeJoin`.
- **`type: path` inputs are confined** to the run workspace or the
  workflow's own directory tree. `must_exist:` probes through
  `os.Root`, so the check cannot traverse or follow a symlink out of
  the permitted root even if the surrounding logic were wrong.
- **Step-level `include:` is confined** to `--include-root` (defaulting
  to `--dir`); symlinks are resolved before the containment test, and
  absolute paths and URLs are rejected outright.
- **`library: true` fragments are not runnable** — excluded from the
  catalogue, rejected as a `POST /runs` target, and rejected when a
  schedule names them, so an include-only fragment can't be triggered
  directly by a principal holding `workflows: "*"`.
- **Presets may not carry secrets.** A preset supplying a
  `secret: true` input is a compile-time error — presets are committed
  YAML exposed through `GET /workflows/{id}`.
- **Constraint errors on a secret never echo the value**, and suppress
  the did-you-mean suggestion, which would otherwise leak a credential
  a character at a time.

## CLI

```
weftly run <workflow.yml> [flags]      Execute a workflow (default verb)
  --input k=v            supply an input (repeatable)
  --input-file <f>       supply inputs from a YAML/JSON file
  --var k=v              override workflow env (repeatable)
  --dry-run              compile, validate, print plan; execute nothing
  --json                 emit the event stream as JSON
  --no-color             plain output
  --strict               inline ${{ }} in run: bodies is an error
  -y, --yes              auto-answer 'yes' to every prompt(type:confirm)
  -p, --parallel N       max concurrent steps (default 4; needs edges honored)
      --resume <run-id>  resume a prior run; skips successful steps and
                         replays their outputs into downstream steps
  --preset <name>        apply a named preset from `presets:` (--input wins)
  --ci                   GitHub-Actions ::group:: markers, no colour
  --otel-endpoint <url>  OTLP/HTTP endpoint; exports run + step spans

weftly validate <workflow.yml>         Static validation, no execution
weftly describe <workflow.yml>         Print inputs (type, constraints,
                                       default, required/secret) and presets
weftly list                            Discover workflows in ./workflows
weftly init [name]                     Scaffold a starter workflow
  -o, --out <path>       write to a path instead of stdout
weftly fmt <workflow.yml>              Canonical formatting (idempotent)
weftly diff <a.yml> <b.yml>            Structural diff; non-zero on difference
weftly mcp                             Serve the catalogue over the Model
                                       Context Protocol on stdio
weftly import-gha <path-or-->          Convert a GitHub Actions workflow to
                                       weftly YAML (skips uses:, matrix:, etc.
                                       with a note per dropped construct)
  --job <id>             pick a specific job when the file has multiple
  -o, --out <path>       write the converted YAML instead of stdout
weftly server                          Start the REST + SSE + UI server
  --addr :8080           listen address
  --dir  ./workflows     catalogue directory (only these workflows run)
  --include-root <dir>   widen the boundary step-level `include:` may reach
                         (defaults to --dir; set to a project root when
                         workflows/ and lib/ are siblings)
  --runs-dir ./.weftly   parent directory for per-run state
  --token ...            single bearer token (or $WEFTLY_TOKEN)
  --auth-file <path>     multi-token RBAC file (supersedes --token)
  --schedules <path>     schedules.yaml — enables cron-driven runs
  --s3-endpoint / --s3-bucket / --s3-prefix / --s3-region
  --s3-access-key / --s3-secret-key      mirror artifacts to S3-compatible store
  --s3-plaintext                          talk http to the S3 endpoint (dev-only)
  --audit-file <path>    append-only JSON-lines log of mutating requests
  --otel-endpoint <url>  OTLP/HTTP endpoint; exports run + step spans
weftly version
```

Exit codes: `0` success, `1` a step failed, `2` validation error,
`3` input resolution error.

## Server + UI (Phase 2)

`weftly server` starts a small self-contained HTTP server that catalogues
workflows from a directory, runs them on request, and streams live logs
to a built-in SPA. The trust boundary is *"who commits to the
catalogue"* — no arbitrary YAML is ever accepted over the wire (spec
§16).

```
# The release tarball already ships a ready-to-use ./workflows/ dir
# containing hello.yml + petclinic-onboarding.yml, so this is enough:
export WEFTLY_TOKEN=$(head -c 32 /dev/urandom | base64)
weftly server --dir ./workflows --addr :8080
```

Open http://localhost:8080/ — the SPA prompts for the token on first
API call and stashes it in `localStorage`. Curl-only usage works too:

```
curl -sH "Authorization: Bearer $WEFTLY_TOKEN" http://localhost:8080/workflows | jq
curl -sH "Authorization: Bearer $WEFTLY_TOKEN" -H "Content-Type: application/json" \
     -d '{"workflow":"petclinic-onboarding","inputs":{"env_url":"...","api_key":"..."}}' \
     http://localhost:8080/runs
```

Endpoints:

| Method + path | Purpose |
|---|---|
| `GET /healthz` | Liveness (unauthenticated) |
| `GET /workflows` | Catalogue list |
| `GET /workflows/{id}` | Full metadata + inputs schema |
| `POST /runs` | Start a run (`{workflow, inputs}` body) |
| `GET /runs` | List every persisted run (optional `?workflow=<id>`) |
| `GET /runs/{id}` | Serve the run's `state.json` |
| `DELETE /runs/{id}` | Cancel an in-flight run (idempotent) |
| `GET /runs/{id}/events` | SSE event stream (replay + live) |
| `GET /runs/{id}/artifacts/{name}` | Download a collected artifact (local first, S3 fallback) |
| `GET /schedules` / `GET /schedules/{id}` | List / detail configured schedules |
| `POST /schedules/{id}/trigger` | Fire a schedule immediately |
| `POST /reload` | Re-scan the catalogue + schedules (SIGHUP does the same) |

## Phase 3 features

### RBAC — multi-token principals + workflow ACLs

Point `weftly server --auth-file weftly.yaml` at a file like this:

```yaml
tokens:
  "opaque-token-alice-32-plus-chars":
    name: alice
    roles: [ops, admin]
  "opaque-token-bob-32-plus-chars":
    name: bob
    roles: [dev]
roles:
  admin:
    admin: true          # unlocks POST /reload
    workflows: "*"
  ops:
    workflows: "*"
  dev:
    workflows: [petclinic-onboarding, dev-smoke]
```

Callers see only workflows and runs their allowlist permits — the
catalogue endpoint, the runs listing, cancel, and schedule endpoints
all filter identically. Token compare is constant-time; token entries
under 12 chars are rejected at load time.

### Scheduled runs — `--schedules schedules.yaml`

Cron-driven dispatch of catalogue workflows. Bad crons on one entry
surface as `parse_error` on that entry, not a whole-file reject.

```yaml
schedules:
  - id: nightly-onboarding
    workflow: petclinic-onboarding
    cron: "0 2 * * *"        # 5-field cron, or one of @hourly/@daily/@weekly/@monthly/@yearly
    tz: America/Los_Angeles
    inputs:
      env_url: https://petclinic.example.com
      api_key: ${WEFTLY_PETCLINIC_KEY}
  - id: on-demand
    workflow: dev-smoke
    cron: "@yearly"          # effectively manual — trigger via POST /schedules/on-demand/trigger
    disabled: false
```

Reload with `POST /reload` or `SIGHUP`. The SPA has a Schedules page
with a Trigger-now button per row.

### Container executor — `container:` on a step

Runs the shell inside `podman run` / `docker run` (podman preferred,
docker fallback). Workspace + script + `$WEFTLY_OUTPUT` bind-mounted;
env vars validated as POSIX identifiers before `-e`; `--network=none`
by default.

```yaml
steps:
  - id: audit
    container: alpine:3.19
    run: |
      apk add --no-cache jq >/dev/null
      echo "vuln_count=$(jq '.data | length' report.json)" >> "$WEFTLY_OUTPUT"
```

Neither engine on `$PATH` → the run errors immediately rather than
silently falling back to host exec.

### Remote artifact store — S3-compatible

Every `upload` action mirrors to the bucket in addition to writing
locally. `GET /runs/{id}/artifacts/{name}` transparently falls back to
the bucket when the local file is missing (retention pruned, node
replaced, ...).

```
weftly server \
  --dir ./workflows \
  --s3-endpoint s3.amazonaws.com \
  --s3-bucket weftly-artifacts \
  --s3-prefix prod/         # namespaces runs inside a shared bucket
```

Access + secret keys can come from `--s3-access-key / --s3-secret-key`
or `$WEFTLY_S3_ACCESS_KEY / $WEFTLY_S3_SECRET_KEY`.

### GitHub Actions ingestion — `weftly import-gha`

Compile-time seam: converts the supported subset of a GHA workflow to
weftly YAML. Not called at runtime — you review the converted file
and any translation notes, then drop it into a catalogue.

```
weftly import-gha .github/workflows/deploy.yml --job deploy > workflows/deploy.yml
```

Steps with `uses:` are skipped with a note (weftly doesn't run
marketplace actions). GHA-only expression helpers
(`success()`, `hashFiles()`, `fromJSON()`, ...) are copied verbatim
into `if:` and flagged. The emitted YAML is re-validated against
`schema.Validate` before being written, so translator bugs surface at
import time.

## Testing

```
go test ./...
```

The flagship end-to-end test spins up an in-process mock PetClinic
server (`internal/mockpetclinic`) and executes
`workflows/petclinic-onboarding.yml` against it. It skips automatically if
`curl` or `jq` isn't on `PATH`.

## Design

See [`spec.md`](spec.md) for the full design document (motivation,
non-goals, architecture, security model, roadmap). Phase 2 targets —
DAG parallelism, `--resume`, the `prompt` action, and the server + UI —
are called out in the code and are the reason `server/` and
`pkg/weftly/` exist today as placeholders.
