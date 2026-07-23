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
	// production; log emits a warning on startup).
	Token string
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
	cfg  Config
	log  *slog.Logger
	cat  *catalogue
	runs *runManager
	srv  *http.Server
	auth Authenticator
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
	if cfg.Token == "" {
		cfg.Logger.Warn("server: no Token configured; all requests will be accepted (do not do this in production)")
	}
	cat, err := loadCatalogue(cfg.CatalogueDir)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:  cfg,
		log:  cfg.Logger,
		cat:  cat,
		runs: newRunManager(cfg.RunsDir, cfg.Logger),
		auth: BearerToken(cfg.Token),
	}, nil
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
		}
	}()
	defer signal.Stop(hup)

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

// Addr returns the bound address, if the server is running.
func (s *Server) Addr() string {
	if s.srv == nil {
		return s.cfg.Addr
	}
	return s.srv.Addr
}
