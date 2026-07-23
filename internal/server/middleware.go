package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Authenticator decides whether a request is allowed. Returning "" from
// Principal grants anonymous access (used when no token is configured);
// returning a non-empty principal identifies the caller for logging.
type Authenticator interface {
	Principal(r *http.Request) (principal string, ok bool)
}

// BearerToken accepts requests carrying `Authorization: Bearer <token>`
// where <token> matches the configured value exactly (constant-time
// compare). An empty configured token disables enforcement — every
// request passes with principal "anon".
type bearerToken string

func BearerToken(token string) Authenticator { return bearerToken(token) }

func (b bearerToken) Principal(r *http.Request) (string, bool) {
	if b == "" {
		return "anon", true
	}
	got := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	} else if q := r.URL.Query().Get("token"); q != "" {
		// EventSource in the browser cannot set custom headers, so the SPA
		// falls back to ?token=... on SSE URLs. Anything else should keep
		// using the header for cleanliness — but we accept both uniformly
		// so the auth surface is one code path.
		got = q
	}
	if got == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(b)) != 1 {
		return "", false
	}
	return "bearer", true
}

// withAuth wraps h with bearer-token enforcement. Paths in `exempt` bypass
// the check (used for /healthz so external probes work).
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
		principal, ok := a.Principal(r)
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
			"principal", principalFromContext(r.Context()),
		)
	})
}

// sanitizeForLog scrubs bytes that could forge log-entry boundaries when
// the value is subsequently written to a text-format log. slog's text
// handler already quotes control characters, but CodeQL flags the raw
// pattern regardless — and being explicit here makes the intent obvious
// to a human reader too.
func sanitizeForLog(s string) string {
	const maxLen = 512
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			b = append(b, '?')
			continue
		}
		b = append(b, c)
	}
	return string(b)
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

func withPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, principalKey, principal)
}

func principalFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(principalKey).(string); ok {
		return v
	}
	return ""
}
