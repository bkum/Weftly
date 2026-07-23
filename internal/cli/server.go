package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bkum/weftly/internal/server"
	"github.com/spf13/cobra"
)

func newServerCmd() *cobra.Command {
	var (
		addr    string
		dir     string
		runsDir string
		token   string
	)
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Serve the curated workflow catalogue over REST + SSE (Phase 2)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("WEFTLY_TOKEN")
			}
			srv, err := server.New(server.Config{
				Addr:            addr,
				CatalogueDir:    dir,
				RunsDir:         runsDir,
				Token:           token,
				ShutdownTimeout: 15 * time.Second,
			})
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return srv.ListenAndServe(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&dir, "dir", "./workflows", "catalogue directory (only these workflows can be run)")
	cmd.Flags().StringVar(&runsDir, "runs-dir", "./.weftly", "parent directory for per-run state")
	cmd.Flags().StringVar(&token, "token", "", "bearer token required in Authorization header (or WEFTLY_TOKEN env)")
	return cmd
}
