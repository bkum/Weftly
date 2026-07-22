package cli

import (
	"fmt"

	"github.com/bkum/weftly/internal/schema"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <workflow.yml>",
		Short: "Static validation, no execution",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wf, err := schema.Load(args[0])
			if err != nil {
				return err
			}
			if err := schema.Validate(wf); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: ok (%d step(s))\n", args[0], len(wf.Steps))
			return nil
		},
	}
}
