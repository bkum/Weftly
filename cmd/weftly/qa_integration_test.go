// QA-style integration tests. Instead of exercising internal APIs, these
// scenarios build the shipped `weftly` binary and drive it through the
// commands and flags a real operator (or a QA automation script) would
// use: exit codes, stdout/stderr messages, artifacts on disk, HTTP
// endpoints exposed by `weftly server`. If any of these break, an
// end-user immediately notices.
package main_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- test bootstrap: build the binary once, share it ----------

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// weftlyBinary compiles cmd/weftly to a temporary path the first time
// it's called and returns that path on every subsequent call. Building
// once and reusing keeps the whole file's runtime bounded even as
// scenarios grow.
func weftlyBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "weftly-qa-*")
		if err != nil {
			binErr = err
			return
		}
		out := filepath.Join(dir, "weftly")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		// -trimpath keeps paths deterministic if the binary is inspected;
		// CGO off matches how the release binary is built.
		cmd := exec.Command("go", "build", "-trimpath", "-o", out, "./cmd/weftly")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		cmd.Dir = repoRoot(t)
		if b, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("build weftly: %v\n%s", err, string(b))
			return
		}
		binPath = out
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// This file lives at cmd/weftly/qa_integration_test.go so the repo
	// root is two levels up from the file's dir. We locate it via wd.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// runWeftly runs the compiled binary with args, returning stdout, stderr
// combined into one string, and the exit code. A non-zero exit is not a
// test failure — several scenarios expect it.
func runWeftly(t *testing.T, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, weftlyBinary(t), args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("weftly %v: %v", args, err)
		}
	}
	return string(out), code
}

// ---------- QA scenario 1: version smoke ----------

func TestQA_VersionPrintsIdentity(t *testing.T) {
	out, code := runWeftly(t, "version")
	if code != 0 {
		t.Fatalf("version: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "weftly") {
		t.Errorf("version output should mention weftly, got:\n%s", out)
	}
}

// ---------- QA scenario 2: help lists every top-level verb ----------

func TestQA_HelpAdvertisesAllCommands(t *testing.T) {
	out, code := runWeftly(t, "--help")
	if code != 0 {
		t.Fatalf("help: exit %d\n%s", code, out)
	}
	for _, verb := range []string{"run", "validate", "list", "server", "init", "fmt", "diff", "version", "mcp"} {
		if !strings.Contains(out, verb) {
			t.Errorf("help missing %q\n%s", verb, out)
		}
	}
}

// ---------- QA scenario 3: `weftly run` on the bundled hello workflow ----------

func TestQA_RunHelloWorkflowEndToEnd(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "hello.yml")
	// Use the bundled starter so we're exercising the file that ships
	// in the release tarball, not a bespoke test fixture.
	src := filepath.Join(repoRoot(t), "workflows", "hello.yml")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("bundled workflow not accessible: %v", err)
	}
	if err := os.WriteFile(wf, body, 0o644); err != nil {
		t.Fatal(err)
	}
	runsDir := filepath.Join(dir, "runs")
	out, code := runWeftly(t,
		"run", wf,
		"--no-color",
		"--input", "name=qa",
	)
	// Weftly's `run` writes state.json under $WEFTLY_HOME (default ~/.weftly).
	// We can't easily point it at runsDir from the CLI, but the run should
	// still finish successfully.
	_ = runsDir
	if code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	for _, want := range []string{"hello, qa!", "run success"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// ---------- QA scenario 4: `weftly run --json` emits event stream ----------

func TestQA_RunJSONEmitsTypedEvents(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "hello.yml")
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "workflows", "hello.yml"))
	if err != nil {
		t.Skipf("bundled workflow not accessible: %v", err)
	}
	if err := os.WriteFile(wf, body, 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runWeftly(t, "run", wf, "--json", "--input", "name=qa")
	if code != 0 {
		t.Fatalf("run --json: exit %d\n%s", code, out)
	}
	// Every non-empty stdout line should decode as {"type":"...","event":{...}}.
	// The tty renderer's non-JSON updates get filtered out because --json
	// switches the renderer wholesale.
	var seenStarted, seenFinished bool
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			// Some diagnostic lines may not be JSON — tolerate but
			// require at least one RunStarted and one RunFinished.
			continue
		}
		if env.Type == "RunStarted" {
			seenStarted = true
		}
		if env.Type == "RunFinished" {
			seenFinished = true
		}
	}
	if !seenStarted || !seenFinished {
		t.Errorf("--json output missing RunStarted/RunFinished:\n%s", out)
	}
}

// ---------- QA scenario 5: validate rejects a broken workflow ----------

func TestQA_ValidateRejectsBrokenWorkflowWithNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yml")
	// `id` collision — two steps with the same id is a spec violation
	// the validator should catch clearly (spec §5).
	body := "name: broken\nsteps:\n  - id: same\n    run: echo a\n  - id: same\n    run: echo b\n"
	if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runWeftly(t, "validate", bad)
	if code == 0 {
		t.Fatalf("expected non-zero exit for duplicate step id, got 0:\n%s", out)
	}
	// The error must at least hint at the actual problem.
	if !strings.Contains(out, "same") {
		t.Errorf("validation error should reference the duplicated id %q:\n%s", "same", out)
	}
}

func TestQA_ValidateAcceptsBundledFlagshipWorkflow(t *testing.T) {
	wf := filepath.Join(repoRoot(t), "workflows", "petclinic-onboarding.yml")
	if _, err := os.Stat(wf); err != nil {
		t.Skipf("bundled flagship missing: %v", err)
	}
	out, code := runWeftly(t, "validate", wf)
	if code != 0 {
		t.Fatalf("bundled flagship failed validate: %d\n%s", code, out)
	}
}

// ---------- QA scenario 6: init scaffolds a runnable workflow ----------

func TestQA_InitProducesFileThatValidatesAndRuns(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "scaffold.yml")
	if out, code := runWeftly(t, "init", "--out", wf, "scaffold"); code != 0 {
		t.Fatalf("init: %d\n%s", code, out)
	}
	if _, err := os.Stat(wf); err != nil {
		t.Fatalf("init did not create file: %v", err)
	}
	if out, code := runWeftly(t, "validate", wf); code != 0 {
		t.Fatalf("scaffolded file failed validate: %d\n%s", code, out)
	}
}

// ---------- QA scenario 7: --dry-run compiles and prints without executing ----------

func TestQA_DryRunSkipsExecution(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yml")
	// Include a step that would fail if actually run (missing command).
	body := "name: dry\nsteps:\n  - id: never\n    run: /nonexistent/command\n"
	if err := os.WriteFile(wf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runWeftly(t, "run", wf, "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run should succeed even with un-runnable step, got %d\n%s", code, out)
	}
}

// ---------- QA scenario 8: server end-to-end via subprocess ----------

// findFreePort asks the kernel for a free localhost port so a subprocess
// server can bind without racing another test.
func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForHealthz(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server never answered /healthz at %s", base)
}

func TestQA_ServerEndToEndSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("subprocess signalling differs on windows; covered by unit tests")
	}
	// Stage a catalogue containing just the hello workflow.
	dir := t.TempDir()
	cat := filepath.Join(dir, "catalogue")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "workflows", "hello.yml"))
	if err != nil {
		t.Skipf("hello workflow not accessible: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cat, "hello.yml"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	port := findFreePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	runsDir := filepath.Join(dir, "runs")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, weftlyBinary(t),
		"server",
		"--addr", fmt.Sprintf("127.0.0.1:%d", port),
		"--dir", cat,
		"--runs-dir", runsDir,
		"--token", "qa-token",
	)
	cmd.Dir = repoRoot(t)
	// Capture output for diagnostics on failure.
	logBuf := &syncBuffer{}
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Always stop the server, even on t.Fatal.
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	waitForHealthz(t, base)

	// Scenario 8.1: /workflows without a token → 401.
	resp, err := http.Get(base + "/workflows")
	if err != nil {
		t.Fatalf("unauthenticated GET: %v\n%s", err, logBuf.String())
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("no-token /workflows: want 401, got %d", resp.StatusCode)
	}

	// Scenario 8.2: /workflows with wrong token → 401.
	req, _ := http.NewRequest("GET", base+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer nope")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("wrong-token /workflows: want 401, got %d", resp.StatusCode)
	}

	// Scenario 8.3: /workflows with correct token → 200 and lists hello.
	req, _ = http.NewRequest("GET", base+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer qa-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("good-token /workflows: %d %s", resp.StatusCode, string(body))
	}
	var listing struct {
		Workflows []struct {
			ID string `json:"id"`
		}
	}
	_ = json.NewDecoder(resp.Body).Decode(&listing)
	if len(listing.Workflows) == 0 {
		t.Fatalf("catalogue empty in listing: %+v", listing)
	}

	// Scenario 8.4: POST /runs kicks off a run and returns 202+run_id.
	runBody, _ := json.Marshal(map[string]any{
		"workflow": listing.Workflows[0].ID,
		"inputs":   map[string]any{"name": "qa"},
	})
	req, _ = http.NewRequest("POST", base+"/runs", strings.NewReader(string(runBody)))
	req.Header.Set("Authorization", "Bearer qa-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /runs: %d %s", resp.StatusCode, string(body))
	}
	var runRes struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&runRes); err != nil {
		t.Fatal(err)
	}
	if runRes.RunID == "" {
		t.Fatal("empty run_id from POST /runs")
	}

	// Scenario 8.5: SSE stream carries RunFinished with success.
	sseCtx, sseCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer sseCancel()
	req, _ = http.NewRequestWithContext(sseCtx, "GET", base+"/runs/"+runRes.RunID+"/events", nil)
	req.Header.Set("Authorization", "Bearer qa-token")
	sseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	sc := bufio.NewScanner(sseResp.Body)
	sc.Buffer(make([]byte, 128*1024), 4*1024*1024)
	finishedOK := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, `"RunFinished"`) && strings.Contains(line, `"Status":"success"`) {
			finishedOK = true
			break
		}
	}
	if !finishedOK {
		t.Fatalf("SSE did not surface RunFinished/success; server log:\n%s", logBuf.String())
	}
}

// syncBuffer is an io.Writer safe for concurrent stdout/stderr fan-in
// from an exec.Cmd.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
