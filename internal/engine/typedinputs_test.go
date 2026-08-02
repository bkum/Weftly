package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/schema"
)

func mustParse(t *testing.T, body string) *schema.Workflow {
	t.Helper()
	wf, err := schema.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return wf
}

// §15.5 — three simultaneously-invalid inputs produce three errors in
// ONE report. A form with three bad fields should not need three round
// trips.
func TestAllInputErrorsReportedAtOnce(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  cases:  { type: int, min: 100 }
  domain: { type: enum, values: [retail, healthcare] }
  when:   { type: duration }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{Supplied: map[string]any{
		"cases": "5", "domain": "retial", "when": "soon",
	}})
	if err == nil {
		t.Fatal("expected input errors")
	}
	ierrs, ok := err.(schema.InputErrors)
	if !ok {
		t.Fatalf("expected schema.InputErrors, got %T: %v", err, err)
	}
	if len(ierrs) != 3 {
		t.Fatalf("expected 3 errors in one report, got %d: %v", len(ierrs), ierrs)
	}
}

// §15.7 — a typed int enters the expression context as a number, so
// `inputs.cases > 1000` compares numerically rather than lexically.
func TestTypedValueEntersExpressionContext(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  cases: { type: int, default: 2500 }
steps: [{id: s, run: echo}]`)
	out, _, err := resolveInputs(wf, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["cases"].(int); !ok {
		t.Fatalf("cases should be an int in the expression context, got %T", out["cases"])
	}
}

// §15.13 — --preset plus --input: the explicit flag wins, the rest of
// the preset still applies.
func TestPresetPrecedence(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  domain: { type: enum, values: [retail, healthcare] }
  cases:  { type: int }
  profile: { type: string }
presets:
  qa_exceptions:
    values:
      domain: retail
      cases: 500
      profile: exception_heavy
steps: [{id: s, run: echo}]`)
	out, _, err := resolveInputs(wf, ResolveOptions{
		Preset:   "qa_exceptions",
		Supplied: map[string]any{"cases": "800"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["cases"] != 800 {
		t.Errorf("--input must beat the preset: cases=%v", out["cases"])
	}
	if out["domain"] != "retail" || out["profile"] != "exception_heavy" {
		t.Errorf("rest of the preset should still apply: %+v", out)
	}
}

func TestUnknownPresetIsFatal(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  a: { default: x }
presets:
  good: { values: { a: y } }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{Preset: "typo"})
	if err == nil {
		t.Fatal("an unknown preset must fail — running with silent defaults is worse than stopping")
	}
	if !strings.Contains(err.Error(), "good") {
		t.Errorf("error should list the available presets: %v", err)
	}
}

// §3 — a default referencing another input resolves in dependency
// order regardless of map iteration order.
func TestDefaultExpressionReferencesAnotherInput(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  transaction: { default: "850" }
  output_file: { default: "out/${{ inputs.transaction }}.edi" }
steps: [{id: s, run: echo}]`)
	for i := 0; i < 20; i++ { // map order varies per run; repeat
		out, _, err := resolveInputs(wf, ResolveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if out["output_file"] != "out/850.edi" {
			t.Fatalf("dependency order not honoured: %v", out["output_file"])
		}
	}
}

// A supplied value must NOT trigger its own default's evaluation.
func TestSuppliedValueSkipsDefaultExpression(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  a: { default: "${{ inputs.missing_thing }}" }
steps: [{id: s, run: echo}]`)
	out, _, err := resolveInputs(wf, ResolveOptions{Supplied: map[string]any{"a": "explicit"}})
	if err != nil {
		t.Fatalf("supplying a value should bypass its default entirely: %v", err)
	}
	if out["a"] != "explicit" {
		t.Errorf("got %v", out["a"])
	}
}

// A required input that is never supplied names itself clearly.
func TestRequiredInputError(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  env_url: { required: true }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{})
	if err == nil || !strings.Contains(err.Error(), "env_url") {
		t.Fatalf("want a required-input error naming env_url, got %v", err)
	}
}

// A `type: path` input is caller-supplied and flows into steps that
// open it, so it must be confined. Both legitimate roots are allowed:
// the run workspace (somewhere to write) and the workflow's own tree
// (a library's bundled assets).
func TestPathInputConfinement(t *testing.T) {
	ws := t.TempDir()
	wfdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wfdir, "asset.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	wf := mustParse(t, `
name: t
inputs:
  p: { type: path }
steps: [{id: s, run: echo}]`)

	resolve := func(v string) (map[string]any, error) {
		out, _, err := resolveInputs(wf, ResolveOptions{
			Supplied: map[string]any{"p": v}, WorkspaceDir: ws, WorkflowDir: wfdir,
		})
		return out, err
	}

	// Escaping both roots is rejected.
	if _, err := resolve("/etc/passwd"); err == nil {
		t.Error("an absolute path outside both roots must be rejected")
	}
	if _, err := resolve("../../../etc/passwd"); err == nil {
		t.Error("traversal out of the workspace must be rejected")
	}

	// Inside the workspace: allowed, and returned absolute.
	out, err := resolve("out/report.html")
	if err != nil {
		t.Fatalf("a workspace-relative path should be allowed: %v", err)
	}
	// Compare against the CANONICAL workspace. resolvePathInput returns
	// a symlink-resolved path, and on macOS t.TempDir() hands back
	// /var/... while the resolved form is /private/var/... — comparing
	// against the raw TempDir would fail on a correct result.
	canonWS := ws
	if r, err := filepath.EvalSymlinks(ws); err == nil {
		canonWS = r
	}
	if got := out["p"].(string); !strings.HasPrefix(got, canonWS) {
		t.Errorf("expected a path under the workspace %q, got %q", canonWS, got)
	}

	// Inside the workflow's tree: allowed, so a library can reach its
	// own bundled assets via ${{ workflow.dir }}.
	if _, err := resolve(filepath.Join(wfdir, "asset.json")); err != nil {
		t.Errorf("a path under the workflow dir should be allowed: %v", err)
	}
}

// must_exist reports against the input rather than failing four steps
// later in a shell command.
func TestPathMustExist(t *testing.T) {
	ws := t.TempDir()
	wf := mustParse(t, `
name: t
inputs:
  p: { type: path, must_exist: true }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{
		Supplied: map[string]any{"p": "nope.json"}, WorkspaceDir: ws, WorkflowDir: ws,
	})
	if err == nil || !strings.Contains(err.Error(), "must_exist") {
		t.Fatalf("want a must_exist error, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "yes.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveInputs(wf, ResolveOptions{
		Supplied: map[string]any{"p": "yes.json"}, WorkspaceDir: ws, WorkflowDir: ws,
	}); err != nil {
		t.Errorf("an existing file should pass: %v", err)
	}
}

// didYouMean must not do unbounded work on a caller-sized value.
func TestDidYouMeanIgnoresHugeValues(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  domain: { type: enum, values: [retail, healthcare] }
steps: [{id: s, run: echo}]`)
	huge := strings.Repeat("x", 1<<20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = resolveInputs(wf, ResolveOptions{Supplied: map[string]any{"domain": huge}})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a 1 MiB enum value took too long — suggestion work is unbounded")
	}
}

// TestPathConfinementThroughSymlinkedRoot reproduces the macOS layout
// on any platform: there, every temp dir lives under /var/... which is
// a symlink to /private/var/..., so the canonical form of a root and
// the lexical form of a path built from it disagree.
//
// This bit twice — once in the include-confinement check and again
// here — because a path whose leaf does not exist yet cannot be
// EvalSymlinks'd at all, so it stays lexical while the roots are
// canonical and filepath.Rel sees two unrelated trees.
func TestPathConfinementThroughSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevation on windows")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	wf := mustParse(t, `
name: t
inputs:
  p: { type: path }
steps: [{id: s, run: echo}]`)

	// Workspace given via the SYMLINK; the path resolves through it to
	// the real directory. Both must be seen as the same tree.
	out, _, err := resolveInputs(wf, ResolveOptions{
		Supplied: map[string]any{"p": "out/report.html"}, WorkspaceDir: link, WorkflowDir: link,
	})
	if err != nil {
		t.Fatalf("a path under a symlinked workspace must be allowed: %v", err)
	}
	got := out["p"].(string)
	if !strings.HasSuffix(got, filepath.Join("out", "report.html")) {
		t.Errorf("unexpected resolved path %q", got)
	}

	// The confinement itself must still hold through the symlink.
	if _, _, err := resolveInputs(wf, ResolveOptions{
		Supplied: map[string]any{"p": "../../../etc/passwd"}, WorkspaceDir: link, WorkflowDir: link,
	}); err == nil {
		t.Error("traversal out of a symlinked root must still be rejected")
	}
}

// A symlink planted inside the workspace that points outside it must not
// become an escape hatch. Two independent layers stop it: canonicalPath
// resolves the link before the containment test, and the must_exist
// probe runs through os.Root, which cannot traverse out of the opened
// directory even if the first layer were wrong.
func TestPathSymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevation on windows")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "escape")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	wf := mustParse(t, `
name: t
inputs:
  p: { type: path, must_exist: true }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{
		Supplied: map[string]any{"p": "escape/secret.txt"}, WorkspaceDir: ws, WorkflowDir: ws,
	})
	if err == nil {
		t.Fatal("a symlink out of the workspace must not be readable through a path input")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("want a confinement error, got %v", err)
	}
}
