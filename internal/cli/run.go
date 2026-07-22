package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var (
		inputs     []string
		inputFile  string
		vars       []string
		dryRun     bool
		jsonOutput bool
		noColor    bool
		strict     bool
	)
	cmd := &cobra.Command{
		Use:   "run <workflow.yml>",
		Short: "Execute a workflow (default verb)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = inputs
			_ = inputFile
			_ = vars
			_ = dryRun
			_ = jsonOutput
			_ = noColor
			_ = strict
			return errors.New("run: not implemented yet (M4)")
		},
	}
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "supply an input k=v (repeatable)")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "supply inputs from a YAML/JSON file")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "override workflow env k=v (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compile, validate, print plan; execute nothing")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the event stream as JSON")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "plain output")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat inline expr-in-run as an error")
	return cmd
}
