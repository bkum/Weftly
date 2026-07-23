// Package server is the Phase 2 REST + SSE + embedded-SPA front-end over
// the same engine core the CLI uses. It only ever runs workflows from a
// configured catalogue directory — never arbitrary YAML submitted by a
// caller — so the trust boundary stays "who can commit to the catalogue"
// (spec §16).
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bkum/weftly/internal/artifacts"
	"github.com/bkum/weftly/internal/scheduler"
)

// Config configures a server instance.
type Config struct {
	// Addr is the TCP listen address (e.g. ":8080").
	Addr string
	// CatalogueDir is a directory scanned for *.yml / *.yaml workflow
	// files. Nothing outside it is runnable.
	CatalogueDir string
	// RunsDir is the parent under which per-run state lives (matches the
	// CLI's ./.weftly convention).
	RunsDir string
	// Token is the bearer token required in the `Authorization: Bearer
	// <token>` header. Empty disables auth (test only — never do this in
	// production; log emits a warning on startup). Ignored when AuthFile
	// is set.
	Token string
	// AuthFile points at a weftly.yaml with a multi-token → role → workflow
	// allowlist mapping (see LoadAuthFile). When set, it supersedes Token
	// and full RBAC applies.
	AuthFile string
	// S3 is optional. When set, the upload action mirrors each artifact
	// to this bucket and GET /runs/:id/artifacts/:name falls back to it
	// when the local file is absent (e.g. after local retention pruning).
	S3 *artifacts.S3Config
	// SchedulesFile points at schedules.yaml. When set and non-empty
	// the server launches a scheduler goroutine that dispatches workflows
	// on their cron cadence (spec §17). Empty disables scheduling.
	SchedulesFile string
	// MaxBodyBytes caps request bodies to prevent trivial DoS.
	// Default 1 MiB when zero.
	MaxBodyBytes int64
	// ShutdownTimeout is the grace period the server gives in-flight
	// requests during a graceful stop. Default 15s.
	ShutdownTimeout time.Duration
	// Logger receives structured server logs. Defaults to slog.Default().
	Logger *slog.Logger
}

// Server is a running or startable HTTP server.
type Server struct {
	cfg   Config
	log   *slog.Logger
	cat   *catalogue
	runs  *runManager
	srv   *http.Server
	auth  Authenticator
	store artifacts.Store      // nil unless Config.S3 is set
	sched *scheduler.Scheduler // nil unless Config.SchedulesFile is set
}

// New builds a Server. It loads the catalogue eagerly so mis-configuration
// is caught before Listen.
func New(cfg Config) (*Server, error) {
	if cfg.CatalogueDir == "" {
		return nil, errors.New("server: CatalogueDir is required")
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.RunsDir == "" {
		cfg.RunsDir = "./.weftly"
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = 15 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	auth, err := chooseAuthenticator(cfg)
	if err != nil {
		return nil, err
	}
	cat, err := loadCatalogue(cfg.CatalogueDir)
	if err != nil {
		return nil, err
	}
	var store artifacts.Store
	if cfg.S3 != nil {
		s3, err := artifacts.NewS3(*cfg.S3)
		if err != nil {
			return nil, err
		}
		store = s3
		cfg.Logger.Info("server: S3 artifact store enabled",
			"endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket, "prefix", cfg.S3.KeyPrefix)
	}
	s := &Server{
		cfg:   cfg,
		log:   cfg.Logger,
		cat:   cat,
		runs:  newRunManager(cfg.RunsDir, cfg.Logger, store),
		auth:  auth,
		store: store,
	}
	if cfg.SchedulesFile != "" {
		if err := s.initScheduler(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// initScheduler loads schedules.yaml (or no-ops if the file is missing —
// operators can create it later and SIGHUP-reload) and wires the
// dispatch callback to the run manager. It does NOT start the ticker;
// ListenAndServe does that once the HTTP server is listening.
func (s *Server) initScheduler() error {
	f, err := scheduler.LoadFile(s.cfg.SchedulesFile)
	if err != nil {
		return fmt.Errorf("server: schedules: %w", err)
	}
	entries := []scheduler.Entry{}
	if f != nil {
		entries = f.Schedules
	}
	sc := scheduler.New(s.log, func(ctx context.Context, wf string, inputs map[string]any) (string, error) {
		entry := s.cat.get(wf)
		if entry == nil {
			return "", fmt.Errorf("scheduled workflow %q not in catalogue", wf)
		}
		rec, err := s.runs.start(ctx, wf, entry.Workflow, inputs)
		if err != nil {
			return "", err
		}
		return rec.ID, nil
	})
	if err := sc.SetSchedules(entries, time.Now()); err != nil {
		return fmt.Errorf("server: schedules: %w", err)
	}
	s.sched = sc
	s.log.Info("server: scheduler enabled",
		"file", s.cfg.SchedulesFile,
		"entries", len(entries),
	)
	return nil
}

// chooseAuthenticator picks between the single-token (Bearer) and the
// file-driven RBAC backends. AuthFile always wins when present; otherwise
// Token is used; otherwise the empty-token permissive mode logs a warning.
func chooseAuthenticator(cfg Config) (Authenticator, error) {
	if cfg.AuthFile != "" {
		af, err := LoadAuthFile(cfg.AuthFile)
		if err != nil {
			return nil, err
		}
		if af == nil {
			return nil, fmt.Errorf("server: AuthFile %q does not exist", cfg.AuthFile)
		}
		cfg.Logger.Info("server: RBAC enabled",
			"tokens", len(af.Tokens),
			"roles", len(af.Roles),
		)
		return RBACFromFile(af), nil
	}
	if cfg.Token == "" {
		cfg.Logger.Warn("server: no Token or AuthFile configured; all requests will be accepted (do not do this in production)")
	}
	return BearerToken(cfg.Token), nil
}

// Handler returns the http.Handler with all routes wired.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// API
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /workflows", s.handleListWorkflows)
	mux.HandleFunc("GET /workflows/{id}", s.handleGetWorkflow)
	mux.HandleFunc("GET /runs", s.handleListRuns)
	mux.HandleFunc("POST /runs", s.handleCreateRun)
	mux.HandleFunc("GET /runs/{id}", s.handleGetRun)
	mux.HandleFunc("GET /runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /runs/{id}/artifacts/{name}", s.handleArtifact)
	mux.HandleFunc("POST /reload", s.handleReload)
	mux.HandleFunc("GET /schedules", s.handleListSchedules)
	mux.HandleFunc("GET /schedules/{id}", s.handleGetSchedule)
	mux.HandleFunc("POST /schedules/{id}/trigger", s.handleTriggerSchedule)
	// UI (unauthenticated shell; the SPA does authenticated API calls)
	ui := uiHandler()
	mux.Handle("GET /", ui)
	mux.Handle("GET /app.js", ui)
	mux.Handle("GET /styles.css", ui)
	mux.Handle("GET /favicon.ico", ui)

	// Order matters: request-size cap first so any handler that reads the
	// body benefits; then auth. /healthz and the SPA shell paths stay
	// unauthenticated so external probes and initial page loads work.
	var h http.Handler = mux
	h = withMaxBody(h, s.cfg.MaxBodyBytes)
	h = withAuth(h, s.auth, s.log, "/healthz", "/", "/app.js", "/styles.css", "/favicon.ico")
	h = withAccessLog(h, s.log)
	return h
}

// ListenAndServe starts the server and blocks until it stops. Context
// cancellation triggers a graceful shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	l, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // SSE needs unbounded writes; per-request timeouts guard non-SSE handlers.
		IdleTimeout:       60 * time.Second,
	}
	s.log.Info("server listening", "addr", l.Addr().String(), "catalogue", s.cfg.CatalogueDir, "workflows", len(s.cat.list()))

	// SIGHUP reloads the catalogue in place (same as POST /reload).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := s.cat.reload(s.cfg.CatalogueDir); err != nil {
				s.log.Error("SIGHUP reload failed", "err", err)
				continue
			}
			s.log.Info("SIGHUP reload", "workflows", len(s.cat.list()))
			if s.sched != nil {
				if err := s.reloadSchedules(); err != nil {
					s.log.Error("SIGHUP schedule reload failed", "err", err)
				}
			}
		}
	}()
	defer signal.Stop(hup)

	if s.sched != nil {
		go s.sched.Run(ctx)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(l) }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := s.srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// reloadSchedules re-reads schedules.yaml and swaps the in-memory entry
// set. Called from SIGHUP and (optionally) POST /reload — same failure
// semantics: a bad file leaves the running scheduler untouched.
func (s *Server) reloadSchedules() error {
	f, err := scheduler.LoadFile(s.cfg.SchedulesFile)
	if err != nil {
		return err
	}
	entries := []scheduler.Entry{}
	if f != nil {
		entries = f.Schedules
	}
	if err := s.sched.SetSchedules(entries, time.Now()); err != nil {
		return err
	}
	s.log.Info("schedules reloaded", "entries", len(entries))
	return nil
}

// Addr returns the bound address, if the server is running.
func (s *Server) Addr() string {
	if s.srv == nil {
		return s.cfg.Addr
	}
	return s.srv.Addr
}
