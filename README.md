# Weftly

A single-binary, no-database, no-container **workflow / runbook engine**
for internal operational procedures — onboarding a partner, running a
diagnostic, generating a config bundle, checking a migration.

Weftly turns those procedures into versioned YAML files that stream live
logs, emit a summary + HTML report, and exit with a status code. It is
deliberately **not** a CI/CD replacement (see `spec.md` §2).

Phase 1 ships the CLI core: schema + validate, expression engine, event
bus + TTY renderer, filesystem run-state, and the built-in actions
`run`, `http`, `template`, `assert`, `summary`, `upload`. A `prompt`
stub reserves the action name; DAG parallelism, `--resume`, and the
optional server + UI (`spec.md` §15) are Phase 2.

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
| `prompt`   | Reserved. Interactive prompts are a Phase 2 feature. |

### Expressions

`${{ ... }}` spans are evaluated by [expr-lang/expr](https://github.com/expr-lang/expr).
Namespaces: `inputs.<name>`, `steps.<id>.outputs.<key>`,
`steps.<id>.status`, `env.<KEY>`, `secrets.<name>`, `run.{id,workspace}`,
and (inside `http`) `response.{status,headers,body,raw}`.

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

weftly validate <workflow.yml>         Static validation, no execution
weftly list                            Discover workflows in ./workflows
weftly import-gha <path-or-->          Convert a GitHub Actions workflow to
                                       weftly YAML (skips uses:, matrix:, etc.
                                       with a note per dropped construct)
  --job <id>             pick a specific job when the file has multiple
  -o, --out <path>       write the converted YAML instead of stdout
weftly server                          Start the REST + SSE + UI server
  --addr :8080           listen address
  --dir  ./workflows     catalogue directory (only these workflows run)
  --runs-dir ./.weftly   parent directory for per-run state
  --token ...            single bearer token (or $WEFTLY_TOKEN)
  --auth-file <path>     multi-token RBAC file (supersedes --token)
  --schedules <path>     schedules.yaml — enables cron-driven runs
  --s3-endpoint / --s3-bucket / --s3-prefix / --s3-region
  --s3-access-key / --s3-secret-key      mirror artifacts to S3-compatible store
  --s3-plaintext                          talk http to the S3 endpoint (dev-only)
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
