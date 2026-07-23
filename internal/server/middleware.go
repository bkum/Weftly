package server

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// withAuth wraps h with bearer-token enforcement. Paths in `exempt` bypass
// the check (used for /healthz and the SPA shell so external probes and
// initial page loads work).
func withAuth(h http.Handler, a Authenticator, log *slog.Logger, exempt ...string) http.Handler {
	exemptSet := map[string]bool{}
	for _, p := range exempt {
		exemptSet[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exemptSet[r.URL.Path] {
			h.ServeHTTP(w, r)
			return
		}
		principal, ok := a.Authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="weftly"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			log.Info("unauthorized", "path", sanitizeForLog(r.URL.Path), "remote", sanitizeForLog(r.RemoteAddr))
			return
		}
		r = r.WithContext(withPrincipal(r.Context(), principal))
		h.ServeHTTP(w, r)
	})
}

// withMaxBody caps r.Body to n bytes so a malformed or hostile client
// can't force the server to buffer arbitrary payloads.
func withMaxBody(h http.Handler, n int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		h.ServeHTTP(w, r)
	})
}

// withAccessLog emits one structured log line per request. Duration and
// status are captured via a lightweight ResponseWriter wrapper.
func withAccessLog(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(lw, r)
		log.Info("http",
			"method", sanitizeForLog(r.Method),
			"path", sanitizeForLog(r.URL.Path),
			"status", lw.status,
			"bytes", lw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"principal", principalNameFromContext(r.Context()),
		)
	})
}

// sanitizeForLog neutralises log-forging bytes (CR/LF and other control
// chars) in user-controlled strings before they hit the logger. It wraps
// strconv.Quote — which CodeQL's taint model recognises as a log-injection
// sanitiser — so the analyser sees a clean flow. The extra length cap
// keeps a hostile-long path from bloating log lines.
func sanitizeForLog(s string) string {
	const maxLen = 512
	if len(s) > maxLen {
		s = s[:maxLen] + "…(truncated)"
	}
	return strconv.Quote(s)
}

type loggingWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *loggingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *loggingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}
func (w *loggingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// principal context helpers ---------------------------------------------

type ctxKey int

const principalKey ctxKey = 1

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFromContext returns the resolved caller identity, or a zero
// Principal if no authentication ran (in-process test harness, for
// example). Handlers should treat the zero value as anonymous.
func PrincipalFromContext(ctx context.Context) Principal {
	if v, ok := ctx.Value(principalKey).(Principal); ok {
		return v
	}
	return Principal{}
}

func principalNameFromContext(ctx context.Context) string {
	return PrincipalFromContext(ctx).Name
}
