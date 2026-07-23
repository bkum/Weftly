package schema

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load reads and parses a workflow file, expanding any `include:` list
// recursively (cycle-detected, paths resolved relative to the including
// file). No validation.
func Load(path string) (*Workflow, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return loadWithVisited(abs, map[string]bool{})
}

// loadWithVisited is the recursive helper for include expansion. The
// visited map is passed by reference (same map across the whole tree)
// so a cycle like a.yml → b.yml → a.yml is caught even across
// unrelated branches.
func loadWithVisited(path string, visited map[string]bool) (*Workflow, error) {
	if visited[path] {
		return nil, fmt.Errorf("include: cycle detected at %s", path)
	}
	visited[path] = true
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	wf, err := Parse(f)
	if err != nil {
		return nil, err
	}
	if len(wf.Include) == 0 {
		return wf, nil
	}
	// Expand each include relative to the including file and prepend
	// its steps + merge its env + inherit its defaults.shell (only if
	// the parent didn't set one). Included name/inputs/description are
	// deliberately dropped — this is a step library, not a workflow.
	base := filepath.Dir(path)
	var mergedSteps []Step
	for _, rel := range wf.Include {
		incPath := rel
		if !filepath.IsAbs(incPath) {
			incPath = filepath.Join(base, rel)
		}
		incAbs, err := filepath.Abs(incPath)
		if err != nil {
			return nil, fmt.Errorf("include %q: %w", rel, err)
		}
		child, err := loadWithVisited(incAbs, visited)
		if err != nil {
			return nil, fmt.Errorf("include %q: %w", rel, err)
		}
		mergedSteps = append(mergedSteps, child.Steps...)
		for k, v := range child.Env {
			if _, exists := wf.Env[k]; !exists {
				if wf.Env == nil {
					wf.Env = map[string]string{}
				}
				wf.Env[k] = v
			}
		}
		if wf.Defaults.Shell == "" && child.Defaults.Shell != "" {
			wf.Defaults.Shell = child.Defaults.Shell
		}
	}
	// Included steps come first (a prelude); parent steps run after.
	wf.Steps = append(mergedSteps, wf.Steps...)
	return wf, nil
}

// Parse reads a workflow from r.
func Parse(r io.Reader) (*Workflow, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if root.Kind == 0 {
		return nil, fmt.Errorf("empty workflow")
	}

	var wf Workflow
	if err := root.Decode(&wf); err != nil {
		return nil, fmt.Errorf("workflow decode: %w", err)
	}
	wf.Source = &root

	// The generic decode above does NOT populate the ActionType/ActionNode
	// fields on each Step because those depend on which action key was
	// present in the YAML. Walk the steps sequence and pick out the action.
	if err := decodeSteps(&root, &wf); err != nil {
		return nil, err
	}
	if err := decodeStepSequence(&root, "cleanup", wf.Cleanup); err != nil {
		return nil, err
	}
	return &wf, nil
}

// decodeStepSequence populates ActionType/ActionNode for the steps in a
// top-level list other than `steps:` (currently just `cleanup:`).
// Delegates to the same per-step walker as decodeSteps.
func decodeStepSequence(root *yaml.Node, key string, steps []Step) error {
	doc := root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	seq := lookupKey(doc, key)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	return decodeStepMappings(seq, steps, key)
}

// decodeSteps finds the top-level `steps:` sequence and, for each step
// mapping, records the action key (if any) and its raw node.
func decodeSteps(root *yaml.Node, wf *Workflow) error {
	doc := root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return fmt.Errorf("root must be a mapping (got kind=%d)", doc.Kind)
	}
	stepsNode := lookupKey(doc, "steps")
	if stepsNode == nil {
		return nil
	}
	if stepsNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: `steps` must be a sequence", stepsNode.Line)
	}
	return decodeStepMappings(stepsNode, wf.Steps, "steps")
}

// decodeStepMappings is the per-step walker shared by decodeSteps and
// decodeStepSequence. Panics-safe: length mismatch between the parsed
// slice and the yaml sequence is a hard "internal" error.
func decodeStepMappings(seq *yaml.Node, steps []Step, label string) error {
	if len(steps) != len(seq.Content) {
		return fmt.Errorf("internal: %s slice length %d does not match yaml sequence %d", label, len(steps), len(seq.Content))
	}
	actionSet := make(map[string]struct{}, len(actionKeys))
	for _, k := range actionKeys {
		actionSet[k] = struct{}{}
	}
	for i, m := range seq.Content {
		if m.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: each step must be a mapping", m.Line)
		}
		steps[i].Source = m
		var found []string
		var primary string
		var primaryNode *yaml.Node
		for j := 0; j < len(m.Content); j += 2 {
			key := m.Content[j].Value
			if _, ok := actionSet[key]; ok {
				found = append(found, key)
				if primary == "" {
					primary = key
					primaryNode = m.Content[j+1]
				}
			}
		}
		// `assert` as a top-level sibling of another action is an inline
		// modifier (spec §6.2 http example), not a competing action. If we
		// see [http, assert] or [run, assert] etc., the non-assert one is
		// the primary and assert is moved into the primary's config.
		if len(found) == 2 && contains(found, "assert") {
			var otherKey string
			for _, k := range found {
				if k != "assert" {
					otherKey = k
				}
			}
			primary = otherKey
			primaryNode = lookupKey(m, otherKey)
			// Inline the assert body under the primary action's mapping so
			// the action's Run(sc) sees it via findChild(sc.Config, "assert").
			if primaryNode != nil && primaryNode.Kind == yaml.MappingNode {
				assertNode := lookupKey(m, "assert")
				if assertNode != nil && lookupKey(primaryNode, "assert") == nil {
					primaryNode.Content = append(primaryNode.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Value: "assert"},
						assertNode)
				}
			}
			found = []string{primary}
		}
		steps[i].ActionType = primary
		steps[i].ActionNode = primaryNode
		if len(found) > 1 {
			return fmt.Errorf("line %d: step must have exactly one action key, found %v", m.Line, found)
		}
		// zero actions is a validation-time error, not a parse-time one; let
		// Validate produce a clean diagnostic there.
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// lookupKey returns the value node for a top-level key in a mapping node.
func lookupKey(m *yaml.Node, key string) *yaml.Node {
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
