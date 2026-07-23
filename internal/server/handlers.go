package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bkum/weftly/internal/artifacts"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/state"
	"github.com/bkum/weftly/internal/workspace"
)

// --- shared helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "status": status})
}

// --- routes ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "UP", "time": time.Now().UTC()})
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFromContext(r.Context())
	all := s.cat.list()
	// Hide workflows the caller can't run — better UX than 404-ing them
	// only on click.
	visible := make([]*catalogueEntry, 0, len(all))
	for _, e := range all {
		if p.CanRunWorkflow(e.ID) {
			visible = append(visible, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": visible})
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry := s.cat.get(id)
	if entry == nil {
		writeError(w, http.StatusNotFound, "unknown workflow")
		return
	}
	if !PrincipalFromContext(r.Context()).CanRunWorkflow(id) {
		writeError(w, http.StatusForbidden, "workflow not accessible to this principal")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleAudit returns the in-memory tail of the audit log, newest
// first. Admin-only when RBAC is enabled — audit data leaks who ran
// what and shouldn't be visible to non-admin principals.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !PrincipalFromContext(r.Context()).Admin {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	entries := s.audit.Tail()
	// Reverse so the newest is first — that's what an operator
	// scanning the panel actually wants.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleReload re-scans the catalogue directory and swaps the in-memory
// catalogue on success. Same handler as SIGHUP on unix (see server.go).
// Admin-only when RBAC is enabled.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if !PrincipalFromContext(r.Context()).Admin {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	if err := s.cat.reload(s.cfg.CatalogueDir); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("catalogue reloaded", "workflows", len(s.cat.list()))
	resp := map[string]any{"reloaded": true, "workflows": len(s.cat.list())}
	if s.sched != nil {
		if err := s.reloadSchedules(); err != nil {
			writeError(w, http.StatusInternalServerError, "schedules: "+err.Error())
			return
		}
		resp["schedules"] = len(s.sched.States())
	}
	writeJSON(w, http.StatusOK, resp)
}

// runVisibleTo reports whether the principal may access the given run —
// true if its workflow is in the principal's allowlist. Loads the run's
// state.json to discover the workflow id (cheap; already on disk).
func (s *Server) runVisibleTo(runID string, p Principal) bool {
	if p.Admin || p.AllWorkflows {
		return true
	}
	prior, _, err := state.Load(filepath.Join(s.cfg.RunsDir, "runs"), runID)
	if err != nil || prior == nil {
		// If we can't identify the run's workflow, deny by default.
		return false
	}
	return p.CanRunWorkflow(prior.Workflow)
}

type createRunReq struct {
	Workflow string         `json:"workflow"`
	Inputs   map[string]any `json:"inputs"`
}

type createRunResp struct {
	RunID string `json:"run_id"`
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var req createRunReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Workflow) == "" {
		writeError(w, http.StatusBadRequest, "workflow is required")
		return
	}
	entry := s.cat.get(req.Workflow)
	if entry == nil {
		writeError(w, http.StatusNotFound, "unknown workflow: "+req.Workflow)
		return
	}
	if !PrincipalFromContext(r.Context()).CanRunWorkflow(req.Workflow) {
		writeError(w, http.StatusForbidden, "workflow not accessible to this principal")
		return
	}
	rec, err := s.runs.start(r.Context(), req.Workflow, entry.Workflow, req.Inputs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Location", "/runs/"+rec.ID)
	writeJSON(w, http.StatusAccepted, createRunResp{RunID: rec.ID})
}

// handleListRuns returns a summary of every run persisted under
// <RunsDir>/runs/. Optional ?workflow=<id> filter. Sorted newest first
// by the state layer.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	all, err := state.LoadRunsDir(filepath.Join(s.cfg.RunsDir, "runs"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := PrincipalFromContext(r.Context())
	if wf := r.URL.Query().Get("workflow"); wf != "" {
		filtered := all[:0]
		for _, r := range all {
			if r.Workflow == wf {
				filtered = append(filtered, r)
			}
		}
		all = filtered
	}
	// Hide runs whose workflow the caller can't access. Never leak names.
	visible := make([]state.RunSummary, 0, len(all))
	for _, run := range all {
		if p.CanRunWorkflow(run.Workflow) {
			visible = append(visible, run)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": visible})
}

// handleCancelRun cancels an in-flight run by invoking its context
// cancel func — that unblocks each step's exec.CommandContext / http
// request as it runs, and the engine emits StepFinished(status=failed)
// + RunFinished(status=failed) on the normal path. Returns 404 if the
// run isn't tracked in memory (already completed; nothing to cancel).
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.runs.get(id)
	if rec == nil {
		writeError(w, http.StatusNotFound, "unknown run (already finished or purged)")
		return
	}
	if !s.runVisibleTo(id, PrincipalFromContext(r.Context())) {
		writeError(w, http.StatusForbidden, "run not accessible to this principal")
		return
	}
	rec.mu.RLock()
	closed := rec.closed
	cancel := rec.cancel
	rec.mu.RUnlock()
	if closed || cancel == nil {
		// Race with completion — the run already reached RunFinished.
		// Report success rather than 409 to keep the button idempotent.
		writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "already_finished": true})
		return
	}
	cancel()
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": id, "cancelling": true})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.runs.get(id)
	if rec == nil {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	// The freshest state lives on disk (state.Writer flushes on every
	// event). Serve it verbatim so parallel writers see a consistent view.
	path := filepath.Join(s.cfg.RunsDir, "runs", id, "state.json")
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state not readable: "+err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.Copy(w, f)
}

// handleRunEvents implements the SSE stream. New subscribers get the
// entire event log to date (replay), then live events until RunFinished
// or the client disconnects.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.runs.get(id)
	if rec == nil {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported by this response writer")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	snap, live := rec.subscribe()
	writeSSE := func(e events.Event) bool {
		payload, err := json.Marshal(map[string]any{
			"type":  eventTypeName(e),
			"event": e,
		})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for _, e := range snap {
		if !writeSSE(e) {
			return
		}
	}
	// Heartbeat every 25s so long-lived proxies don't time the connection
	// out during quiet stretches.
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-live:
			if !ok {
				return
			}
			if !writeSSE(e) {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if rec := s.runs.get(id); rec == nil {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	base := filepath.Join(s.cfg.RunsDir, "runs", id, "artifacts")
	// Reuse the workspace path-traversal guard to keep this safe from a
	// crafted `name` (e.g. "../../etc/passwd").
	full, err := workspace.SafeJoin(base, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact name")
		return
	}
	f, err := os.Open(full)
	if err == nil {
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		http.ServeContent(w, r, filepath.Base(full), stat.ModTime(), f)
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Local miss → fall back to remote store if configured. The upload
	// action mirrors artifacts to it, so the file is likely there even
	// if local retention pruned this run's dir.
	if s.store != nil {
		key := id + "/" + name
		info, serr := s.store.Stat(r.Context(), key)
		if serr == nil {
			rc, _, gerr := s.store.Get(r.Context(), key)
			if gerr == nil {
				defer rc.Close()
				w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size))
				http.ServeContent(w, r, name, info.LastModified, readSeekerAdapter(rc, info.Size))
				return
			}
		}
		if errors.Is(serr, artifacts.ErrNotFound) {
			writeError(w, http.StatusNotFound, "artifact not found")
			return
		}
	}
	writeError(w, http.StatusNotFound, "artifact not found")
}

// readSeekerAdapter turns a plain ReadCloser into an io.ReadSeeker for
// http.ServeContent. For streams that don't support Seek we buffer to
// memory; artifacts are typically small enough for this to be fine.
func readSeekerAdapter(rc io.ReadCloser, _ int64) io.ReadSeeker {
	if rs, ok := rc.(io.ReadSeeker); ok {
		return rs
	}
	buf, _ := io.ReadAll(rc)
	return bytes.NewReader(buf)
}

func eventTypeName(e events.Event) string {
	name := fmt.Sprintf("%T", e)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}
