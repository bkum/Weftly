package actions

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
	"github.com/bkum/weftly/internal/secrets"
	"gopkg.in/yaml.v3"
)

func scalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: s}
}

func newContext(t *testing.T, cfg *yaml.Node, env map[string]string, strict bool) (*StepContext, *[]events.Event) {
	t.Helper()
	sec := secrets.NewRegistry()
	var mu sync.Mutex
	var got []events.Event
	sc := &StepContext{
		StepID:  "s",
		Config:  cfg,
		Env:     env,
		Secrets: sec,
		Workdir: t.TempDir(),
		Emit: func(e events.Event) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, e)
		},
		Expr:   expr.New(),
		Strict: strict,
	}
	return sc, &got
}

func TestRunActionOutputFileKV(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test requires POSIX shell")
	}
	sc, got := newContext(t, scalar(`echo "k1=v1" >> "$WEFTLY_OUTPUT"
echo "flag=true" >> "$WEFTLY_OUTPUT"`), nil, false)
	outs, err := runAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outs["k1"] != "v1" {
		t.Errorf("k1: got %v", outs["k1"])
	}
	if outs["flag"] != true {
		t.Errorf("flag: expected typed bool true, got %v (%T)", outs["flag"], outs["flag"])
	}
	// StepOutput events emitted for each output
	var outputEvents int
	for _, e := range *got {
		if _, ok := e.(events.StepOutput); ok {
			outputEvents++
		}
	}
	if outputEvents != 2 {
		t.Errorf("expected 2 StepOutput events, got %d", outputEvents)
	}
}

func TestRunActionOutputFileHeredoc(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test requires POSIX shell")
	}
	body := `{
  echo "payload<<__EOF__"
  echo "line1"
  echo "line2"
  echo "__EOF__"
} >> "$WEFTLY_OUTPUT"`
	sc, _ := newContext(t, scalar(body), nil, false)
	outs, err := runAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outs["payload"] != "line1\nline2" {
		t.Errorf("heredoc payload: got %q", outs["payload"])
	}
}

func TestRunActionNonZeroExitSkipsOutputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test requires POSIX shell")
	}
	sc, _ := newContext(t, scalar(`echo "will_not_be_seen=y" >> "$WEFTLY_OUTPUT"
exit 3`), nil, false)
	_, err := runAction{}.Run(context.Background(), sc)
	if err == nil {
		t.Fatal("expected error on exit 3")
	}
	if !strings.Contains(err.Error(), "code 3") {
		t.Errorf("want exit code in error, got %v", err)
	}
}

func TestRunActionStrictRejectsInlineExpr(t *testing.T) {
	sc, _ := newContext(t, scalar(`echo "${{ inputs.foo }}"`), nil, true)
	_, err := runAction{}.Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "forbidden under --strict") {
		t.Fatalf("expected strict rejection, got %v", err)
	}
}

func TestRunActionMasksSecretsInLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test requires POSIX shell")
	}
	sc, got := newContext(t, scalar(`echo "leak: $SECRET"`), map[string]string{"SECRET": "hunter2-token"}, false)
	sc.Secrets.Register("hunter2-token")
	_, err := runAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range *got {
		if log, ok := e.(events.StepLog); ok {
			if strings.Contains(log.Line, "hunter2-token") {
				t.Fatalf("secret leaked into log: %q", log.Line)
			}
			if strings.Contains(log.Line, "leak: ***") {
				return
			}
		}
	}
	t.Fatal("expected masked line not seen")
}

func TestRunActionTimeoutKillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test requires POSIX shell")
	}
	sc, _ := newContext(t, scalar(`sleep 5; echo done`), nil, false)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sc.Timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := runAction{}.Run(ctx, sc)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not actually cancel the process")
	}
}

func TestRunActionMalformedOutputRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test requires POSIX shell")
	}
	sc, _ := newContext(t, scalar(`echo "not a valid line" >> "$WEFTLY_OUTPUT"`), nil, false)
	_, err := runAction{}.Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed error, got %v", err)
	}
}

// sanity: writing to WEFTLY_OUTPUT works from the child's cwd
func TestRunActionCwdIsWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test requires POSIX shell")
	}
	sc, _ := newContext(t, scalar(`pwd > pwd.txt
echo "ok=1" >> "$WEFTLY_OUTPUT"`), nil, false)
	if _, err := (runAction{}).Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sc.Workdir + "/pwd.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), sc.Workdir) {
		t.Errorf("cwd not set to workspace: %q vs %q", data, sc.Workdir)
	}
}
