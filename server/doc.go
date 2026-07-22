// Package server is the Phase 2 target: a REST + SSE + embedded-SPA
// front-end over the same engine core used by the CLI. See spec.md §15.
//
// Intentionally empty in Phase 1. The event bus in internal/events and the
// action.Emit seam are the plumbing this package will subscribe to; no
// engine changes should be required to add server mode.
package server
