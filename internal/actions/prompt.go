package actions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bkum/weftly/internal/events"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

func init() { Register(&promptAction{}) }

// promptAction implements spec §6.4. In a TTY it reads the user's answer
// from stdin. In a non-TTY environment it uses `default:` if provided,
// otherwise fails fast — automation must supply required values ahead of
// time (--input on the workflow's declared inputs, or a workflow with
// defaults on its prompt steps).
//
//	prompt:
//	  message: "Proceed against ${{ inputs.env_url }}?"
//	  type: confirm            # confirm | text | password | select
//	  options: [yes, no]       # required if type: select
//	  default: "n"             # used in non-TTY; also pre-filled in TTY
//	  secret: true             # register the answer with the mask registry
//
// Output: steps.<id>.outputs.value (typed as bool for confirm, string
// otherwise).
type promptAction struct{}

type promptConfig struct {
	Message string   `yaml:"message"`
	Type    string   `yaml:"type"`
	Options []string `yaml:"options"`
	Default string   `yaml:"default"`
	Secret  bool     `yaml:"secret"`
}

func (promptAction) Type() string { return "prompt" }

func (promptAction) Validate(cfg StepConfig) error {
	if cfg == nil || cfg.Kind != yaml.MappingNode {
		return errors.New("prompt: config must be a mapping")
	}
	var pc promptConfig
	if err := cfg.Decode(&pc); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	if strings.TrimSpace(pc.Message) == "" {
		return errors.New("prompt: `message:` required")
	}
	switch pc.Type {
	case "", "text", "password", "confirm", "select":
	default:
		return fmt.Errorf("prompt: unknown type %q (want text|password|confirm|select)", pc.Type)
	}
	if pc.Type == "select" && len(pc.Options) == 0 {
		return errors.New("prompt: `options:` required for type: select")
	}
	return nil
}

func (p promptAction) Run(ctx context.Context, sc *StepContext) (Outputs, error) {
	var pc promptConfig
	if err := sc.Config.Decode(&pc); err != nil {
		return nil, fmt.Errorf("prompt decode: %w", err)
	}
	msg, err := sc.Expr.InterpolateString(pc.Message, sc.ExprEnv)
	if err != nil {
		return nil, fmt.Errorf("prompt message: %w", err)
	}
	if pc.Type == "" {
		pc.Type = "text"
	}

	answer, err := p.ask(ctx, sc, msg, pc)
	if err != nil {
		return nil, err
	}
	if pc.Secret && sc.Secrets != nil {
		if s, ok := answer.(string); ok {
			sc.Secrets.Register(s)
		}
	}
	sc.Emit(events.StepOutput{StepID: sc.StepID, Key: "value", Value: answer})
	return Outputs{"value": answer}, nil
}

func (p promptAction) ask(ctx context.Context, sc *StepContext, msg string, pc promptConfig) (any, error) {
	// AutoYes short-circuits every confirm prompt.
	if pc.Type == "confirm" && sc.AutoYes {
		sc.Log(events.Info, fmt.Sprintf("prompt %q auto-answered yes (--yes)", msg))
		return true, nil
	}

	if !isInteractive() {
		if pc.Default == "" {
			return nil, fmt.Errorf("prompt %q: non-interactive session and no default; supply the value or run in a TTY", msg)
		}
		return coerceAnswer(pc.Type, pc.Default, pc.Options)
	}
	// interactive
	return interactiveAsk(ctx, sc, msg, pc)
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// interactiveAsk writes the prompt to stderr (leaving stdout clean for
// --json runs) and reads the answer from stdin. Password type disables
// terminal echo.
func interactiveAsk(_ context.Context, sc *StepContext, msg string, pc promptConfig) (any, error) {
	switch pc.Type {
	case "text":
		writePrompt(msg, pc.Default, "")
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		if line == "" {
			line = pc.Default
		}
		return line, nil

	case "password":
		writePrompt(msg, "", "")
		fd := int(os.Stdin.Fd())
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, err
		}
		s := string(b)
		if s == "" {
			s = pc.Default
		}
		return s, nil

	case "confirm":
		def := pc.Default
		if def == "" {
			def = "n"
		}
		hint := "[y/N]"
		if truthyString(def) {
			hint = "[Y/n]"
		}
		writePrompt(msg, "", hint)
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		if line == "" {
			return truthyString(def), nil
		}
		return truthyString(line), nil

	case "select":
		defIdx := -1
		for i, o := range pc.Options {
			if o == pc.Default {
				defIdx = i
			}
		}
		fmt.Fprintln(os.Stderr, msg)
		for i, o := range pc.Options {
			marker := "  "
			if i == defIdx {
				marker = "▸ "
			}
			fmt.Fprintf(os.Stderr, "%s[%d] %s\n", marker, i+1, o)
		}
		def := ""
		if defIdx >= 0 {
			def = strconv.Itoa(defIdx + 1)
		}
		writePrompt("choice", def, "")
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		if line == "" && def != "" {
			line = def
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(pc.Options) {
			return nil, fmt.Errorf("prompt: invalid choice %q", line)
		}
		return pc.Options[n-1], nil
	}
	return nil, fmt.Errorf("prompt: unhandled type %q", pc.Type)
}

func writePrompt(msg, def, hint string) {
	suffix := ""
	if hint != "" {
		suffix = " " + hint
	} else if def != "" {
		suffix = " (default: " + def + ")"
	}
	fmt.Fprintf(os.Stderr, "%s%s: ", msg, suffix)
}

func readLine() (string, error) {
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func truthyString(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "t", "1":
		return true
	}
	return false
}

func coerceAnswer(t, raw string, options []string) (any, error) {
	switch t {
	case "confirm":
		return truthyString(raw), nil
	case "select":
		for _, o := range options {
			if o == raw {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("prompt default %q not in options %v", raw, options)
	}
	return raw, nil
}
