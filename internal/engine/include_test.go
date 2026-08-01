package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	// register built-in actions
	_ "github.com/bkum/weftly/internal/actions"

	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/schema"
)

// writeTwo writes a caller and a callee workflow into a scratch dir,
// returning the caller's path. Both use only the `run` action so the
// tests don't depend on curl/jq being on PATH.
func writeInclude(t *testing.T, caller, callee string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib.yml")
	if err := os.WriteFile(lib, []byte(callee), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	return main, lib
}

func collectEvents(bus *events.Bus) (*sync.Mutex, *[]events.Event) {
	var mu sync.Mutex
	var out []events.Event
	bus.Subscribe(func(e events.Event) {
		mu.Lock()
		out = append(out, e)
		mu.Unlock()
	})
	return &mu, &out
}

// TestIncludeExpandsChildStepsAndSurfacesOutputs is the M2 shape from
// spec §12: an include with a `with:` binding runs the child's steps,
// exposes its outputs to the parent, and everything lands in one run.
func TestIncludeExpandsChildStepsAndSurfacesOutputs(t *testing.T) {
	callee := `
name: greet-lib
inputs:
  who:
    description: name to greet
    required: true
steps:
  - id: build
    run: |
      echo "hi $NAME" > "$WEFTLY_OUTPUT.tmp"
      echo "greeting=hello-$NAME" >> "$WEFTLY_OUTPUT"
    env:
      NAME: "${{ inputs.who }}"
outputs:
  greeting: "${{ steps.build.outputs.greeting }}"
`
	caller := `
name: caller
inputs:
  target: { default: "world" }
steps:
  - id: sub
    include: lib.yml
    with:
      who: "${{ inputs.target }}"
  - id: check
    run: |
      echo "sub said: $G"
      if [ "$G" != "hello-world" ]; then
        echo "wrong greeting"; exit 1
      fi
    env:
      G: "${{ steps.sub.outputs.greeting }}"
`
	main, _ := writeInclude(t, caller, callee)
	wf, err := schema.Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatalf("validate: %v", err)
	}
	bus := events.NewBus()
	mu, evs := collectEvents(bus)
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(),
		Bus:     bus,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("want success, got %s", res.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	// Verify the child's step ran under a qualified id, and the check
	// step saw the include's output.
	var (
		sawChildBuild bool
		sawCheckOK    bool
	)
	for _, e := range *evs {
		if s, ok := e.(events.StepStarted); ok && s.StepID == "sub.build" {
			sawChildBuild = true
		}
		if l, ok := e.(events.StepLog); ok && l.StepID == "check" && strings.Contains(l.Line, "sub said: hello-world") {
			sawCheckOK = true
		}
	}
	if !sawChildBuild {
		t.Errorf("expected qualified child step id sub.build to appear in events")
	}
	if !sawCheckOK {
		t.Errorf("check step did not observe include's greeting output")
	}
}

func TestIncludeMissingRequiredInputFailsValidation(t *testing.T) {
	callee := `
name: lib
inputs:
  need_me: { required: true }
steps:
  - id: noop
    run: echo hi
`
	caller := `
name: caller
steps:
  - id: sub
    include: lib.yml
`
	main, _ := writeInclude(t, caller, callee)
	wf, err := schema.Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatalf("validate: %v", err)
	}
	_, err = engine.Run(context.Background(), wf, engine.Options{BaseDir: t.TempDir(), Bus: events.NewBus()})
	if err == nil || !strings.Contains(err.Error(), "need_me") {
		t.Fatalf("want error about missing required input need_me, got %v", err)
	}
}

func TestIncludeUnknownWithKeyFails(t *testing.T) {
	callee := `
name: lib
inputs:
  who: { default: nobody }
steps:
  - id: noop
    run: echo hi
`
	caller := `
name: caller
steps:
  - id: sub
    include: lib.yml
    with:
      typo: something
`
	main, _ := writeInclude(t, caller, callee)
	wf, _ := schema.Load(main)
	_, err := engine.Run(context.Background(), wf, engine.Options{BaseDir: t.TempDir(), Bus: events.NewBus()})
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("want error about unknown with: key 'typo', got %v", err)
	}
}

func TestIncludeCycleDetected(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yml")
	b := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(a, []byte(`
name: a
steps:
  - id: cycle
    include: b.yml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(`
name: b
steps:
  - id: back
    include: a.yml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(a)
	_, err := engine.Run(context.Background(), wf, engine.Options{BaseDir: t.TempDir(), Bus: events.NewBus()})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestIncludeCatalogueRootConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	// caller lives INSIDE root; the file it tries to include lives outside.
	if err := os.WriteFile(filepath.Join(outside, "secret.yml"), []byte(`
name: secret
steps:
  - id: leak
    run: echo pwned
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "caller.yml")
	relPath, _ := filepath.Rel(root, filepath.Join(outside, "secret.yml"))
	if err := os.WriteFile(main, []byte(`
name: caller
steps:
  - id: sub
    include: `+relPath+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(main)
	_, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir:       t.TempDir(),
		Bus:           events.NewBus(),
		CatalogueRoot: root,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes catalogue root") {
		t.Fatalf("want catalogue-root escape error, got %v", err)
	}
}

// TestIncludeCatalogueRootWidenedByProjectRoot mirrors the real user
// layout: workflows/ and lib/ are siblings under a project root. The
// server operator points --include-root at the project root so
// `workflows/main.yml` can `include: ../lib/shared.yml` while --dir
// (the runnable catalogue) stays scoped to workflows/.
func TestIncludeCatalogueRootWidenedByProjectRoot(t *testing.T) {
	root := t.TempDir()
	workflowsDir := filepath.Join(root, "workflows")
	libDir := filepath.Join(root, "lib")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "shared.yml"), []byte(`
name: shared
steps:
  - id: noop
    run: echo hi
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(workflowsDir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: main
steps:
  - id: sub
    include: ../lib/shared.yml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(main)
	// With CatalogueRoot=workflowsDir, the include should be rejected
	// because ../lib escapes it.
	_, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir:       t.TempDir(),
		Bus:           events.NewBus(),
		CatalogueRoot: workflowsDir,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes catalogue root") {
		t.Fatalf("expected confinement error at narrow root, got %v", err)
	}
	// With CatalogueRoot=root (the project root), the include resolves
	// under the widened boundary and the run succeeds.
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir:       t.TempDir(),
		Bus:           events.NewBus(),
		CatalogueRoot: root,
	})
	if err != nil {
		t.Fatalf("widened root: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("widened root: want success, got %s", res.Status)
	}
}

// TestIncludeDefaultResolvesWorkflowDir exercises the common pattern
// where a library workflow's input default references
// `${{ workflow.dir }}/asset.json` so the library ships with its own
// bundled resources and callers don't need to know the internal
// layout. Without interpolation of literal defaults, the shell just
// gets the raw "${{ workflow.dir }}" and can't open the file — the
// exact failure the user's EDI toolkit hit.
func TestIncludeDefaultResolvesWorkflowDir(t *testing.T) {
	dir := t.TempDir()
	// Put an asset file next to the library.
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("hello-from-asset\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	callee := `
name: lib
inputs:
  asset_path:
    description: bundled asset
    default: "${{ workflow.dir }}/asset.txt"
steps:
  - id: readit
    run: |
      cat "$A"
      echo "size=$(wc -c < $A | tr -d ' ')" >> "$WEFTLY_OUTPUT"
    env:
      A: "${{ inputs.asset_path }}"
outputs:
  size: "${{ steps.readit.outputs.size }}"
`
	caller := `
name: caller
steps:
  - id: sub
    include: lib.yml
`
	// Write directly into dir so the child's workflow.dir == dir and
	// the default's ${{ workflow.dir }}/asset.txt resolves.
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(callee), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, err := schema.Load(main)
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(),
		Bus:     events.NewBus(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("want success, got %s", res.Status)
	}
}

func TestIncludeSecretInputMasked(t *testing.T) {
	callee := `
name: lib
inputs:
  api_token: { secret: true, required: true }
steps:
  - id: use
    run: echo "token was $TOKEN"
    env:
      TOKEN: "${{ inputs.api_token }}"
`
	caller := `
name: caller
inputs:
  api_token: { secret: true, required: true }
steps:
  - id: sub
    include: lib.yml
    with:
      api_token: "${{ inputs.api_token }}"
`
	main, _ := writeInclude(t, caller, callee)
	wf, _ := schema.Load(main)
	bus := events.NewBus()
	mu, evs := collectEvents(bus)
	_, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(),
		Bus:     bus,
		Inputs:  map[string]any{"api_token": "very-secret-value-abcdef"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range *evs {
		if l, ok := e.(events.StepLog); ok {
			if strings.Contains(l.Line, "very-secret-value-abcdef") {
				t.Errorf("secret leaked into log line: %q", l.Line)
			}
		}
	}
}

// --- Wave 1: workspace isolation, workspace.dir, with_if_set ---------

// TestIncludeScopedWorkspacesDoNotCollide is the headline correctness
// case: the SAME fragment included twice, each writing the same
// relative path, must produce two distinct files rather than one
// silently overwriting the other.
func TestIncludeScopedWorkspacesDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(`
name: lib
inputs:
  tag: { required: true }
steps:
  - id: write
    run: |
      mkdir -p ./corpus
      echo "$TAG" > ./corpus/manifest.json
      echo "path=$(pwd)/corpus/manifest.json" >> "$WEFTLY_OUTPUT"
    env:
      TAG: "${{ inputs.tag }}"
outputs:
  path: "${{ steps.write.outputs.path }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
steps:
  - id: alpha
    include: lib.yml
    with: { tag: "from-alpha" }
  - id: beta
    include: lib.yml
    with: { tag: "from-beta" }
  - id: verify
    run: |
      echo "alpha file: $A"
      echo "beta file:  $B"
      test "$A" != "$B" || { echo "same path — scopes collided"; exit 1; }
      grep -q from-alpha "$A" || { echo "alpha clobbered"; exit 1; }
      grep -q from-beta  "$B" || { echo "beta clobbered"; exit 1; }
    env:
      A: "${{ steps.alpha.outputs.path }}"
      B: "${{ steps.beta.outputs.path }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, err := schema.Load(main)
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(), Bus: events.NewBus(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("want success, got %s", res.Status)
	}
}

// TestIncludeSharedWorkspaceViaWorkspaceDir is the deliberate opposite
// of the test above: passing `${{ workspace.dir }}` through with:
// evaluates in the PARENT scope, so both children cooperate on one
// directory when the author actually wants that.
func TestIncludeSharedWorkspaceViaWorkspaceDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(`
name: lib
inputs:
  out_dir: { required: true }
  tag:     { required: true }
steps:
  - id: write
    run: |
      mkdir -p "$OUT"
      echo "$TAG" > "$OUT/$TAG.txt"
    env:
      OUT: "${{ inputs.out_dir }}"
      TAG: "${{ inputs.tag }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
steps:
  - id: alpha
    include: lib.yml
    with:
      out_dir: "${{ workspace.dir }}/corpus"
      tag: alpha
  - id: beta
    include: lib.yml
    with:
      out_dir: "${{ workspace.dir }}/corpus"
      tag: beta
  - id: verify
    run: |
      ls ./corpus
      test -f ./corpus/alpha.txt || { echo "missing alpha"; exit 1; }
      test -f ./corpus/beta.txt  || { echo "missing beta";  exit 1; }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(main)
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(), Bus: events.NewBus(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("want success, got %s", res.Status)
	}
}

// TestWorkspaceDirAndWorkflowDirDiffer pins the distinction the two
// namespaces exist to draw: workflow.dir is where the CODE lives,
// workspace.dir is where this run's DATA goes.
func TestWorkspaceDirAndWorkflowDirDiffer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(`
name: lib
steps:
  - id: probe
    run: |
      echo "wf=$WF"
      echo "ws=$WS"
      test "$WF" != "$WS" || { echo "workflow.dir == workspace.dir"; exit 1; }
      test -f "$WF/lib.yml" || { echo "workflow.dir is not the file's dir"; exit 1; }
      test -d "$WS" || { echo "workspace.dir does not exist"; exit 1; }
    env:
      WF: "${{ workflow.dir }}"
      WS: "${{ workspace.dir }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
steps:
  - id: sub
    include: lib.yml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(main)
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(), Bus: events.NewBus(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("want success, got %s", res.Status)
	}
}

// TestWithIfSetKeepsChildDefaultWhenEmpty is the reproduced
// blank-clobbers-default bug: a caller forwarding its own unset
// optional input must not force "" onto the child.
func TestWithIfSetKeepsChildDefaultWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(`
name: lib
inputs:
  parties: { default: "SENSIBLE_DEFAULT" }
steps:
  - id: check
    run: |
      echo "parties=$P"
      test "$P" = "SENSIBLE_DEFAULT" || { echo "default was clobbered with '$P'"; exit 1; }
    env:
      P: "${{ inputs.parties }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
inputs:
  parties: { default: "" }
steps:
  - id: sub
    include: lib.yml
    with_if_set:
      parties: "${{ inputs.parties }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(main)
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(), Bus: events.NewBus(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("want success, got %s", res.Status)
	}
}

// TestWithIfSetOverridesWhenSupplied is the other half of the
// contract: a non-empty value must still win.
func TestWithIfSetOverridesWhenSupplied(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(`
name: lib
inputs:
  parties: { default: "SENSIBLE_DEFAULT" }
steps:
  - id: check
    run: |
      test "$P" = "CALLER_VALUE" || { echo "override lost, got '$P'"; exit 1; }
    env:
      P: "${{ inputs.parties }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
inputs:
  parties: { default: "CALLER_VALUE" }
steps:
  - id: sub
    include: lib.yml
    with_if_set:
      parties: "${{ inputs.parties }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(main)
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(), Bus: events.NewBus(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != events.Success {
		t.Fatalf("want success, got %s", res.Status)
	}
}

// TestWithAndWithIfSetSameKeyRejected — ambiguous, must not silently pick.
func TestWithAndWithIfSetSameKeyRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(`
name: lib
inputs:
  x: { default: "d" }
steps:
  - id: noop
    run: echo hi
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
steps:
  - id: sub
    include: lib.yml
    with:        { x: "a" }
    with_if_set: { x: "b" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf, _ := schema.Load(main)
	_, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(), Bus: events.NewBus(),
	})
	if err == nil || !strings.Contains(err.Error(), "both with:") {
		t.Fatalf("want both-maps rejection, got %v", err)
	}
}
