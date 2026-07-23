package gha

import (
	"strings"
	"testing"
)

func imp(t *testing.T, src string, opts Options) *ImportResult {
	t.Helper()
	res, err := Import(strings.NewReader(src), opts)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return res
}

func TestImportBasicRunStep(t *testing.T) {
	src := `
name: Deploy
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: hello
        id: greet
        run: echo hi
        env:
          FOO: bar
`
	res := imp(t, src, Options{})
	if res.Job != "build" {
		t.Errorf("job: got %q", res.Job)
	}
	got := string(res.YAML)
	for _, want := range []string{"name: Deploy", "id: greet", "run: echo hi", "FOO: bar"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Note about `on:` should be recorded.
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "dropped `on:`") {
		t.Errorf("missing on: note. notes:\n%s", joined)
	}
}

func TestImportSkipsUsesStep(t *testing.T) {
	src := `
name: X
jobs:
  j:
    steps:
      - uses: actions/checkout@v4
      - run: echo hi
`
	res := imp(t, src, Options{})
	if !strings.Contains(string(res.YAML), "run: echo hi") {
		t.Fatalf("run: step missing:\n%s", res.YAML)
	}
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "actions/checkout@v4") {
		t.Errorf("missing skip note for checkout. notes:\n%s", joined)
	}
}

func TestImportSanitisesID(t *testing.T) {
	src := `
name: X
jobs:
  j:
    steps:
      - id: My-Step
        run: echo ok
`
	res := imp(t, src, Options{})
	if !strings.Contains(string(res.YAML), "id: my_step") {
		t.Errorf("expected sanitised id, got:\n%s", res.YAML)
	}
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, `"My-Step" → "my_step"`) {
		t.Errorf("missing id-rename note. notes:\n%s", joined)
	}
}

func TestImportSynthesizesIDWhenMissing(t *testing.T) {
	src := `
name: X
jobs:
  j:
    steps:
      - run: echo 1
      - run: echo 2
`
	res := imp(t, src, Options{})
	got := string(res.YAML)
	if !strings.Contains(got, "id: step_1") || !strings.Contains(got, "id: step_2") {
		t.Errorf("expected step_1 + step_2, got:\n%s", got)
	}
}

func TestImportMultipleJobsNeedsPick(t *testing.T) {
	src := `
name: X
jobs:
  build:
    steps: [{run: echo build}]
  deploy:
    steps: [{run: echo deploy}]
`
	// Default → alphabetically first.
	res := imp(t, src, Options{})
	if res.Job != "build" || !strings.Contains(string(res.YAML), "echo build") {
		t.Errorf("default job pick wrong: job=%s\n%s", res.Job, res.YAML)
	}
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "workflow has 2 jobs") {
		t.Errorf("expected multi-job note. notes:\n%s", joined)
	}
	// Explicit --job → chosen job.
	res = imp(t, src, Options{JobID: "deploy"})
	if res.Job != "deploy" || !strings.Contains(string(res.YAML), "echo deploy") {
		t.Errorf("explicit job pick wrong: job=%s\n%s", res.Job, res.YAML)
	}
}

func TestImportGHAOnlyHelperFlagged(t *testing.T) {
	src := `
name: X
jobs:
  j:
    steps:
      - if: ${{ success() && !cancelled() }}
        run: echo hi
`
	res := imp(t, src, Options{})
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "won't evaluate under weftly") {
		t.Errorf("expected GHA-helper note. notes:\n%s", joined)
	}
}

func TestImportTimeoutMinutes(t *testing.T) {
	src := `
name: X
jobs:
  j:
    steps:
      - id: slow
        run: sleep 5
        timeout-minutes: 3
`
	res := imp(t, src, Options{})
	if !strings.Contains(string(res.YAML), "timeout: 3m0s") {
		t.Errorf("expected timeout: 3m0s, got:\n%s", res.YAML)
	}
}

func TestImportRejectsAllUsesOnly(t *testing.T) {
	src := `
name: X
jobs:
  j:
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
`
	_, err := Import(strings.NewReader(src), Options{})
	if err == nil || !strings.Contains(err.Error(), "no importable steps") {
		t.Fatalf("expected no-importable-steps error, got %v", err)
	}
}

func TestImportRejectsMissingJob(t *testing.T) {
	src := `
name: X
jobs:
  build:
    steps: [{run: echo hi}]
`
	_, err := Import(strings.NewReader(src), Options{JobID: "missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing-job error, got %v", err)
	}
}

func TestImportRejectsZeroJobs(t *testing.T) {
	src := `
name: X
`
	_, err := Import(strings.NewReader(src), Options{})
	if err == nil || !strings.Contains(err.Error(), "no jobs") {
		t.Fatalf("expected no-jobs error, got %v", err)
	}
}
