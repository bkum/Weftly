package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/workspace"
	"gopkg.in/yaml.v3"
)

func init() { Register(&uploadAction{}) }

// uploadAction collects one or more workspace files into the run's
// artifacts directory. `path` may be a single file or a glob; both are
// validated to resolve inside the workspace.
type uploadAction struct{}

func (uploadAction) Type() string { return "upload" }

func (uploadAction) Validate(cfg StepConfig) error {
	if cfg == nil || cfg.Kind != yaml.MappingNode {
		return errors.New("upload: config must be a mapping")
	}
	if findChild(cfg, "path") == nil {
		return errors.New("upload: `path:` required")
	}
	return nil
}

func (uploadAction) Run(ctx context.Context, sc *StepContext) (Outputs, error) {
	pathNode := findChild(sc.Config, "path")
	nameNode := findChild(sc.Config, "name")
	p, err := interpString(sc, pathNode)
	if err != nil {
		return nil, err
	}
	label := p
	if nameNode != nil {
		s, err := interpString(sc, nameNode)
		if err != nil {
			return nil, err
		}
		if s != "" {
			label = s
		}
	}

	// Validate the glob root stays inside the workspace.
	// A user-supplied glob like `./out/**` becomes `<workdir>/out/**`.
	absPattern, err := workspace.SafeJoin(sc.Workdir, p)
	if err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(absPattern)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("upload: no files matched %s", p)
	}
	if err := os.MkdirAll(sc.ArtifactsDir, 0o755); err != nil {
		return nil, err
	}
	var totalSize int64
	for _, m := range matches {
		// After glob resolution, double-check each result is still inside
		// the workspace (symlinks etc.).
		if rel, err := filepath.Rel(sc.Workdir, m); err != nil || rel == ".." || (len(rel) >= 3 && rel[:3] == "../") {
			return nil, fmt.Errorf("upload: refused %s (outside workspace)", m)
		}
		fi, err := os.Stat(m)
		if err != nil {
			return nil, err
		}
		if fi.IsDir() {
			continue
		}
		dst := filepath.Join(sc.ArtifactsDir, filepath.Base(m))
		if err := copyFile(m, dst); err != nil {
			return nil, err
		}
		// Mirror to the configured remote store (server-mode deployments).
		// The local copy is authoritative for report embedding; a remote
		// mirror failure logs and continues rather than failing the run —
		// the artifact is still available on disk.
		if sc.ArtifactStore != nil {
			if err := mirrorToStore(ctx, sc, dst, fi.Size()); err != nil {
				sc.Log(events.Info, fmt.Sprintf("artifact store: mirror of %s failed: %v", filepath.Base(m), err))
			}
		}
		sc.Emit(events.ArtifactUploaded{Name: label, Path: dst, Size: fi.Size()})
		totalSize += fi.Size()
	}
	return Outputs{"count": len(matches), "size": totalSize}, nil
}

// mirrorToStore reads a local artifact file back and Puts it into the
// configured remote store under key "<run-id>/<basename>". Called only
// when sc.ArtifactStore is non-nil. Honours the step context so a
// cancelled run also cancels any in-flight S3 upload — previously
// the upload kept going against context.Background() long after the
// operator had already hit DELETE /runs/{id}.
func mirrorToStore(ctx context.Context, sc *StepContext, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	key := filepath.Base(path)
	if sc.RunID != "" {
		key = sc.RunID + "/" + key
	}
	return sc.ArtifactStore.Put(ctx, key, f, size, "")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
