package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// newFmtCmd — `weftly fmt <workflow.yml>` canonicalises indentation
// and key order via a round trip through yaml.Node. It deliberately
// does NOT go through schema.Load (that would apply include: expansion
// and strip comments), staying at the yaml.Node level so comments and
// per-node style attributes survive. Idempotent: fmt-then-fmt is a
// no-op.
//
// --write / -w replaces the file in place; without it the formatted
// output goes to stdout so scripts can pipe it. --diff / -d prints a
// unified-style diff instead of the file body (useful in CI to gate a
// PR on "please run weftly fmt").
func newFmtCmd() *cobra.Command {
	var (
		write bool
		diff  bool
	)
	cmd := &cobra.Command{
		Use:   "fmt <workflow.yml>",
		Short: "Canonicalise a workflow's YAML formatting (in place with -w)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			formatted, err := formatYAML(src)
			if err != nil {
				return err
			}
			if diff {
				printDiff(cmd.OutOrStdout(), path, src, formatted)
				return nil
			}
			if write {
				if string(src) == string(formatted) {
					return nil
				}
				return os.WriteFile(path, formatted, 0o644)
			}
			_, err = cmd.OutOrStdout().Write(formatted)
			return err
		},
	}
	cmd.Flags().BoolVarP(&write, "write", "w", false, "rewrite the file in place")
	cmd.Flags().BoolVarP(&diff, "diff", "d", false, "print a diff between original and formatted; suppresses stdout / --write")
	return cmd
}

// formatYAML re-encodes a YAML document through yaml.Node with a fixed
// 2-space indent and the encoder's default key ordering, which is
// what most weftly files already use.
func formatYAML(src []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, fmt.Errorf("fmt: parse: %w", err)
	}
	buf := &yamlBuffer{}
	enc := yaml.NewEncoder(buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("fmt: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("fmt: encode: %w", err)
	}
	return buf.b, nil
}

// yamlBuffer is a tiny append-only sink for the encoder. Avoids
// dragging bytes.Buffer's zero-cost import in a file where we don't
// otherwise need it.
type yamlBuffer struct{ b []byte }

func (y *yamlBuffer) Write(p []byte) (int, error) {
	y.b = append(y.b, p...)
	return len(p), nil
}

// printDiff writes a very compact per-line prefix diff. Not a full
// unified diff, but enough for the "run weftly fmt to fix" workflow
// to be actionable in CI logs.
func printDiff(w interface{ Write([]byte) (int, error) }, path string, a, b []byte) {
	if string(a) == string(b) {
		return
	}
	fmt.Fprintf(&stubWriter{w}, "--- %s (current)\n+++ %s (fmt)\n", path, path)
	al, bl := splitLines(a), splitLines(b)
	i, j := 0, 0
	for i < len(al) && j < len(bl) {
		if al[i] == bl[j] {
			i++
			j++
			continue
		}
		fmt.Fprintf(&stubWriter{w}, "-%s\n", al[i])
		fmt.Fprintf(&stubWriter{w}, "+%s\n", bl[j])
		i++
		j++
	}
	for ; i < len(al); i++ {
		fmt.Fprintf(&stubWriter{w}, "-%s\n", al[i])
	}
	for ; j < len(bl); j++ {
		fmt.Fprintf(&stubWriter{w}, "+%s\n", bl[j])
	}
}

type stubWriter struct {
	w interface{ Write([]byte) (int, error) }
}

func (s *stubWriter) Write(p []byte) (int, error) { return s.w.Write(p) }

func splitLines(b []byte) []string {
	s := string(b)
	out := []string{}
	start := 0
	for i, c := range s {
		if c == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
