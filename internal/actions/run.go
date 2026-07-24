package actions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bkum/weftly/internal/events"
	"gopkg.in/yaml.v3"
)

func init() { Register(&runAction{}) }

// runAction is the shell escape hatch. It:
//   - creates a temp $WEFTLY_OUTPUT file, parsed on success for step outputs;
//   - passes step env vars (already ${{ }}-resolved by the engine) as env,
//     never string-interpolated into the script body (spec §7.3);
//   - streams stdout/stderr line-by-line, masking secrets;
//   - honors per-step timeout with process-group termination.
type runAction struct{}

func (runAction) Type() string { return "run" }

func (runAction) Validate(cfg StepConfig) error {
	if cfg == nil {
		return errors.New("run: script body is required")
	}
	// Accept either a scalar string body or a mapping with `script:` etc.
	// For M4 we only support the scalar shell body per Appendix A.
	if body := scriptBody(cfg); body == "" {
		return errors.New("run: script body must be a non-empty string")
	}
	return nil
}

func (r runAction) Run(ctx context.Context, sc *StepContext) (Outputs, error) {
	body := scriptBody(sc.Config)
	if body == "" {
		return nil, errors.New("run: empty script")
	}
	if sc.Strict && strings.Contains(body, "${{") {
		return nil, fmt.Errorf("run: inline ${{ ... }} in script body is forbidden under --strict (step %q); pass values via env instead", sc.StepID)
	}
	if strings.Contains(body, "${{") {
		sc.Log(events.Info, "warning: inline ${{ ... }} in run: body — prefer env: for safety (spec §7.3)")
	}

	outputFile, err := os.CreateTemp("", "weftly-output-*.env")
	if err != nil {
		return nil, fmt.Errorf("run: create output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	// container mounts a bind file with -v; the mount inside the guest
	// must be writable by whatever uid the image runs as. 0666 keeps
	// this simple without needing --userns.
	_ = os.Chmod(outputPath, 0o666)
	defer os.Remove(outputPath)

	// Materialize the script body to a temp file rather than passing via -c,
	// so heredocs and quoting work identically to the file the user wrote.
	scriptFile, err := os.CreateTemp("", "weftly-script-*.sh")
	if err != nil {
		return nil, fmt.Errorf("run: create script file: %w", err)
	}
	scriptPath := scriptFile.Name()
	if _, err := scriptFile.WriteString(body); err != nil {
		_ = scriptFile.Close()
		os.Remove(scriptPath)
		return nil, err
	}
	_ = scriptFile.Close()
	defer os.Remove(scriptPath)

	var cmd *exec.Cmd
	if sc.Container != "" {
		cmd, err = buildContainerCmd(ctx, sc, scriptPath, outputPath)
		if err != nil {
			return nil, err
		}
	} else {
		shell, shellArg := resolveShell(sc.Shell)
		if shell == "" {
			return nil, errors.New("run: no shell available on PATH (tried bash, sh)")
		}
		var cmdArgs []string
		if shellArg != "" {
			cmdArgs = append(cmdArgs, shellArg)
		}
		cmdArgs = append(cmdArgs, scriptPath)
		cmd = exec.CommandContext(ctx, shell, cmdArgs...)
		cmd.Dir = sc.Workdir
		cmd.Env = buildEnv(sc, outputPath)
	}
	// Platform-specific process-group setup (POSIX: Setpgid=true) +
	// cancel hook (POSIX: SIGKILL to -pgid; Windows: default cmd.Cancel
	// which kills the process). Splitting these behind build tags keeps
	// the file compilable for windows/amd64 in the release build —
	// syscall.Kill / SysProcAttr.Setpgid don't exist there and no
	// runtime `if runtime.GOOS != "windows"` guard can hide the missing
	// symbols from the compiler.
	setupProcessGroup(cmd)
	// WaitDelay force-closes the stdio pipes if a grandchild survives
	// the process-group kill and keeps them open (e.g. `sleep 5` inside
	// a killed bash). exec joins on the internal copy goroutines
	// created for cmd.Stdout / cmd.Stderr; WaitDelay bounds how long
	// Wait will block waiting for them.
	cmd.WaitDelay = 2 * time.Second

	// Attach line-buffered writers directly. This is deliberately NOT
	// StdoutPipe/StderrPipe: those docs warn that Wait closes the pipe
	// once the process exits, racing any concurrent reader — under load
	// (or GOMAXPROCS=1 + race detector, seen on CI) that race drops
	// entire log lines and the "expected masked line" assertion in
	// TestRunActionMasksSecretsInLogs fails intermittently. Assigning
	// cmd.Stdout / cmd.Stderr instead makes exec spawn the copy
	// goroutine internally, and Wait joins on that goroutine before
	// returning — so every byte the child wrote is delivered.
	stdoutLW := newLineWriter(events.Stdout, sc)
	stderrLW := newLineWriter(events.Stderr, sc)
	cmd.Stdout = stdoutLW
	cmd.Stderr = stderrLW

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	runErr := cmd.Wait()
	// Flush any partial trailing line (no final newline) so `printf 'x'`
	// still surfaces — same guarantee the old bufio.Scanner path gave.
	stdoutLW.flush()
	stderrLW.flush()

	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("run: timed out after %s", sc.Timeout)
	}
	if runErr != nil {
		// non-zero exit — do NOT parse outputs (spec §7.2)
		return nil, fmt.Errorf("run: exit %s", exitDescription(runErr))
	}
	outs, err := parseOutputFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("run: parse outputs: %w", err)
	}
	for k, v := range outs {
		sc.Emit(events.StepOutput{StepID: sc.StepID, Key: k, Value: v})
	}
	return outs, nil
}

// scriptBody returns the script text for the run action. Accepts a YAML
// scalar or a mapping with `script:`.
func scriptBody(cfg StepConfig) string {
	if cfg == nil {
		return ""
	}
	switch cfg.Kind {
	case yaml.ScalarNode:
		return cfg.Value
	case yaml.MappingNode:
		for i := 0; i < len(cfg.Content); i += 2 {
			if cfg.Content[i].Value == "script" && cfg.Content[i+1].Kind == yaml.ScalarNode {
				return cfg.Content[i+1].Value
			}
		}
	}
	return ""
}

func resolveShell(pref string) (string, string) {
	candidates := []string{pref}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, "pwsh", "powershell", "cmd")
	} else {
		candidates = append(candidates, "bash", "sh")
	}
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			if strings.EqualFold(filepath.Base(name), "cmd") {
				return p, "/C"
			}
			if strings.EqualFold(filepath.Base(name), "pwsh") || strings.EqualFold(filepath.Base(name), "powershell") {
				return p, "-File"
			}
			return p, ""
		}
	}
	return "", ""
}

func buildEnv(sc *StepContext, outputPath string) []string {
	base := os.Environ()
	// Overlay step/workflow env last so it wins.
	env := append([]string{}, base...)
	env = append(env, "WEFTLY_OUTPUT="+outputPath)
	env = append(env, "WEFTLY_STEP_ID="+sc.StepID)
	env = append(env, "WEFTLY_WORKSPACE="+sc.Workdir)
	for k, v := range sc.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// lineWriter is an io.Writer that splits incoming bytes on '\n' and
// emits one StepLog per line, masking secrets first. exec's internal
// copy goroutine calls Write repeatedly, so we buffer partial lines
// across calls and flush the trailing remainder from Run() after Wait
// returns.
//
// This replaces the previous StdoutPipe+bufio.Scanner path, which
// leaked lines under race-detector + GOMAXPROCS=1 because cmd.Wait
// closes the pipe once the process exits and the scanner goroutine
// wasn't joined.
type lineWriter struct {
	stream events.Stream
	sc     *StepContext
	buf    bytes.Buffer
}

func newLineWriter(stream events.Stream, sc *StepContext) *lineWriter {
	return &lineWriter{stream: stream, sc: sc}
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf.Write(p)
	for {
		b := lw.buf.Bytes()
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		line := string(b[:i])
		lw.buf.Next(i + 1)
		lw.emit(line)
	}
	return len(p), nil
}

// flush emits any pending partial line (no trailing newline). Called
// once after cmd.Wait so `printf 'x'` still shows up.
func (lw *lineWriter) flush() {
	if lw.buf.Len() == 0 {
		return
	}
	lw.emit(lw.buf.String())
	lw.buf.Reset()
}

func (lw *lineWriter) emit(line string) {
	if lw.sc.Secrets != nil {
		line = lw.sc.Secrets.Mask(line)
	}
	lw.sc.Emit(events.StepLog{StepID: lw.sc.StepID, Stream: lw.stream, Line: line})
}

// parseOutputFile parses the $WEFTLY_OUTPUT file: `key=value` lines and
// `key<<DELIM\n...multiline...\nDELIM` heredoc blocks (GitHub-Actions style).
func parseOutputFile(path string) (Outputs, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Outputs{}, nil
		}
		return nil, err
	}
	defer f.Close()
	out := Outputs{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// heredoc form: KEY<<DELIM
		if idx := strings.Index(line, "<<"); idx > 0 && !strings.Contains(line[:idx], "=") {
			key := strings.TrimSpace(line[:idx])
			delim := strings.TrimSpace(line[idx+2:])
			if key == "" || delim == "" {
				return nil, fmt.Errorf("malformed heredoc line: %q", line)
			}
			var buf strings.Builder
			for scanner.Scan() {
				l := scanner.Text()
				if strings.TrimSpace(l) == delim {
					out[key] = buf.String()
					break
				}
				if buf.Len() > 0 {
					buf.WriteByte('\n')
				}
				buf.WriteString(l)
			}
			continue
		}
		// key=value form
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("malformed output line (want key=value): %q", line)
		}
		key := strings.TrimSpace(line[:eq])
		val := line[eq+1:]
		out[key] = coerce(val)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// coerce turns "true"/"false"/int/float scalars from the output file into
// typed values so that `if: ${{ !steps.<id>.outputs.exists }}` behaves.
// Values that don't parse cleanly stay as strings.
func coerce(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	return s
}

func exitDescription(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("code %d", ee.ExitCode())
	}
	return err.Error()
}

// Platform-specific process-group setup + cancel hook live in
// run_unix.go / run_windows.go behind build tags — syscall.Kill and
// SysProcAttr.Setpgid aren't defined on Windows.
