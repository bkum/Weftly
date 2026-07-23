package cron

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"",
		"* * * *",         // 4 fields
		"* * * * * *",     // 6 fields
		"@bogus",          // bad descriptor
		"60 * * * *",      // minute out of range
		"* 24 * * *",      // hour out of range
		"* * 0 * *",       // dom < 1
		"* * * 13 *",      // month > 12
		"* * * * 8",       // dow > 7
		"*/0 * * * *",     // zero step
		"5-3 * * * *",     // reversed range
		"* * * jam *",     // bad alias
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("Parse(%q): want error, got nil", c)
		}
	}
}

func TestDescriptors(t *testing.T) {
	// @daily fires at 00:00 next day.
	s := mustParse(t, "@daily")
	base := time.Date(2026, 5, 10, 15, 42, 0, 0, time.UTC)
	next := s.Next(base)
	want := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("@daily Next: got %v, want %v", next, want)
	}
	// @weekly = Sundays at midnight.
	next = mustParse(t, "@weekly").Next(base) // 2026-05-10 is a Sunday
	want = time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("@weekly Next: got %v, want %v", next, want)
	}
}

func TestNextStepsAndRanges(t *testing.T) {
	base := time.Date(2026, 7, 1, 10, 7, 30, 0, time.UTC)
	if got := mustParse(t, "*/15 * * * *").Next(base); got.Minute() != 15 {
		t.Errorf("*/15 next minute: got %d, want 15", got.Minute())
	}
	if got := mustParse(t, "0 9-17 * * *").Next(base); got.Hour() != 11 {
		t.Errorf("0 9-17 next hour: got %d, want 11", got.Hour())
	}
	// Monday–Friday: 2026-07-01 is a Wed → next Mon-Fri at 09:00 is
	// 2026-07-02 09:00.
	got := mustParse(t, "0 9 * * mon-fri").Next(base)
	want := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("mon-fri: got %v, want %v", got, want)
	}
}

func TestDomDowOR(t *testing.T) {
	// "1st of the month OR any Monday at 12:00" — the OR is the Vixie
	// semantic. On Wed 2026-07-01 the very next fire should be 12:00
	// today (it's the 1st).
	s := mustParse(t, "0 12 1 * mon")
	base := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	got := s.Next(base)
	want := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("dom OR dow: got %v, want %v", got, want)
	}
	// After 12:00 the next fire is Monday 2026-07-06 at 12:00.
	got = s.Next(time.Date(2026, 7, 1, 12, 30, 0, 0, time.UTC))
	want = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("dom OR dow later: got %v, want %v", got, want)
	}
}

func TestSundayAlias7(t *testing.T) {
	s := mustParse(t, "0 0 * * 7")
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) // Wed
	got := s.Next(base)
	want := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC) // Sun
	if !got.Equal(want) {
		t.Errorf("dow=7 (sun): got %v, want %v", got, want)
	}
}
