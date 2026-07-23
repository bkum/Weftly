// Package mcp exposes the workflow catalogue as an MCP (Model Context
// Protocol) tool server over stdio. IDE agents (Claude Code and
// friends) connect over a spawned process; every workflow in the
// catalogue directory becomes an MCP tool whose call runs the
// workflow synchronously.
//
// Protocol subset implemented:
//   - initialize
//   - notifications/initialized
//   - tools/list
//   - tools/call
//
// Everything else returns method-not-found. The wire is line-delimited
// JSON-RPC 2.0 over stdin/stdout, per the MCP transport spec.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/schema"
)

// Config parametrises the server. Dir is scanned once at startup; the
// tool set is stable for the process lifetime (matching how MCP clients
// cache tool schemas after initialize).
type Config struct {
	Dir string // catalogue directory (*.yml / *.yaml)
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Serve runs the request loop until stdin closes or the context is
// done. Fatal errors return; recoverable per-request errors go on the
// wire as JSON-RPC error objects.
func Serve(ctx context.Context, cfg Config) error {
	if cfg.In == nil {
		cfg.In = os.Stdin
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.Err == nil {
		cfg.Err = os.Stderr
	}
	tools, err := loadTools(cfg.Dir)
	if err != nil {
		return err
	}
	s := &server{
		cfg:   cfg,
		tools: tools,
	}
	return s.loop(ctx)
}

// tool is one workflow exposed as an MCP tool.
type tool struct {
	Name        string
	Description string
	Workflow    *schema.Workflow
	InputSchema map[string]any
}

func loadTools(dir string) (map[string]*tool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("mcp: read dir: %w", err)
	}
	tools := map[string]*tool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		wf, err := schema.Load(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("mcp: %s: %w", name, err)
		}
		if err := schema.Validate(wf); err != nil {
			return nil, fmt.Errorf("mcp: %s: %w", name, err)
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		tools[id] = &tool{
			Name:        id,
			Description: wf.Description,
			Workflow:    wf,
			InputSchema: inputsToJSONSchema(wf.Inputs),
		}
	}
	return tools, nil
}

// inputsToJSONSchema turns the workflow's declared inputs into a
// JSON-Schema-shaped object the MCP client can validate arguments
// against. Only the subset MCP clients actually use.
func inputsToJSONSchema(inputs map[string]schema.Input) map[string]any {
	props := map[string]any{}
	var required []string
	for name, in := range inputs {
		p := map[string]any{}
		switch in.Type {
		case schema.InputNumber:
			p["type"] = "number"
		case schema.InputBool:
			p["type"] = "boolean"
		default:
			p["type"] = "string"
		}
		if in.Description != "" {
			p["description"] = in.Description
		}
		if in.Default != nil {
			p["default"] = in.Default
		}
		props[name] = p
		if in.Required {
			required = append(required, name)
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// -----------------------------------------------------------------------
// Request loop
// -----------------------------------------------------------------------

type server struct {
	cfg   Config
	tools map[string]*tool
	mu    sync.Mutex // serialises writes to Out so response lines don't interleave
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *server) loop(ctx context.Context) error {
	scanner := bufio.NewScanner(s.cfg.In)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(s.cfg.Err, "mcp: bad request: %v\n", err)
			continue
		}
		s.handle(ctx, &req)
	}
	return scanner.Err()
}

func (s *server) handle(ctx context.Context, req *rpcRequest) {
	// Notifications carry no id and expect no response.
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		s.reply(req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "weftly", "version": "0.4"},
		})
	case "notifications/initialized":
		// no-op ack
	case "tools/list":
		list := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			list = append(list, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
		s.reply(req.ID, map[string]any{"tools": list})
	case "tools/call":
		s.handleToolCall(ctx, req)
	default:
		if !isNotification {
			s.replyError(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

// callParams is the wire shape for tools/call.
type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *server) handleToolCall(ctx context.Context, req *rpcRequest) {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	t, ok := s.tools[p.Name]
	if !ok {
		s.replyError(req.ID, -32602, "unknown tool: "+p.Name)
		return
	}
	// Collect a rendered transcript for the MCP client. We subscribe to
	// StepFinished + SummaryEmitted; per-line log capture would balloon
	// the response for tools the client already gets via streaming
	// (weftly's log stream isn't exposed over MCP in this MVP).
	var (
		mu         sync.Mutex
		transcript strings.Builder
	)
	bus := events.NewBus()
	bus.Subscribe(func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev := e.(type) {
		case events.StepFinished:
			fmt.Fprintf(&transcript, "step %s → %s (%s)\n", ev.StepID, ev.Status, ev.Duration.Round(1e6))
			if ev.Err != nil {
				fmt.Fprintf(&transcript, "  err: %s\n", ev.Err)
			}
		case events.SummaryEmitted:
			fmt.Fprintf(&transcript, "\n%s\n", ev.Markdown)
		}
	})
	res, err := engine.Run(ctx, t.Workflow, engine.Options{
		Inputs: p.Arguments,
		Bus:    bus,
	})
	if err != nil {
		s.replyError(req.ID, -32000, "engine: "+err.Error())
		return
	}
	fmt.Fprintf(&transcript, "\nrun %s finished in %s\n", res.Status, res.Duration.Round(1e6))
	s.reply(req.ID, map[string]any{
		"content": []any{map[string]any{
			"type": "text",
			"text": transcript.String(),
		}},
		"isError": res.Status != events.Success,
	})
}

func (s *server) reply(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) replyError(id json.RawMessage, code int, msg string) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *server) write(resp rpcResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(s.cfg.Err, "mcp: marshal: %v\n", err)
		return
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.cfg.Out.Write(b)
}
