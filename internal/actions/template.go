package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/workspace"
	"gopkg.in/yaml.v3"
)

func init() { Register(&templateAction{}) }

// templateAction renders a Go text/template into a workspace file. `src`
// may be either a filesystem path OR an inline template supplied via
// `inline:` for one-off uses. `dest` must resolve inside the workspace.
type templateAction struct{}

func (templateAction) Type() string { return "template" }

func (templateAction) Validate(cfg StepConfig) error {
	if cfg == nil || cfg.Kind != yaml.MappingNode {
		return errors.New("template: config must be a mapping")
	}
	src := findChild(cfg, "src")
	inline := findChild(cfg, "inline")
	if src == nil && inline == nil {
		return errors.New("template: `src:` or `inline:` required")
	}
	if findChild(cfg, "dest") == nil {
		return errors.New("template: `dest:` required")
	}
	return nil
}

func (templateAction) Run(_ context.Context, sc *StepContext) (Outputs, error) {
	srcNode := findChild(sc.Config, "src")
	inlineNode := findChild(sc.Config, "inline")
	destNode := findChild(sc.Config, "dest")
	varsNode := findChild(sc.Config, "vars")

	dest, err := interpString(sc, destNode)
	if err != nil {
		return nil, fmt.Errorf("template dest: %w", err)
	}
	destAbs, err := workspace.SafeJoin(sc.Workdir, dest)
	if err != nil {
		return nil, fmt.Errorf("template dest: %w", err)
	}
	// Interpolate the vars map (both keys and values). Preserve types.
	vars := map[string]any{}
	if varsNode != nil && varsNode.Kind == yaml.MappingNode {
		for i := 0; i < len(varsNode.Content); i += 2 {
			k := varsNode.Content[i].Value
			v, err := interpolateAny(sc, varsNode.Content[i+1])
			if err != nil {
				return nil, fmt.Errorf("template vars[%s]: %w", k, err)
			}
			vars[k] = v
		}
	}

	var body string
	var name string
	if inlineNode != nil {
		body = inlineNode.Value
		name = "inline"
	} else {
		p, err := interpString(sc, srcNode)
		if err != nil {
			return nil, fmt.Errorf("template src: %w", err)
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(sc.Workdir, p)
			// callers may point at a template outside the workspace (a
			// checked-in template file). Only refuse absolute paths that
			// look weird; do not workspace-restrict src.
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("template src: %w", err)
		}
		body = string(data)
		name = filepath.Base(p)
	}

	tmpl, err := template.New(name).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("template parse: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(destAbs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := tmpl.Execute(f, vars); err != nil {
		return nil, fmt.Errorf("template execute: %w", err)
	}
	sc.Log(events.Info, fmt.Sprintf("rendered %s → %s", name, relTo(sc.Workdir, destAbs)))
	return Outputs{"dest": destAbs}, nil
}

func relTo(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return strings.TrimPrefix(p, base)
}
