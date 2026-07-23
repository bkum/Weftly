package actions

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/bkum/weftly/internal/events"
)

// buildContainerCmd wraps the step's shell script in a `podman run` /
// `docker run` invocation instead of executing it on the host. Contract
// mirrors the host path:
//
//   - Workspace bind-mounted at /weftly/workspace (guest CWD).
//   - Script bind-mounted read-only at /weftly/script and executed with sh.
//   - $WEFTLY_OUTPUT file bind-mounted r/w at /weftly/output.env; the guest
//     writes step outputs there just as it would on the host, and the parent
//     parses the same file after cmd.Wait.
//   - Step env vars, including WEFTLY_STEP_ID / WEFTLY_WORKSPACE, are passed
//     via `-e KEY=VAL`. Never inlined into the script body (spec §7.3).
//
// Backend selection: podman preferred (rootless-friendlier default), docker
// as fallback. The selection is logged once via sc.Log so operators can
// spot which one ran without turning on verbose exec tracing.
func buildContainerCmd(ctx context.Context, sc *StepContext, scriptPath, outputPath string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("run: container: not supported on windows in this build")
	}
	engine, engineName, err := resolveContainerEngine()
	if err != nil {
		return nil, err
	}

	const (
		guestWs     = "/weftly/workspace"
		guestScript = "/weftly/script"
		guestOutput = "/weftly/output.env"
	)

	args := []string{
		"run", "--rm",
		// Isolate by default: no network unless the workflow author
		// explicitly re-enables it in a later revision. Networked
		// containers can exfil to arbitrary endpoints and defeat the
		// "curated catalogue" trust model.
		"--network=none",
		"-v", sc.Workdir + ":" + guestWs,
		"-v", scriptPath + ":" + guestScript + ":ro",
		"-v", outputPath + ":" + guestOutput,
		"-w", guestWs,
		"-e", "WEFTLY_OUTPUT=" + guestOutput,
		"-e", "WEFTLY_STEP_ID=" + sc.StepID,
		"-e", "WEFTLY_WORKSPACE=" + guestWs,
	}
	// Sort env keys so the invocation is deterministic — easier to
	// eyeball and trivially unit-testable.
	keys := make([]string, 0, len(sc.Env))
	for k := range sc.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !validEnvKey(k) {
			return nil, fmt.Errorf("run: container: env key %q contains characters that %s -e cannot pass safely", k, engineName)
		}
		args = append(args, "-e", k+"="+sc.Env[k])
	}
	args = append(args, sc.Container, "sh", guestScript)

	sc.Log(events.Stderr, fmt.Sprintf("weftly: container backend=%s image=%s", engineName, sc.Container))

	cmd := exec.CommandContext(ctx, engine, args...)
	// Host cwd is irrelevant to the guest — pin it to something valid so
	// callers that inspect cmd.Dir aren't surprised by an empty string.
	cmd.Dir = filepath.Dir(scriptPath)
	return cmd, nil
}

// resolveContainerEngine finds podman first, then docker. Absent both,
// returns an actionable error rather than silently degrading — a workflow
// that opted into a container image expects containment.
func resolveContainerEngine() (path, name string, err error) {
	for _, candidate := range []string{"podman", "docker"} {
		if p, lookErr := exec.LookPath(candidate); lookErr == nil {
			return p, candidate, nil
		}
	}
	return "", "", errors.New("run: container: neither podman nor docker found on PATH; install one or drop the container: field")
}

// validEnvKey rejects env keys that could confuse the CLI parser
// (embedded '=', whitespace, unicode). POSIX identifier characters only.
func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
