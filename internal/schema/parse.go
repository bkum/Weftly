package schema

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and parses a workflow file. It performs YAML unmarshal only; no
// validation. Callers should follow with Validate.
func Load(path string) (*Workflow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
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
	return &wf, nil
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
	if len(wf.Steps) != len(stepsNode.Content) {
		return fmt.Errorf("internal: step slice length %d does not match yaml sequence %d", len(wf.Steps), len(stepsNode.Content))
	}
	actionSet := make(map[string]struct{}, len(actionKeys))
	for _, k := range actionKeys {
		actionSet[k] = struct{}{}
	}
	for i, m := range stepsNode.Content {
		if m.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: each step must be a mapping", m.Line)
		}
		wf.Steps[i].Source = m
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
		wf.Steps[i].ActionType = primary
		wf.Steps[i].ActionNode = primaryNode
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
