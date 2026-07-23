package server

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed ui/*
var uiFS embed.FS

// uiHandler serves the SPA shell (index.html + app.js + styles.css) from
// the embedded filesystem. Nothing here is authenticated — the SPA loads
// unauthenticated and then makes API calls with a bearer token the user
// supplies on first 401.
func uiHandler() http.Handler {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "ui bundle missing: "+err.Error(), http.StatusInternalServerError)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// SPA-style fallback: unknown non-file paths return index so
		// client-side hash routing can pick them up. A dotted basename
		// implies a real asset request.
		if !strings.Contains(path.Base(p), ".") {
			p = "index.html"
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType(p))
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, path.Base(p), time.Time{}, bytes.NewReader(data))
	})
}

func contentType(p string) string {
	switch path.Ext(p) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	}
	return "application/octet-stream"
}
