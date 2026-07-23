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
)

func init() { Register(notifyAction{}) }

// notifyAction posts a message to a webhook. It defaults to a
// Slack-shaped payload (`{"text": "..."}`) but any target that accepts
// a JSON body works; for a fully custom shape, use the `payload:`
// field. Config shape:
//
//	notify:
//	  url: https://hooks.slack.com/services/T00/B00/xyz
//	  message: "deploy of ${{ inputs.version }} finished"
//	  # optional
//	  payload:
//	    channel: "#ops"
//	    text: "custom body"
//	  headers:
//	    X-Weftly-Run: "${{ run.id }}"
//	  timeout: 10s
//
// Secrets registered on the run are masked from log lines this action
// emits, exactly like every other action. The URL itself is treated
// as sensitive: it's registered with the secret masker on first use
// so it can't leak into subsequent StepLog output if a workflow
// carelessly echoes it.
type notifyAction struct{}

func (notifyAction) Type() string { return "notify" }

type notifyConfig struct {
	URL     string            `yaml:"url"`
	Message string            `yaml:"message"`
	Payload map[string]any    `yaml:"payload"`
	Headers map[string]string `yaml:"headers"`
	Timeout time.Duration     `yaml:"timeout"`
}

func (notifyAction) Validate(cfg StepConfig) error {
	if cfg == nil {
		return errors.New("notify: config is required")
	}
	var nc notifyConfig
	if err := cfg.Decode(&nc); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	if nc.URL == "" {
		return errors.New("notify: url is required")
	}
	if nc.Message == "" && nc.Payload == nil {
		return errors.New("notify: one of message or payload is required")
	}
	return nil
}

func (notifyAction) Run(ctx context.Context, sc *StepContext) (Outputs, error) {
	var nc notifyConfig
	if err := sc.Config.Decode(&nc); err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}

	// Interpolate url + message + headers via expr.
	url, err := sc.Expr.InterpolateString(nc.URL, sc.ExprEnv)
	if err != nil {
		return nil, fmt.Errorf("notify: url: %w", err)
	}
	if sc.Secrets != nil {
		sc.Secrets.Register(url)
	}
	msg, err := sc.Expr.InterpolateString(nc.Message, sc.ExprEnv)
	if err != nil {
		return nil, fmt.Errorf("notify: message: %w", err)
	}

	// Build the payload. When the workflow supplied a custom payload
	// map, use it verbatim; otherwise wrap the message in the
	// Slack-shaped `{"text": ...}` envelope that most webhook targets
	// (Slack, MS Teams incoming webhook adapter, Discord after routing)
	// accept.
	body := nc.Payload
	if body == nil {
		body = map[string]any{"text": msg}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("notify: marshal: %w", err)
	}

	timeout := nc.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST", url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range nc.Headers {
		hv, err := sc.Expr.InterpolateString(v, sc.ExprEnv)
		if err != nil {
			return nil, fmt.Errorf("notify: header %s: %w", k, err)
		}
		req.Header.Set(k, hv)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("notify: webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	sc.Log(events.Info, fmt.Sprintf("notify: %d — %d bytes response", resp.StatusCode, len(respBody)))
	return Outputs{"status": resp.StatusCode}, nil
}
