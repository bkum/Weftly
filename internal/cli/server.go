package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bkum/weftly/internal/artifacts"
	"github.com/bkum/weftly/internal/server"
	"github.com/spf13/cobra"
)

func newServerCmd() *cobra.Command {
	var (
		addr          string
		dir           string
		runsDir       string
		token         string
		authFile      string
		s3Endpoint    string
		s3Region      string
		s3Bucket      string
		s3Prefix      string
		s3AccessKey   string
		s3SecretKey   string
		s3Plaintext   bool
		schedulesFile string
		auditFile     string
	)
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Serve the curated workflow catalogue over REST + SSE (Phase 2)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("WEFTLY_TOKEN")
			}
			var s3 *artifacts.S3Config
			if s3Bucket != "" || s3Endpoint != "" {
				if s3AccessKey == "" {
					s3AccessKey = os.Getenv("WEFTLY_S3_ACCESS_KEY")
				}
				if s3SecretKey == "" {
					s3SecretKey = os.Getenv("WEFTLY_S3_SECRET_KEY")
				}
				s3 = &artifacts.S3Config{
					Endpoint:  s3Endpoint,
					Region:    s3Region,
					Bucket:    s3Bucket,
					KeyPrefix: s3Prefix,
					AccessKey: s3AccessKey,
					SecretKey: s3SecretKey,
					UseSSL:    !s3Plaintext,
				}
			}
			srv, err := server.New(server.Config{
				Addr:            addr,
				CatalogueDir:    dir,
				RunsDir:         runsDir,
				Token:           token,
				AuthFile:        authFile,
				S3:              s3,
				SchedulesFile:   schedulesFile,
				AuditFile:       auditFile,
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
	cmd.Flags().StringVar(&token, "token", "", "single bearer token (or WEFTLY_TOKEN env); ignored when --auth-file is set")
	cmd.Flags().StringVar(&authFile, "auth-file", "", "YAML file with multi-token → role → workflow allowlist (RBAC)")
	cmd.Flags().StringVar(&s3Endpoint, "s3-endpoint", "", "S3-compatible endpoint host (e.g. s3.amazonaws.com, minio.internal:9000)")
	cmd.Flags().StringVar(&s3Region, "s3-region", "", "S3 region (optional for MinIO)")
	cmd.Flags().StringVar(&s3Bucket, "s3-bucket", "", "S3 bucket to mirror artifacts into")
	cmd.Flags().StringVar(&s3Prefix, "s3-prefix", "", "S3 key prefix (namespaces runs within a shared bucket)")
	cmd.Flags().StringVar(&s3AccessKey, "s3-access-key", "", "S3 access key (or WEFTLY_S3_ACCESS_KEY env)")
	cmd.Flags().StringVar(&s3SecretKey, "s3-secret-key", "", "S3 secret key (or WEFTLY_S3_SECRET_KEY env)")
	cmd.Flags().BoolVar(&s3Plaintext, "s3-plaintext", false, "talk http to the S3 endpoint (dev-only, e.g. local MinIO)")
	cmd.Flags().StringVar(&schedulesFile, "schedules", "", "path to schedules.yaml (enables cron-driven runs); reload with SIGHUP or POST /reload")
	cmd.Flags().StringVar(&auditFile, "audit-file", "", "append-only JSON-lines audit log of mutating requests; empty = in-memory only, exposed at GET /audit (admin)")
	return cmd
}
