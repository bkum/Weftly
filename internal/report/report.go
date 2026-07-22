// Package report collects SummaryEmitted markdown and ArtifactUploaded
// entries from the event bus and writes a self-contained HTML report at
// end of run.
package report

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/secrets"
)

// Artifact is a copy of ArtifactUploaded suitable for embedding in the
// rendered report.
type Artifact struct {
	Name string
	Path string
	Size int64
}

// Report accumulates report material for a single run.
type Report struct {
	Secrets *secrets.Registry

	mu         sync.Mutex
	summaries  []string
	artifacts  []Artifact
	workflow   string
	runID      string
	workspace  string
	startedAt  time.Time
	finishedAt time.Time
	status     events.Status
}

func New(sec *secrets.Registry) *Report { return &Report{Secrets: sec} }

// Handle is the events.Bus subscriber.
func (r *Report) Handle(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch ev := e.(type) {
	case events.RunStarted:
		r.workflow = ev.Workflow
		r.runID = ev.RunID
		r.workspace = ev.Workspace
		r.startedAt = time.Now()
	case events.SummaryEmitted:
		md := ev.Markdown
		if r.Secrets != nil {
			md = r.Secrets.Mask(md)
		}
		r.summaries = append(r.summaries, md)
	case events.ArtifactUploaded:
		r.artifacts = append(r.artifacts, Artifact{Name: ev.Name, Path: ev.Path, Size: ev.Size})
	case events.RunFinished:
		r.finishedAt = time.Now()
		r.status = ev.Status
	}
}

// Write renders the accumulated material to path as a self-contained HTML
// document. Safe to call at end of run.
func (r *Report) Write(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString(htmlPrefix)
	fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(r.workflow))
	fmt.Fprintf(&b, "<p class=meta>run <code>%s</code> · status <strong class=%s>%s</strong> · %s</p>\n",
		html.EscapeString(r.runID),
		statusClass(r.status),
		html.EscapeString(string(r.status)),
		html.EscapeString(r.finishedAt.Sub(r.startedAt).Round(time.Millisecond).String()),
	)
	if len(r.summaries) > 0 {
		b.WriteString("<h2>Summary</h2>\n")
		for _, s := range r.summaries {
			b.WriteString(renderMarkdown(s))
		}
	}
	if len(r.artifacts) > 0 {
		b.WriteString("<h2>Artifacts</h2>\n<ul>\n")
		for _, a := range r.artifacts {
			fmt.Fprintf(&b, "  <li><code>%s</code> (%d bytes) — <code>%s</code></li>\n",
				html.EscapeString(a.Name), a.Size, html.EscapeString(a.Path))
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString(htmlSuffix)
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func statusClass(s events.Status) string {
	switch s {
	case events.Success:
		return "ok"
	case events.Failed, events.TimedOut:
		return "err"
	case events.FailedContinued:
		return "warn"
	}
	return "info"
}

// renderMarkdown is a tiny subset renderer: line-based, honoring `#`
// headings, `- ` bullets, fenced code blocks, `**bold**`, and inline
// `code`. This is deliberately not a general-purpose markdown engine —
// summaries are short and structured.
func renderMarkdown(md string) string {
	var b strings.Builder
	inCode := false
	inList := false
	closeList := func() {
		if inList {
			b.WriteString("</ul>\n")
			inList = false
		}
	}
	for _, raw := range strings.Split(md, "\n") {
		line := raw
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			closeList()
			if !inCode {
				b.WriteString("<pre><code>")
				inCode = true
			} else {
				b.WriteString("</code></pre>\n")
				inCode = false
			}
			continue
		}
		if inCode {
			b.WriteString(html.EscapeString(line))
			b.WriteString("\n")
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "### "):
			closeList()
			fmt.Fprintf(&b, "<h3>%s</h3>\n", inline(trimmed[4:]))
		case strings.HasPrefix(trimmed, "## "):
			closeList()
			fmt.Fprintf(&b, "<h2>%s</h2>\n", inline(trimmed[3:]))
		case strings.HasPrefix(trimmed, "# "):
			closeList()
			fmt.Fprintf(&b, "<h1>%s</h1>\n", inline(trimmed[2:]))
		case strings.HasPrefix(trimmed, "- "):
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			fmt.Fprintf(&b, "  <li>%s</li>\n", inline(trimmed[2:]))
		case trimmed == "":
			closeList()
			b.WriteString("\n")
		default:
			closeList()
			fmt.Fprintf(&b, "<p>%s</p>\n", inline(trimmed))
		}
	}
	closeList()
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	return b.String()
}

// inline handles bold and inline code within a snippet of markdown.
func inline(s string) string {
	// escape first, then substitute the escaped markers back.
	out := html.EscapeString(s)
	// inline code: `x`
	out = replacePairs(out, "`", "<code>", "</code>")
	// bold: **x**
	out = replacePairs(out, "**", "<strong>", "</strong>")
	return out
}

// replacePairs alternates between open and close around every pair of
// occurrences of marker.
func replacePairs(s, marker, open, close string) string {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+len(marker):]
		j := strings.Index(rest, marker)
		if j < 0 {
			// unbalanced; write literal marker
			b.WriteString(marker)
			b.WriteString(rest)
			break
		}
		b.WriteString(open)
		b.WriteString(rest[:j])
		b.WriteString(close)
		rest = rest[j+len(marker):]
	}
	return b.String()
}

const htmlPrefix = `<!doctype html>
<html><head><meta charset="utf-8">
<title>Weftly run report</title>
<style>
  body { font: 14px/1.5 -apple-system,Segoe UI,Roboto,sans-serif; max-width: 780px; margin: 2rem auto; padding: 0 1rem; color: #24292f; }
  h1 { margin-bottom: .25rem; }
  .meta { color: #57606a; margin-top: 0; }
  code { background: #f6f8fa; padding: 1px 4px; border-radius: 4px; }
  pre { background: #f6f8fa; padding: .75rem; border-radius: 6px; overflow-x: auto; }
  pre code { background: transparent; padding: 0; }
  ul { padding-left: 1.25rem; }
  strong.ok  { color: #1a7f37; }
  strong.err { color: #cf222e; }
  strong.warn { color: #9a6700; }
  strong.info { color: #57606a; }
</style>
</head><body>
`

const htmlSuffix = `</body></html>
`
