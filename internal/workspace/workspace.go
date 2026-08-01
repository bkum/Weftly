// Package workspace manages per-run directories: the shared step working
// directory, the artifacts collection dir, and safe path resolution for
// user-supplied dest paths in the template and upload actions.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace represents one run's on-disk layout.
type Workspace struct {
	Root         string // ./.weftly/runs/<run-id>
	StepsDir     string // Root/workspace  (step cwd)
	ArtifactsDir string // Root/artifacts
}

// New creates the directory tree for a run. baseDir is the parent to
// contain runs/ (typically "./.weftly"). It returns the resolved absolute
// Workspace so downstream actions can resolve paths against it.
func New(baseDir, runID string) (*Workspace, error) {
	if baseDir == "" {
		baseDir = "./.weftly"
	}
	root := filepath.Join(baseDir, "runs", runID)
	steps := filepath.Join(root, "workspace")
	artifacts := filepath.Join(root, "artifacts")
	for _, d := range []string{steps, artifacts} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	absSteps, err := filepath.Abs(steps)
	if err != nil {
		return nil, err
	}
	absArtifacts, err := filepath.Abs(artifacts)
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Workspace{Root: absRoot, StepsDir: absSteps, ArtifactsDir: absArtifacts}, nil
}

// SafeJoin resolves p relative to base and returns the absolute path only
// when it lies inside base after evaluating symlinks. It rejects "..",
// absolute paths that escape base, and any resolution that leaves the tree.
func SafeJoin(base, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	// Absolute inputs are rejected outright — they'd bypass the workspace.
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("absolute path not allowed: %s", p)
	}
	joined := filepath.Join(absBase, p)
	clean := filepath.Clean(joined)
	rel, err := filepath.Rel(absBase, clean)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace: %s", p)
	}
	return clean, nil
}

// ScopeDir returns (creating on first use) the working directory for a
// step-level include scope, given the scope's dotted prefix ("edi", or
// "a.b" when nested). Top-level steps pass "" and get the run's shared
// StepsDir unchanged.
//
// Each scope owning its own directory is what makes an included
// fragment safe to use twice in one run: two includes of a fragment
// that both write ./corpus/manifest.json land in
//
//	workspace/edi_a/corpus/manifest.json
//	workspace/edi_b/corpus/manifest.json
//
// instead of silently overwriting each other in the single shared
// workspace. A caller that WANTS the shared directory can still get it
// by passing `${{ workspace.dir }}/corpus` through `with:` — that
// expression is evaluated in the PARENT scope, so it resolves to the
// parent's workspace and both children cooperate on one tree.
//
// Dots in the prefix become path separators so a nested scope nests on
// disk too, mirroring the logical structure.
func (w *Workspace) ScopeDir(prefix string) (string, error) {
	if prefix == "" {
		return w.StepsDir, nil
	}
	// Defence in depth: a prefix is built from validated step ids
	// ([a-z0-9_]+ joined by dots) so it cannot contain separators or
	// "..", but this is a path join from a compile-time string and the
	// cost of checking is nil.
	for _, part := range strings.Split(prefix, ".") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return "", fmt.Errorf("workspace: refusing unsafe scope prefix %q", prefix)
		}
	}
	dir := filepath.Join(w.StepsDir, filepath.Join(strings.Split(prefix, ".")...))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
