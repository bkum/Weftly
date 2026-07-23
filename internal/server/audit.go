package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry is one row in the append-only audit log. Every mutating
// endpoint (POST /runs, DELETE /runs/{id}, POST /schedules/{id}/trigger,
// POST /reload) emits one entry. Read-only endpoints are covered by
// the access log; auditing them would triple the log volume without
// adding useful investigative signal.
type AuditEntry struct {
	Time       time.Time `json:"time"`
	Principal  string    `json:"principal"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Workflow   string    `json:"workflow,omitempty"` // populated when known (POST /runs body, schedule ref)
	Schedule   string    `json:"schedule,omitempty"` // populated on trigger/schedule endpoints
	RunID      string    `json:"run_id,omitempty"`   // populated on POST /runs response, DELETE /runs/{id}
	Status     int       `json:"status"`
	RemoteAddr string    `json:"remote_addr,omitempty"`
}

// AuditLog is the per-server sink. Backed by a JSON-lines file (one
// entry per line) so operators can `jq` it and log-shipping agents can
// tail it without a schema translation layer. Writes are serialised on
// a single goroutine so concurrent requests don't interleave partial
// lines. In-memory tail is kept for GET /audit so admins can inspect
// recent activity without reading the file.
type AuditLog struct {
	path string
	log  *slog.Logger

	mu   sync.Mutex
	f    *os.File
	tail []AuditEntry
	cap  int // in-memory tail cap
}

// NewAuditLog opens (or creates) the audit-log file for append.
// A missing directory is created. Path "" disables auditing — every
// Record call becomes a no-op, and GET /audit returns an empty list.
func NewAuditLog(path string, log *slog.Logger) (*AuditLog, error) {
	if path == "" {
		return &AuditLog{log: log, cap: 200}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("audit: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open: %w", err)
	}
	al := &AuditLog{path: path, log: log, f: f, cap: 200}
	// Warm the in-memory tail by reading the last few lines from disk
	// so a server restart doesn't lose recent context for GET /audit.
	al.warmTail()
	return al, nil
}

// warmTail loads the last N entries from disk into the in-memory tail
// buffer. Best-effort — a truncated last line or missing file is fine.
func (a *AuditLog) warmTail() {
	if a.path == "" {
		return
	}
	f, err := os.Open(a.path)
	if err != nil {
		return
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	var buf []AuditEntry
	for scan.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(scan.Bytes(), &e); err == nil {
			buf = append(buf, e)
			if len(buf) > a.cap {
				buf = buf[len(buf)-a.cap:]
			}
		}
	}
	a.tail = buf
}

// Record appends an entry. Errors go to the server logger but do not
// fail the request — an audit-log outage should never take the API
// down.
func (a *AuditLog) Record(e AuditEntry) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tail = append(a.tail, e)
	if len(a.tail) > a.cap {
		a.tail = a.tail[len(a.tail)-a.cap:]
	}
	if a.f == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		a.log.Error("audit: marshal", "err", err)
		return
	}
	b = append(b, '\n')
	if _, err := a.f.Write(b); err != nil {
		a.log.Error("audit: write", "err", err)
	}
}

// Tail returns a copy of the in-memory recent entries, oldest first.
// Callers can slice/reverse as they like.
func (a *AuditLog) Tail() []AuditEntry {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEntry, len(a.tail))
	copy(out, a.tail)
	return out
}

// Close flushes the underlying file. Safe to call on a nil AuditLog
// or one that was constructed with an empty path.
func (a *AuditLog) Close() error {
	if a == nil || a.f == nil {
		return nil
	}
	return a.f.Close()
}

// auditMiddleware wraps mutating handlers so every response emits one
// audit entry. Read-only endpoints (GET) skip the middleware — the
// access log already records them.
func (s *Server) auditMiddleware(h http.HandlerFunc, extract func(*http.Request) (workflow, schedule string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			h(w, r)
			return
		}
		sw := &statusRecorder{ResponseWriter: w, status: 200}
		wf, sc := "", ""
		if extract != nil {
			wf, sc = extract(r)
		}
		h(sw, r)
		s.audit.Record(AuditEntry{
			Time:       time.Now().UTC(),
			Principal:  PrincipalFromContext(r.Context()).Name,
			Method:     r.Method,
			Path:       r.URL.Path,
			Workflow:   wf,
			Schedule:   sc,
			RunID:      r.PathValue("id"),
			Status:     sw.status,
			RemoteAddr: r.RemoteAddr,
		})
	}
}

// statusRecorder is a tiny ResponseWriter wrapper that remembers the
// status code so the audit middleware can record it. If a handler
// never calls WriteHeader, the default 200 kicks in.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets net/http's Hijacker/Flusher chain work through the wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Ensure the io.Writer contract stays clean (SSE writes bypass WriteHeader).
var _ io.Writer = (*statusRecorder)(nil)

// extractCreateRunAudit peeks the JSON body for the workflow name so
// the audit entry records what was launched. Body is read fully and
// re-attached so the actual handler still sees it. On a malformed body
// we return "" and let the handler produce the real error.
func extractCreateRunAudit(r *http.Request) (workflow, schedule string) {
	if r.Body == nil {
		return "", ""
	}
	buf, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return "", ""
	}
	// Restore the body for the downstream handler.
	r.Body = io.NopCloser(bytes.NewReader(buf))
	var v struct {
		Workflow string `json:"workflow"`
	}
	_ = json.Unmarshal(buf, &v)
	return v.Workflow, ""
}

// extractTriggerAudit surfaces the schedule id from the URL path so
// operators can grep "who ran schedule X".
func extractTriggerAudit(r *http.Request) (workflow, schedule string) {
	return "", r.PathValue("id")
}
