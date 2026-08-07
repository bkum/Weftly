# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
CGO_ENABLED=0 go build -o weftly ./cmd/weftly   # matches the release build
go test -race ./...                             # what CI runs
gofmt -l .                                      # must print nothing
go vet ./...
```

Run one test or one package:

```bash
go test -run TestIncludeExpandsChildStepsAndSurfacesOutputs ./internal/engine/
go test -race -count=1 ./internal/server/
```

Some tests skip rather than fail when a host tool is absent (`curl`, `jq`,
`podman`/`docker`). A "skipped" flagship or container test on a bare
machine is expected, not a regression.

`cmd/weftly` holds a subprocess-driven QA suite (`TestQA_*`) that builds
the real binary and drives it end to end. It is slower than the unit
packages and is the thing that catches "the shipped artifact is broken"
bugs the unit tests can't see.

## Architecture

One pipeline, and everything else hangs off it:

```
schema.Load ──► schema.Validate ──► compile.CompileWithOptions ──► engine.schedule ──► actions
   YAML            static rules          []ir.StepNode              DAG walk          Run(ctx, sc)
                                                                        │
                                                                        ▼
                                                                   events.Bus
                                                    ┌───────────────────┼───────────────────┐
                                              state.Writer          report.Report      renderers
                                              (state.json)          (report.html)    (TTY / JSON / SSE)
```

Load these together when changing behaviour: `internal/schema`,
`internal/compile`, `internal/ir`, `internal/engine`. A change to the
step model usually touches all four.

### The event bus is the only output channel

Actions never write to stdout. Everything user-visible goes through
`sc.Emit`, and every consumer — TTY renderer, `--json` renderer, SSE
stream, `state.json` writer, HTML report — is a bus subscriber. Adding
output means adding an event type or a subscriber, never a `fmt.Println`.

Subscriber **order is load-bearing**. `engine.Options.PostSubscribers`
registers after `state.Writer` and `report`, so the server's per-run
record can't let an SSE client observe `RunFinished` and then pivot to a
`GET /runs/{id}` that reads a `state.json` still being written.

### Two different features share the word `include`

- **Top-level `include: [files]`** — expanded by `schema.Load` at *parse*
  time. Prepends the included file's steps; its `name`/`inputs` are
  discarded. A step-library prelude.
- **Step-level `include: file` + `with:`** — expanded by
  `internal/compile` at *compile* time into a flat DAG. This is the
  parameterised call.

They are unrelated implementations. Check which one a bug report means
before touching either.

### Scopes: composition without rewriting expressions

Step-level includes attach an `ir.Scope` to each expanded node. At
runtime the engine resolves `inputs.*` and `steps.*` **through the scope
chain** — expression strings are never rewritten. That is deliberate: a
rewritten expression would make every error message and `--dry-run` plan
show text the author never wrote.

Consequences worth knowing before editing `internal/engine/engine.go`:

- A `with:` expression evaluates in the **parent** scope, which is why
  `with: { out: "${{ workspace.dir }}" }` means the *caller's* workspace
  and lets two children deliberately share a directory.
- Each scope gets its own workspace subdirectory, so one fragment
  included twice can't overwrite its own output.
- Artifacts from a scope are named `<prefix>__<file>` — **and the S3 key
  must match**. Qualifying one tier but not the other re-collides them in
  the bucket.

### Server reuses the engine, it does not reimplement it

`internal/server` calls the same `engine.Run` the CLI does. The SPA under
`internal/server/ui/` is `go:embed`ed — edit those files directly and
rebuild; there is no separate frontend build.

Two distinct boundaries that are easy to conflate:

- `--dir` — the **catalogue**: which workflows can be *run*.
- `--include-root` — the **trust boundary**: which files an `include:`
  may *reach*. Defaults to `--dir`.

### Path handling

`workspace.SafeJoin` for `template dest:` / `upload path:`; `type: path`
inputs are confined to the workspace or the workflow's tree and probe
existence through `os.Root`.

Two traps that have each caused a bug here:

- `filepath.EvalSymlinks` **fails outright** when the leaf doesn't exist
  — the normal case for an output path about to be created. Canonicalise
  the longest *existing* prefix instead.
- macOS temp dirs are `/var/...` symlinked to `/private/var/...`, so a
  canonical root compared against a lexical path looks like an escape.
  Canonicalise **both sides** before `filepath.Rel`.

## Conventions

Beyond `CONTRIBUTING.md`'s list, the ones that bite in review:

- **Comments explain WHY.** The codebase's comment style is a constraint
  or a rejected alternative, not a restatement of the line below.
- **Secrets never echo.** A constraint violation on a `secret: true`
  input reports the constraint only — no value, no did-you-mean, no
  allowed-value list. A suggestion leaks a credential a character at a
  time.
- **Step ids are `[a-z0-9_]+`.** Hyphens are rejected because
  `steps.resolve-id` parses as subtraction.
- **`weftly validate` errors carry file and line.** Add to
  `schema.Errors` rather than returning a bare `error`.

Actions self-register in `init()` and implement `Type` / `Validate` /
`Run`. Adding one means a new file in `internal/actions/` plus its key in
`actionKeys` (`internal/schema/types.go`).

## Documentation is tested

`internal/cli/docs_test.go` fails the build when a command or flag exists
but isn't in the README's `## CLI` block, or when stale roadmap language
reappears. Adding a flag means updating the README in the same change.

`docs/FEATURES.md` is the verified feature record, including an explicit
§10 of what is *not* implemented and §11 of deliberate non-goals. Keep it
accurate — it has been wrong before, and a wrong "not implemented" row
is worse than a missing one. Verify claims by running the binary rather
than by reading the code.

`spec.md` and `docs/design.md` are the original design documents and are
**older than the code** in places. Prefer `docs/FEATURES.md`, and prefer
the source over both.
