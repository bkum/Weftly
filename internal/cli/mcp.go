package cli

import (
	// registers built-in actions
	_ "github.com/bkum/weftly/internal/actions"

	"github.com/bkum/weftly/internal/mcp"
	"github.com/spf13/cobra"
)

// newMCPCmd — `weftly mcp --dir ./workflows` serves the catalogue as
// an MCP tool server over stdio. Blocks until stdin closes. Every
// workflow becomes an MCP tool whose call runs the workflow
// synchronously and returns the rendered transcript. Intended for
// IDE-agent bindings (Claude Code, etc.) that spawn the process.
func newMCPCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the workflow catalogue as an MCP tool server over stdio",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcp.Serve(cmd.Context(), mcp.Config{
				Dir: dir,
				In:  cmd.InOrStdin(),
				Out: cmd.OutOrStdout(),
				Err: cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "./workflows", "catalogue directory to expose as MCP tools")
	return cmd
}
