// Package cron parses a 5-field cron expression (minute, hour,
// day-of-month, month, day-of-week) plus the common @-descriptors and
// answers "when is the next fire time after t?". Fields support
// numbers, ranges (1-5), lists (1,3,5), the wildcard "*" and step
// syntax (*/15, 5-30/5). Day-of-month and day-of-week combine with OR
// when both are restricted (Vixie/Debian cron behaviour), which matches
// what most operators expect from "Sundays at 3am" or "the 1st and every
// Monday".
//
// We deliberately keep this in-tree instead of pulling in robfig/cron:
// scheduled runs are a first-class feature but the parser is small
// enough that adding a dependency isn't worth it.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression that can be evaluated repeatedly.
type Schedule struct {
	expr   string
	minute uint64 // bit i (0..59) set → allowed
	hour   uint64 // 0..23
	dom    uint64 // 1..31
	month  uint64 // 1..12
	dow    uint64 // 0..6, Sunday=0
	// domRestricted / dowRestricted mirror whether the user pinned the
	// respective field. When BOTH are pinned the two are OR-ed
	// (Vixie/Debian semantics); when only one is pinned it filters on
	// its own.
	domRestricted bool
	dowRestricted bool
	// step / range bounds cached for quick "does this hour match?"
	// checks — the bitmasks already answer that, so we don't need them.
}

// Parse compiles a cron expression. It accepts either a 5-field spec
// ("30 2 * * *") or one of these descriptors:
//   - @hourly    → 0 * * * *
//   - @daily     → 0 0 * * *
//   - @midnight  → 0 0 * * *
//   - @weekly    → 0 0 * * 0
//   - @monthly   → 0 0 1 * *
//   - @yearly    → 0 0 1 1 *
//   - @annually  → 0 0 1 1 *
//
// We do NOT accept @reboot (semantics don't fit a long-running server
// that reloads schedules) or @every (robfig-only extension; use the
// 5-field form instead).
func Parse(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("cron: empty expression")
	}
	if strings.HasPrefix(expr, "@") {
		if replacement, ok := descriptors[expr]; ok {
			expr = replacement
		} else {
			return nil, fmt.Errorf("cron: unknown descriptor %q (allowed: @hourly @daily @midnight @weekly @monthly @yearly @annually)", expr)
		}
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: want 5 fields, got %d in %q", len(fields), expr)
	}
	s := &Schedule{expr: expr}
	var err error
	if s.minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("cron: minute: %w", err)
	}
	if s.hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("cron: hour: %w", err)
	}
	if s.dom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("cron: day-of-month: %w", err)
	}
	if s.month, err = parseField(fields[3], 1, 12, monthAliases); err != nil {
		return nil, fmt.Errorf("cron: month: %w", err)
	}
	if s.dow, err = parseField(fields[4], 0, 6, dowAliases); err != nil {
		return nil, fmt.Errorf("cron: day-of-week: %w", err)
	}
	// 7 is a common alias for Sunday in cron folklore; normalise.
	if s.dow&(1<<7) != 0 {
		s.dow |= 1 << 0
		s.dow &^= 1 << 7
	}
	s.domRestricted = fields[2] != "*"
	s.dowRestricted = fields[4] != "*"
	return s, nil
}

// String returns the canonical 5-field form.
func (s *Schedule) String() string { return s.expr }

// Next returns the next fire time strictly after t, in t's location.
// Runs a bounded search — cron entries can be sparse (Feb 29th), so we
// cap at 4 years and return the zero Time if nothing matches, which the
// caller should treat as "never fires" (a dead schedule).
func (s *Schedule) Next(t time.Time) time.Time {
	// Round up to the next full minute; cron ignores seconds.
	t = t.Add(time.Minute).Truncate(time.Minute)
	stop := t.AddDate(4, 0, 0)
	for t.Before(stop) {
		if s.month&(1<<uint(t.Month())) == 0 {
			// Jump to the 1st of the next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// dayMatches implements the DOM/DOW OR rule: if both are restricted,
// EITHER matching field lets the day through; if only one is
// restricted, only that one filters.
func (s *Schedule) dayMatches(t time.Time) bool {
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domRestricted && s.dowRestricted:
		return domOK || dowOK
	case s.domRestricted:
		return domOK
	case s.dowRestricted:
		return dowOK
	default:
		return true
	}
}

// -----------------------------------------------------------------------
// Field parser
// -----------------------------------------------------------------------

var descriptors = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var monthAliases = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowAliases = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// parseField compiles one field into a bitmask over [lo, hi]. Aliases
// (nil for numeric-only fields) provide case-insensitive month/day names.
func parseField(field string, lo, hi int, aliases map[string]int) (uint64, error) {
	var mask uint64
	for _, term := range strings.Split(field, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			return 0, fmt.Errorf("empty term in %q", field)
		}
		step := 1
		if i := strings.Index(term, "/"); i >= 0 {
			stepStr := term[i+1:]
			term = term[:i]
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}
		var rangeLo, rangeHi int
		switch {
		case term == "*":
			rangeLo, rangeHi = lo, hi
		case strings.Contains(term, "-"):
			parts := strings.SplitN(term, "-", 2)
			a, err := parseValue(parts[0], aliases)
			if err != nil {
				return 0, err
			}
			b, err := parseValue(parts[1], aliases)
			if err != nil {
				return 0, err
			}
			rangeLo, rangeHi = a, b
		default:
			v, err := parseValue(term, aliases)
			if err != nil {
				return 0, err
			}
			// Bare number with step (e.g. "5/15") means "starting at 5,
			// step by 15 up to hi" — Vixie/robfig behaviour.
			if step != 1 {
				rangeLo, rangeHi = v, hi
			} else {
				rangeLo, rangeHi = v, v
			}
		}
		// Allow dow=7 as Sunday alias — validate lets it through and
		// Parse normalises the bit afterwards.
		effectiveHi := hi
		if aliases != nil && lo == 0 && hi == 6 {
			effectiveHi = 7
		}
		if rangeLo < lo || rangeLo > effectiveHi || rangeHi < lo || rangeHi > effectiveHi {
			return 0, fmt.Errorf("value out of range [%d,%d] in %q", lo, hi, field)
		}
		if rangeHi < rangeLo {
			return 0, fmt.Errorf("range end before start in %q", field)
		}
		for v := rangeLo; v <= rangeHi; v += step {
			mask |= 1 << uint(v)
		}
	}
	if mask == 0 {
		return 0, fmt.Errorf("no values selected in %q", field)
	}
	return mask, nil
}

func parseValue(s string, aliases map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if aliases != nil {
		if v, ok := aliases[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return n, nil
}
