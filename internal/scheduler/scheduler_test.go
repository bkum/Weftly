package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu    sync.Mutex
	calls []struct {
		wf     string
		inputs map[string]any
	}
	runID string
	err   error
}

func (r *recorder) run(_ context.Context, wf string, inputs map[string]any) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		wf     string
		inputs map[string]any
	}{wf, inputs})
	return r.runID, r.err
}

func newTestScheduler(t *testing.T, r *recorder) *Scheduler {
	t.Helper()
	return New(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), r.run)
}

func TestSetSchedulesRejectsDuplicateIDs(t *testing.T) {
	s := newTestScheduler(t, &recorder{})
	err := s.SetSchedules([]Entry{
		{ID: "a", Workflow: "wf", Cron: "@daily"},
		{ID: "a", Workflow: "wf", Cron: "@hourly"},
	}, time.Now())
	if err == nil {
		t.Fatal("want duplicate-id error, got nil")
	}
}

func TestSetSchedulesCapturesParseErrorPerEntry(t *testing.T) {
	s := newTestScheduler(t, &recorder{})
	// Bad cron on entry `b` should NOT block entry `a` from loading.
	if err := s.SetSchedules([]Entry{
		{ID: "a", Workflow: "wf-a", Cron: "@daily"},
		{ID: "b", Workflow: "wf-b", Cron: "bogus"},
	}, time.Now()); err != nil {
		t.Fatalf("SetSchedules: %v", err)
	}
	states := s.States()
	if len(states) != 2 {
		t.Fatalf("want 2 states, got %d", len(states))
	}
	var stA, stB State
	for _, st := range states {
		if st.ID == "a" {
			stA = st
		} else {
			stB = st
		}
	}
	if stA.ParseError != "" {
		t.Errorf("A should parse cleanly, got %q", stA.ParseError)
	}
	if stB.ParseError == "" {
		t.Errorf("B should surface parse error")
	}
	if !stB.NextFire.IsZero() {
		t.Errorf("B should have no next-fire, got %v", stB.NextFire)
	}
}

func TestPassFiresDueEntriesAndAdvancesCursor(t *testing.T) {
	rec := &recorder{runID: "run-xyz"}
	s := newTestScheduler(t, rec)
	// Schedule to fire every minute; start cursor 5 min in the past
	// so it's definitely due.
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := s.SetSchedules([]Entry{{ID: "tick", Workflow: "wf", Cron: "* * * * *"}}, now.Add(-6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.pass(context.Background(), now)
	if len(rec.calls) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(rec.calls))
	}
	st, ok := s.Get("tick")
	if !ok {
		t.Fatal("Get(tick) missing")
	}
	if st.LastResult == nil || st.LastResult.RunID != "run-xyz" {
		t.Errorf("LastResult: got %+v", st.LastResult)
	}
	if !st.NextFire.After(now) {
		t.Errorf("cursor didn't advance past now: %v", st.NextFire)
	}
}

func TestPassSkipsDisabled(t *testing.T) {
	rec := &recorder{}
	s := newTestScheduler(t, rec)
	if err := s.SetSchedules([]Entry{{ID: "z", Workflow: "wf", Cron: "* * * * *", Disabled: true}}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.pass(context.Background(), time.Now())
	if len(rec.calls) != 0 {
		t.Errorf("disabled entry fired: %+v", rec.calls)
	}
}

func TestTriggerNowIgnoresCron(t *testing.T) {
	rec := &recorder{runID: "manual-1"}
	s := newTestScheduler(t, rec)
	if err := s.SetSchedules([]Entry{{ID: "on-demand", Workflow: "wf", Cron: "@yearly"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	id, err := s.TriggerNow(context.Background(), "on-demand")
	if err != nil {
		t.Fatal(err)
	}
	if id != "manual-1" {
		t.Errorf("run id: got %q", id)
	}
	if _, err := s.TriggerNow(context.Background(), "nope"); err == nil {
		t.Error("want not-found for unknown id")
	}
}

func TestTriggerNowRejectsDisabled(t *testing.T) {
	s := newTestScheduler(t, &recorder{})
	if err := s.SetSchedules([]Entry{{ID: "off", Workflow: "wf", Cron: "@daily", Disabled: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TriggerNow(context.Background(), "off"); err == nil {
		t.Error("want error for disabled schedule")
	}
}

func TestDispatchErrorRecorded(t *testing.T) {
	rec := &recorder{err: errors.New("engine unhappy")}
	s := newTestScheduler(t, rec)
	if err := s.SetSchedules([]Entry{{ID: "x", Workflow: "wf", Cron: "* * * * *"}}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.pass(context.Background(), time.Now())
	st, _ := s.Get("x")
	if st.LastResult == nil || st.LastResult.Error == "" {
		t.Fatalf("expected error on LastResult, got %+v", st.LastResult)
	}
}

func TestLoadFileMissingReturnsNil(t *testing.T) {
	f, err := LoadFile("/nonexistent/schedules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Errorf("want nil for missing file, got %+v", f)
	}
}

func TestLoadFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/schedules.yaml"
	body := `schedules:
  - id: nightly
    workflow: petclinic-onboarding
    cron: "0 2 * * *"
    tz: America/Los_Angeles
    inputs:
      env: prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Schedules) != 1 || f.Schedules[0].ID != "nightly" {
		t.Fatalf("unexpected parse: %+v", f)
	}
	if f.Schedules[0].TZ != "America/Los_Angeles" {
		t.Errorf("tz not parsed: %q", f.Schedules[0].TZ)
	}
	if f.Schedules[0].Inputs["env"] != "prod" {
		t.Errorf("inputs not parsed: %+v", f.Schedules[0].Inputs)
	}
}
