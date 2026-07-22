package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <workflow.yml>",
		Short: "Static validation, no execution",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("validate: not implemented yet (M2)")
		},
	}
}
