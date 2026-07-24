package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/bkum/weftly/internal/actions"
)

// writeCatalogue drops a tiny valid workflow into a dir and returns
// the dir path.
func writeCatalogue(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := `
name: smoke
description: minimal MCP-exposed workflow
inputs:
  who:
    default: world
    description: who to greet
steps:
  - id: hello
    run: |
      echo "greeting=hi-$WHO" >> "$WEFTLY_OUTPUT"
    env:
      WHO: "${{ inputs.who }}"
`
	if err := os.WriteFile(filepath.Join(dir, "smoke.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runServe pipes the given requests through Serve and returns each
// non-empty response as a decoded map.
func runServe(t *testing.T, dir string, requests ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out, errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := Serve(ctx, Config{Dir: dir, BaseDir: t.TempDir(), In: in, Out: &out, Err: &errBuf}); err != nil {
		t.Fatalf("Serve: %v; stderr:\n%s", err, errBuf.String())
	}
	var out2 []map[string]any
	scan := bufio.NewScanner(&out)
	scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scan.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scan.Bytes(), &m); err != nil {
			t.Fatalf("decode response: %v — line: %s", err, scan.Text())
		}
		out2 = append(out2, m)
	}
	return out2
}

func TestMCPInitializeAndListTools(t *testing.T) {
	dir := writeCatalogue(t)
	resps := runServe(t, dir,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses (initialize + tools/list; notification is silent), got %d: %+v", len(resps), resps)
	}
	init := resps[0]["result"].(map[string]any)
	if init["protocolVersion"] == "" {
		t.Errorf("initialize missing protocolVersion")
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("want 1 tool, got %+v", tools)
	}
	if tools[0].(map[string]any)["name"] != "smoke" {
		t.Errorf("tool name: got %+v", tools[0])
	}
}

func TestMCPToolCallExecutesWorkflow(t *testing.T) {
	dir := writeCatalogue(t)
	resps := runServe(t, dir,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"smoke","arguments":{"who":"there"}}}`,
	)
	if len(resps) < 2 {
		t.Fatalf("want at least 2 responses, got %+v", resps)
	}
	call := resps[1]
	if call["error"] != nil {
		t.Fatalf("tool call error: %+v", call["error"])
	}
	result := call["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("isError should be false on success: %+v", result)
	}
	content := result["content"].([]any)
	txt := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(txt, "run success finished") {
		t.Errorf("transcript missing run-success line:\n%s", txt)
	}
	if !strings.Contains(txt, "step hello → success") {
		t.Errorf("transcript missing step line:\n%s", txt)
	}
}

func TestMCPUnknownMethodIsErrorReply(t *testing.T) {
	dir := writeCatalogue(t)
	resps := runServe(t, dir,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":99,"method":"bogus/method"}`,
	)
	if len(resps) < 2 {
		t.Fatalf("want 2 responses, got %+v", resps)
	}
	if resps[1]["error"] == nil {
		t.Errorf("expected error reply for unknown method, got %+v", resps[1])
	}
}
