package server

import (
	"net/http"
)

// visibleSchedule reports whether the principal may see / trigger the
// given schedule — same rule as workflows (the schedule dispatches a
// workflow, so its access is bound by the workflow's ACL).
func (s *Server) visibleSchedule(id string, p Principal) (bool, bool) {
	if s.sched == nil {
		return false, false
	}
	st, ok := s.sched.Get(id)
	if !ok {
		return false, false
	}
	return true, p.CanRunWorkflow(st.Workflow)
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		writeJSON(w, http.StatusOK, map[string]any{"schedules": []any{}})
		return
	}
	p := PrincipalFromContext(r.Context())
	all := s.sched.States()
	// Hide schedules whose workflow the caller can't run — same policy
	// as /workflows so a dev token can't discover ops-only jobs.
	visible := all[:0]
	for _, st := range all {
		if p.CanRunWorkflow(st.Workflow) {
			visible = append(visible, st)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": visible})
}

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.sched == nil {
		writeError(w, http.StatusNotFound, "scheduler is not configured")
		return
	}
	exists, allowed := s.visibleSchedule(id, PrincipalFromContext(r.Context()))
	if !exists {
		writeError(w, http.StatusNotFound, "unknown schedule")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "schedule not accessible to this principal")
		return
	}
	st, _ := s.sched.Get(id)
	writeJSON(w, http.StatusOK, st)
}

// handleTriggerSchedule fires a schedule immediately without waiting
// for its cron. Same authz as running the underlying workflow — a
// caller that can't POST /runs for wf X mustn't be able to launch it
// via a schedule that happens to dispatch X.
func (s *Server) handleTriggerSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.sched == nil {
		writeError(w, http.StatusNotFound, "scheduler is not configured")
		return
	}
	exists, allowed := s.visibleSchedule(id, PrincipalFromContext(r.Context()))
	if !exists {
		writeError(w, http.StatusNotFound, "unknown schedule")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "schedule not accessible to this principal")
		return
	}
	// Use the request context so the caller can abort a slow trigger;
	// the run itself is dispatched under context.Background() by
	// runManager.start (detached from the HTTP request lifetime).
	runID, err := s.sched.TriggerNow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}
