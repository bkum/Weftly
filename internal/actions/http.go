package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bkum/weftly/internal/events"
	"gopkg.in/yaml.v3"
)

func init() { Register(&httpAction{}) }

// httpAction is the workhorse for REST-shaped workflows. Method-as-key
// means the schema stays free of a `method:` field:
//
//	http:
//	  POST: "${{ inputs.env_url }}/rest/partners"
//	  headers: { Content-Type: application/json }
//	  body: { name: "${{ inputs.partner }}" }
//	  timeout: 30s
//	  assert: response.status == 201
type httpAction struct{}

func (httpAction) Type() string { return "http" }

func (httpAction) Validate(cfg StepConfig) error {
	if cfg == nil || cfg.Kind != yaml.MappingNode {
		return errors.New("http: config must be a mapping")
	}
	if method, _ := findMethod(cfg); method == "" {
		return errors.New("http: no method key (GET/POST/PUT/PATCH/DELETE/HEAD)")
	}
	return nil
}

func (httpAction) Run(ctx context.Context, sc *StepContext) (Outputs, error) {
	method, urlNode := findMethod(sc.Config)
	if method == "" {
		return nil, errors.New("http: no method key")
	}
	urlStr, err := interpString(sc, urlNode)
	if err != nil {
		return nil, fmt.Errorf("http url: %w", err)
	}
	// Optional fields.
	headersNode := findChild(sc.Config, "headers")
	bodyNode := findChild(sc.Config, "body")
	timeoutNode := findChild(sc.Config, "timeout")
	assertNode := findChild(sc.Config, "assert")

	timeout := sc.HTTPTimeout
	if timeoutNode != nil {
		var s string
		if err := timeoutNode.Decode(&s); err == nil && s != "" {
			if d, err := time.ParseDuration(s); err == nil {
				timeout = d
			}
		} else {
			var d time.Duration
			if err := timeoutNode.Decode(&d); err == nil {
				timeout = d
			}
		}
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Body: JSON-encoded if mapping/sequence, raw string otherwise.
	var reqBody io.Reader
	var isJSON bool
	if bodyNode != nil {
		body, ct, err := encodeBody(sc, bodyNode)
		if err != nil {
			return nil, fmt.Errorf("http body: %w", err)
		}
		reqBody = bytes.NewReader(body)
		isJSON = ct == "application/json"
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, strings.ToUpper(method), urlStr, reqBody)
	if err != nil {
		return nil, err
	}
	// Merge workflow default headers, then step headers.
	for k, v := range sc.HTTPHeaders {
		s, err := sc.Expr.InterpolateString(v, sc.ExprEnv)
		if err != nil {
			return nil, fmt.Errorf("http default header %s: %w", k, err)
		}
		req.Header.Set(k, s)
	}
	if headersNode != nil && headersNode.Kind == yaml.MappingNode {
		for i := 0; i < len(headersNode.Content); i += 2 {
			k := headersNode.Content[i].Value
			v, err := interpString(sc, headersNode.Content[i+1])
			if err != nil {
				return nil, fmt.Errorf("http header %s: %w", k, err)
			}
			req.Header.Set(k, v)
		}
	}
	if isJSON && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	sc.Log(events.Info, fmt.Sprintf("%s %s", req.Method, req.URL.String()))
	client := &http.Client{Timeout: timeout + 5*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Response body: parse as JSON if the content type says so or parses
	// cleanly, else expose raw text.
	respView := map[string]any{
		"status":  resp.StatusCode,
		"headers": flattenHeaders(resp.Header),
		"raw":     string(raw),
		"body":    parseBody(resp.Header.Get("Content-Type"), raw),
	}
	sc.Response = respView

	sc.Log(events.Info, fmt.Sprintf("← %d %s (%d bytes)", resp.StatusCode, http.StatusText(resp.StatusCode), len(raw)))

	// Inline assertion inside the http step.
	if assertNode != nil {
		var assertExpr string
		if err := assertNode.Decode(&assertExpr); err == nil && strings.TrimSpace(assertExpr) != "" {
			envWithResp := sc.ExprEnv
			envWithResp.Response = respView
			ok, aerr := sc.Expr.EvaluateBool(assertExpr, envWithResp)
			if aerr != nil {
				return nil, fmt.Errorf("http assert: %w", aerr)
			}
			if !ok {
				return nil, fmt.Errorf("http assert failed: %s (status=%d)", assertExpr, resp.StatusCode)
			}
		}
	}
	return Outputs{}, nil
}

// findMethod returns the first method-shaped key present in the mapping.
func findMethod(m *yaml.Node) (string, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return "", nil
	}
	methods := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true,
		"DELETE": true, "HEAD": true, "OPTIONS": true,
	}
	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i].Value
		if methods[strings.ToUpper(k)] {
			return strings.ToUpper(k), m.Content[i+1]
		}
	}
	return "", nil
}

func findChild(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// interpString decodes n as a string then interpolates ${{ }} spans.
func interpString(sc *StepContext, n *yaml.Node) (string, error) {
	if n == nil {
		return "", nil
	}
	var s string
	if err := n.Decode(&s); err != nil {
		return "", err
	}
	return sc.Expr.InterpolateString(s, sc.ExprEnv)
}

// encodeBody turns a YAML body node into a request-ready byte slice.
// Mappings/sequences become JSON; scalars are treated as raw strings
// (interpolated first).
func encodeBody(sc *StepContext, n *yaml.Node) ([]byte, string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		s, err := interpString(sc, n)
		if err != nil {
			return nil, "", err
		}
		return []byte(s), "", nil
	case yaml.MappingNode, yaml.SequenceNode:
		val, err := interpolateAny(sc, n)
		if err != nil {
			return nil, "", err
		}
		b, err := json.Marshal(val)
		if err != nil {
			return nil, "", err
		}
		return b, "application/json", nil
	}
	return nil, "", fmt.Errorf("unsupported body kind %v", n.Kind)
}

// interpolateAny walks a yaml.Node recursively, decoding scalars through
// the expression engine (preserving raw types for whole-string ${{ ... }}
// wrappers) so JSON encoding of the result carries the right types.
func interpolateAny(sc *StepContext, n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		var raw any
		if err := n.Decode(&raw); err != nil {
			return nil, err
		}
		if s, ok := raw.(string); ok {
			return sc.Expr.Interpolate(s, sc.ExprEnv)
		}
		return raw, nil
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			v, err := interpolateAny(sc, n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := interpolateAny(sc, c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	var raw any
	_ = n.Decode(&raw)
	return raw, nil
}

// parseBody decodes application/json bodies, otherwise returns the raw
// string. A JSON content-type with a broken body returns nil so
// `response.body.something` degrades gracefully.
func parseBody(contentType string, raw []byte) any {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "json") {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
		return nil
	}
	// Best-effort: if the body starts with `{` or `[`, still try JSON.
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	}
	return string(raw)
}

func flattenHeaders(h http.Header) map[string]string {
	m := map[string]string{}
	for k, vs := range h {
		if len(vs) > 0 {
			m[k] = vs[0]
		}
	}
	return m
}
