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
