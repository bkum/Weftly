# Contributing to Weftly

Thanks for your interest. This document covers dev setup, the change
process, and the conventions the project follows.

## Getting set up

Requirements: **Go 1.23+**. No CGO, no external services, no Docker
required for the build or the test suite.

```
git clone https://github.com/bkum/weftly
cd weftly
CGO_ENABLED=0 go build ./cmd/weftly
go test ./...
```

The flagship end-to-end test spins up an in-process mock server, so it
runs in seconds. If `curl` or `jq` isn't on your `PATH`, the test skips
the shell-round-trip step automatically.

## Repository layout

See `spec.md` §8.2 for the design; the short version:

```
cmd/weftly/                — main; cobra CLI
internal/
  schema/    compile/  ir/       — parse → validate → executable graph
  engine/                          — run lifecycle
  actions/                         — run, http, template, assert, summary, upload
  expr/                            — expr-lang wrapper + interpolator
  events/    render/tty/           — event bus + renderers
  secrets/   workspace/            — masking + per-run dirs
  report/    state/                — HTML report + state.json
  mockpetclinic/                   — in-process mock for the flagship test
examples/                         — sample workflows
server/                           — Phase 2 placeholder
pkg/weftly/                       — public embedding API placeholder
```

## Making a change

1. **Open an issue first** for anything larger than a small bug or
   docs tweak. The `spec.md` non-goals list is load-bearing — please
   check it before proposing a feature that widens scope.
2. Branch from `main`. Small, focused commits with a clear subject
   line beat one large one; if you want to squash on merge, note that
   in the PR body.
3. `go vet ./...`, `gofmt -w .`, and `go test ./...` must all be
   clean. CI runs the same commands plus `govulncheck` and CodeQL.
4. Every new action, expression helper, or renderer needs a
   table-driven test. Existing tests are the pattern.
5. Open a pull request against `main`. Fill in the PR template; a
   reviewer will pick it up.

## Coding conventions

- **Small packages with a single responsibility.** `internal/expr`
  wraps expressions; `internal/render/tty` renders. Cross-package
  helpers are a smell.
- **`context.Context` first.** Every long-running function takes a
  context and honors cancellation.
- **No globals.** Use dependency injection; the action registry is
  the only exception, and it exists to keep `Register` local to each
  action file.
- **Small comments explaining WHY, not WHAT.** Comment when a reader
  cannot guess the constraint from the code (`// longest-first so
  overlapping registrations don't leave partial masks`), not when the
  code is self-evident.
- **No stdout writes from actions.** All output goes through
  `sc.Emit`. The renderer is the only thing that touches
  `os.Stdout`.
- **Secrets never leak.** Anything that comes from a `secret: true`
  input goes through `secrets.Registry.Mask` before it lands in a
  log line, `state.json`, or `report.html`.

## The Action interface

Every built-in implements:

```go
type Action interface {
    Type() string
    Validate(cfg StepConfig) error
    Run(ctx context.Context, sc *StepContext) (Outputs, error)
}
```

and registers itself in an `init()` block. See any file under
`internal/actions/` for the pattern.

## Commit message format

We do not enforce Conventional Commits, but PR titles should be short
and imperative and describe the *why*:

- `run: kill process group on timeout`
- `docs: document that step ids cannot contain hyphens`

## Releasing

Releases are tag-driven. A maintainer pushes a `vX.Y.Z` tag on `main`;
GitHub Actions builds cross-platform binaries via GoReleaser and cuts
a Release with checksums. See `.github/workflows/release.yml`.

## Code of conduct

By participating you agree to abide by our
[Code of Conduct](CODE_OF_CONDUCT.md).

## Reporting security issues

Please **do not** open a public issue for security problems. Follow
the private-disclosure process in [SECURITY.md](SECURITY.md).
