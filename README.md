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

Requires Go 1.23+. The build is CGO-free and produces a static binary.

```
CGO_ENABLED=0 go build -o weftly ./cmd/weftly
./weftly version
```

## Quickstart

Validate a workflow without running it:

```
weftly validate examples/b2b-getting-started.yml
```

Run the flagship example against your own Trading Networks environment
(or any REST endpoint that speaks the same shape):

```
weftly run examples/b2b-getting-started.yml \
  --input env_url=https://tn.example.com \
  --input api_token=$TN_TOKEN \
  --input partner_name="Acme Corp"
```

The token is registered as a secret and masked in every log line and
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

weftly validate <workflow.yml>         Static validation, no execution
weftly list                            Discover workflows in ./workflows
weftly version
```

Exit codes: `0` success, `1` a step failed, `2` validation error,
`3` input resolution error.

## Testing

```
go test ./...
```

The flagship end-to-end test spins up an in-process mock Trading
Networks server (`internal/mocktn`) and executes
`examples/b2b-getting-started.yml` against it. It skips automatically if
`curl` or `jq` isn't on `PATH`.

## Design

See [`spec.md`](spec.md) for the full design document (motivation,
non-goals, architecture, security model, roadmap). Phase 2 targets —
DAG parallelism, `--resume`, the `prompt` action, and the server + UI —
are called out in the code and are the reason `server/` and
`pkg/weftly/` exist today as placeholders.
