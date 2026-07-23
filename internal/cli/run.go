package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	// register built-in actions via their init() side effects
	_ "github.com/bkum/weftly/internal/actions"

	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/render/tty"
	"github.com/bkum/weftly/internal/schema"
	"github.com/bkum/weftly/internal/secrets"
	"github.com/bkum/weftly/internal/tracing"
	"github.com/spf13/cobra"
	"log/slog"
)

func newRunCmd() *cobra.Command {
	var (
		inputs       []string
		inputFile    string
		vars         []string
		dryRun       bool
		jsonOutput   bool
		noColor      bool
		strict       bool
		autoYes      bool
		parallel     int
		resume       string
		ciMode       bool
		otelEndpoint string
	)
	cmd := &cobra.Command{
		Use:   "run <workflow.yml>",
		Short: "Execute a workflow (default verb)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wf, err := schema.Load(args[0])
			if err != nil {
				return err
			}
			if err := schema.Validate(wf); err != nil {
				return err
			}

			supplied, err := engine.ParseKV(inputs)
			if err != nil {
				return err
			}
			if inputFile != "" {
				more, err := loadInputFile(inputFile)
				if err != nil {
					return err
				}
				for k, v := range more {
					if _, exists := supplied[k]; !exists {
						supplied[k] = v
					}
				}
			}
			varOverrides, err := engine.ParseKVString(vars)
			if err != nil {
				return err
			}

			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "workflow: %s\nsteps:\n", wf.Name)
				for i, s := range wf.Steps {
					name := s.Name
					if name == "" {
						name = s.ID
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %d. [%s] %s\n", i+1, s.ActionType, name)
				}
				return nil
			}

			tracing.Init(otelEndpoint, slog.New(slog.NewTextHandler(os.Stderr, nil)))
			defer func() {
				sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = tracing.Shutdown(sctx)
			}()

			bus := events.NewBus()
			sec := secrets.NewRegistry()
			// Renderer is bound to the same registry the engine populates as
			// inputs resolve; because we subscribe before Run, every event
			// passes through Mask before hitting stdout.
			switch {
			case jsonOutput:
				r := tty.NewJSON(cmd.OutOrStdout(), sec)
				bus.Subscribe(r.Handle)
			case ciMode:
				r := tty.NewCI(cmd.OutOrStdout(), sec)
				bus.Subscribe(r.Handle)
			default:
				r := tty.New(cmd.OutOrStdout(), !noColor && isTTY(os.Stdout), sec)
				bus.Subscribe(r.Handle)
			}

			// Pre-register any obviously-secret supplied values so that
			// masking is active even before engine.Run resolves them.
			for name, v := range supplied {
				for wname, in := range wf.Inputs {
					if wname == name && in.Secret {
						if s, ok := v.(string); ok {
							sec.Register(s)
						}
					}
				}
			}

			res, err := engine.Run(context.Background(), wf, engine.Options{
				Strict:   strict,
				AutoYes:  autoYes,
				Parallel: parallel,
				Resume:   resume,
				Inputs:   supplied,
				Vars:     varOverrides,
				Bus:      bus,
			})
			if err != nil {
				return err
			}
			if !jsonOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "\nstate:  %s\nreport: %s\n", res.StateFile, res.ReportFile)
			}
			if code := res.ExitCode(); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "supply an input k=v (repeatable)")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "supply inputs from a YAML/JSON file")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "override workflow env k=v (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compile, validate, print plan; execute nothing")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the event stream as JSON")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "plain output")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat inline expr-in-run as an error")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "auto-answer 'yes' to every prompt(type:confirm) step")
	cmd.Flags().IntVarP(&parallel, "parallel", "p", 4, "maximum concurrent steps (needs edges are always honored)")
	cmd.Flags().StringVar(&resume, "resume", "", "resume a prior run by id (or state.json path); skips successful steps")
	cmd.Flags().BoolVar(&ciMode, "ci", false, "CI-friendly output: no color, GitHub Actions style ::group::/::endgroup:: markers around each step")
	cmd.Flags().StringVar(&otelEndpoint, "otel-endpoint", "", "OTLP/HTTP endpoint (e.g. http://collector:4318); enables per-run + per-step span export")
	return cmd
}

func loadInputFile(path string) (map[string]any, error) {
	// Support YAML or JSON via yaml.v3 (which happily parses JSON).
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := yamlUnmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("input-file %s: %w", path, err)
	}
	return m, nil
}
