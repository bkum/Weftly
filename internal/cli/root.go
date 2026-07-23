// Package cli wires the weftly command-line interface. The default verb is
// `run`; `validate`, `list`, and `version` are siblings. Server mode is a
// Phase 2 target and is intentionally absent here.
package cli

import (
	"github.com/spf13/cobra"
)

// Version is stamped at build time via -ldflags "-X ...Version=...".
var (
	Version = "0.1.0-dev"
	Commit  = "none"
	Date    = "unknown"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "weftly",
		Short:         "Weftly — a single-binary workflow/runbook engine",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newRunCmd())
	root.AddCommand(newValidateCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newServerCmd())
	root.AddCommand(newVersionCmd())
	return root
}
