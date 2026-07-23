// Package scheduler drives workflow dispatch on a cron schedule.
//
// Each entry in schedules.yaml pins a workflow (must exist in the
// server's catalogue), a cron expression, and an optional inputs map.
// A single goroutine ticks every 30 seconds and, for each schedule
// whose next-fire time has passed, calls into the server's run
// manager exactly the same way POST /runs does. Skipped fires
// (server was down at the tick, or the previous run is still
// executing and the entry is single-instance) are surfaced via the
// per-schedule LastResult, not swallowed.
//
// We keep this in a separate package so the server binary can still
// build even if the scheduler is disabled (empty schedules.yaml → no
// goroutine), and so tests can drive it directly.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bkum/weftly/internal/cron"
	"gopkg.in/yaml.v3"
)

// Entry is one row from schedules.yaml as the operator wrote it.
type Entry struct {
	ID       string         `yaml:"id" json:"id"`
	Workflow string         `yaml:"workflow" json:"workflow"`
	Cron     string         `yaml:"cron" json:"cron"`
	TZ       string         `yaml:"tz,omitempty" json:"tz,omitempty"`
	Inputs   map[string]any `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	// Disabled skips the entry entirely; useful for on-call rotations
	// or one-off maintenance windows without deleting the row.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// File is the on-disk shape of schedules.yaml.
type File struct {
	Schedules []Entry `yaml:"schedules"`
}

// LoadFile parses schedules.yaml. A missing file returns (nil, nil) —
// scheduling is opt-in.
func LoadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("schedules: %w", err)
	}
	return &f, nil
}

// Runner is the callback the scheduler invokes to dispatch a workflow.
// Returning an error surfaces on the entry's LastResult; the scheduler
// treats it as informational and keeps ticking.
//
// The server wires this to runManager.start; tests can pass a fake to
// observe firings without spinning up the engine.
type Runner func(ctx context.Context, workflow string, inputs map[string]any) (runID string, err error)

// LastResult captures the outcome of the most recent fire — enough for
// the operator to spot a wedged schedule from GET /schedules.
type LastResult struct {
	At    time.Time `json:"at"`
	RunID string    `json:"run_id,omitempty"`
	Error string    `json:"error,omitempty"`
}

// State is what /schedules returns per entry — the operator-facing
// mirror of an Entry plus its computed next-fire time and last result.
type State struct {
	Entry
	NextFire   time.Time   `json:"next_fire,omitempty"`
	LastResult *LastResult `json:"last_result,omitempty"`
	// ParseError, if non-empty, means the cron expression didn't parse
	// so this entry never fires — surfaced instead of silently dropped.
	ParseError string `json:"parse_error,omitempty"`
}

// Scheduler owns the set of parsed schedules and the ticker goroutine.
type Scheduler struct {
	log    *slog.Logger
	runner Runner

	mu      sync.RWMutex
	entries []*compiled
}

type compiled struct {
	Entry
	schedule *cron.Schedule
	loc      *time.Location
	next     time.Time
	last     *LastResult
	parseErr string
}

// New builds a Scheduler. Call SetSchedules to populate it (or
// LoadFile → SetSchedules).
func New(log *slog.Logger, runner Runner) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{log: log, runner: runner}
}

// SetSchedules atomically replaces the entry set. Duplicate IDs are an
// error; a bad cron on one entry doesn't reject the others (its
// ParseError is surfaced instead).
func (s *Scheduler) SetSchedules(entries []Entry, now time.Time) error {
	seen := map[string]bool{}
	out := make([]*compiled, 0, len(entries))
	for _, e := range entries {
		e.ID = strings.TrimSpace(e.ID)
		if e.ID == "" {
			return fmt.Errorf("schedule: id is required")
		}
		if seen[e.ID] {
			return fmt.Errorf("schedule %q: duplicate id", e.ID)
		}
		seen[e.ID] = true
		if strings.TrimSpace(e.Workflow) == "" {
			return fmt.Errorf("schedule %q: workflow is required", e.ID)
		}
		c := &compiled{Entry: e, loc: time.UTC}
		if e.TZ != "" {
			loc, err := time.LoadLocation(e.TZ)
			if err != nil {
				return fmt.Errorf("schedule %q: tz %q: %w", e.ID, e.TZ, err)
			}
			c.loc = loc
		}
		sch, err := cron.Parse(e.Cron)
		if err != nil {
			c.parseErr = err.Error()
		} else {
			c.schedule = sch
			c.next = sch.Next(now.In(c.loc))
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	s.mu.Lock()
	s.entries = out
	s.mu.Unlock()
	return nil
}

// States returns a snapshot suitable for GET /schedules. Safe to marshal.
func (s *Scheduler) States() []State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]State, 0, len(s.entries))
	for _, c := range s.entries {
		st := State{Entry: c.Entry, ParseError: c.parseErr}
		if c.schedule != nil {
			st.NextFire = c.next
		}
		if c.last != nil {
			cp := *c.last
			st.LastResult = &cp
		}
		out = append(out, st)
	}
	return out
}

// Get looks up a single entry.
func (s *Scheduler) Get(id string) (State, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.entries {
		if c.ID == id {
			st := State{Entry: c.Entry, ParseError: c.parseErr}
			if c.schedule != nil {
				st.NextFire = c.next
			}
			if c.last != nil {
				cp := *c.last
				st.LastResult = &cp
			}
			return st, true
		}
	}
	return State{}, false
}

// TriggerNow dispatches the given schedule immediately, bypassing its
// cron. It does NOT reset next-fire — a manual trigger is bookkeeping
// for the operator, not a substitute for the schedule.
func (s *Scheduler) TriggerNow(ctx context.Context, id string) (string, error) {
	s.mu.RLock()
	var target *compiled
	for _, c := range s.entries {
		if c.ID == id {
			target = c
			break
		}
	}
	s.mu.RUnlock()
	if target == nil {
		return "", errors.New("schedule not found")
	}
	if target.Disabled {
		return "", errors.New("schedule is disabled")
	}
	return s.dispatch(ctx, target, time.Now().UTC())
}

// Run blocks until ctx is done, checking every 30s for schedules whose
// next-fire time has elapsed. 30s is short enough that a per-minute
// cron never drifts by more than a fraction of a minute, and long
// enough that idle CPU cost is negligible.
func (s *Scheduler) Run(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	// Fire an immediate pass so a schedule with next_fire already in
	// the past (server was down) catches up on startup.
	s.pass(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			s.pass(ctx, now)
		}
	}
}

// pass walks the entry list and dispatches every entry whose next-fire
// time is at or before now. Firing advances the entry's next-fire to
// the following occurrence; a run that fails to start still advances
// the schedule (otherwise a persistent error would stall it forever
// and hide subsequent occurrences).
func (s *Scheduler) pass(ctx context.Context, now time.Time) {
	s.mu.RLock()
	due := make([]*compiled, 0)
	for _, c := range s.entries {
		if c.Disabled || c.schedule == nil {
			continue
		}
		if !c.next.IsZero() && !now.Before(c.next) {
			due = append(due, c)
		}
	}
	s.mu.RUnlock()
	for _, c := range due {
		if _, err := s.dispatch(ctx, c, now); err != nil {
			s.log.Warn("schedule dispatch failed", "id", c.ID, "err", err)
		}
	}
}

// dispatch invokes the Runner, records the outcome, and (if we came
// through pass()) advances the next-fire cursor. Manual triggers do
// not advance next-fire — we detect that by checking whether the
// current cursor is already in the future.
func (s *Scheduler) dispatch(ctx context.Context, c *compiled, now time.Time) (string, error) {
	runID, err := s.runner(ctx, c.Workflow, c.Inputs)
	res := &LastResult{At: now.UTC(), RunID: runID}
	if err != nil {
		res.Error = err.Error()
	}
	s.mu.Lock()
	c.last = res
	// Only advance the cursor if the current cursor is in the past —
	// TriggerNow calls this with now >= c.next possible or not, but
	// its intent is "extra fire", so guard.
	if c.schedule != nil && !c.next.IsZero() && !now.Before(c.next) {
		c.next = c.schedule.Next(now.In(c.loc))
	}
	s.mu.Unlock()
	return runID, err
}
