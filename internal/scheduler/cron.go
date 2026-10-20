// file: internal/scheduler/cron.go
// version: 1.1.2
// guid: 7c2f9a14-5be3-4d68-9a07-e1b4c3d85f26
// last-edited: 2026-10-20

package scheduler

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// cronSearchYears bounds Next and Prev: an expression with no fire time in
// this window (0 0 30 2 *) reports none rather than looping.
const cronSearchYears = 5

// CronSpec is a parsed 5-field expression ("m h dom mon dow"). Each field is a
// bitset of the values it accepts. Like standard cron, when both day-of-month
// and day-of-week are restricted a day matches if EITHER does; when one of
// them starts with "*" the other alone decides.
type CronSpec struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

// ParseCron parses "m h dom mon dow" with the one cron grammar in the module
// (ops.ParseCronFields). It returns an error naming the field for anything it
// does not accept. Supported: "*", "a", "a-b", "a,b,c", "*/n", "a-b/n". Not
// supported (error): names (JAN, MON), signs ("+5"), "?", "L", "W", "#",
// seconds, years, and macros such as @daily.
func ParseCron(expr string) (CronSpec, error) {
	f, err := ops.ParseCronFields(expr)
	if err != nil {
		return CronSpec{}, err
	}
	return CronSpec{
		minute: f.Minute, hour: f.Hour, dom: f.DayOfMonth, month: f.Month, dow: f.DayOfWeek,
		domStar: f.DayOfMonthStar, dowStar: f.DayOfWeekStar,
	}, nil
}

func zoneOffset(t time.Time) int { _, off := t.Zone(); return off }

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

// dayMatches applies the month, day-of-month and day-of-week rule to a date.
func (c CronSpec) dayMatches(y int, mo time.Month, d int, loc *time.Location) bool {
	if !has(c.month, int(mo)) {
		return false
	}
	domOK := has(c.dom, d)
	dowOK := has(c.dow, int(time.Date(y, mo, d, 12, 0, 0, 0, loc).Weekday()))
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// instant resolves a wall-clock time to the instant a fire at it happens, per
// the DST rule: a wall time inside a spring-forward gap fires at the first
// valid time after the gap, and one that occurs twice (fall back) resolves to
// its first occurrence.
func instant(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
	t := time.Date(y, mo, d, h, m, 0, 0, loc)
	if t.Hour() != h || t.Minute() != m || t.Day() != d {
		// In the gap. Go resolves it with one of the two offsets, landing
		// just before or just after the gap; find the transition instant
		// (the first valid time after the gap) by stepping to the other
		// offset. A gap is at most a couple of hours wide.
		_, off := t.Zone()
		for i := 1; i <= 180; i++ {
			if p := t.Add(time.Duration(i) * time.Minute); zoneOffset(p) != off {
				return p
			}
		}
		for i := 1; i <= 180; i++ {
			p := t.Add(-time.Duration(i) * time.Minute)
			if zoneOffset(p) != off {
				return p.Add(time.Minute)
			}
		}
		return t
	}
	// Possibly the second of two occurrences: prefer the first.
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute} {
		if e := t.Add(-back); e.Hour() == h && e.Minute() == m && e.Day() == d {
			return e
		}
	}
	return t
}

// Next returns the first fire time strictly after `after`, in loc, or the zero
// time and false if none exists within 5 years. DST rules: a time that does not
// exist (spring forward) fires at the first valid time after the gap; a time
// that occurs twice (fall back) fires once, on the first occurrence.
func (c CronSpec) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	return c.next(after, loc, nil)
}

// next is Next with an optional probe: when non-nil it is incremented once per
// candidate instant examined, so a test can assert how much of the first day
// was scanned without timing anything.
func (c CronSpec) next(after time.Time, loc *time.Location, probe *int) (time.Time, bool) {
	a := after.In(loc)
	limit := a.AddDate(cronSearchYears, 0, 0)
	y, mo, d := a.Date()
	for day := time.Date(y, mo, d, 12, 0, 0, 0, loc); day.Before(limit); day = day.AddDate(0, 0, 1) {
		dy, dmo, dd := day.Date()
		if !c.dayMatches(dy, dmo, dd, loc) {
			continue
		}
		// On the first day nothing before `after`'s wall-clock hour can be
		// later than it, so start there (one hour earlier, to stay clear of a
		// DST gap or repeated hour). Without this every call rescanned the
		// day from midnight, and the scheduler's catch-up count (up to 1000
		// calls) cost about half a second.
		h0 := 0
		if day.Day() == a.Day() && day.Month() == a.Month() && day.Year() == a.Year() {
			h0 = max(0, a.Hour()-1)
		}
		for h := h0; h < 24; h++ {
			if !has(c.hour, h) {
				continue
			}
			for m := range 60 {
				if !has(c.minute, m) {
					continue
				}
				if probe != nil {
					*probe++
				}
				if t := instant(dy, dmo, dd, h, m, loc); t.After(after) {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// Prev returns the most recent fire time at or before `at` (used for
// missed-fire detection), or the zero time and false within 5 years.
func (c CronSpec) Prev(at time.Time, loc *time.Location) (time.Time, bool) {
	a := at.In(loc)
	limit := a.AddDate(-cronSearchYears, 0, 0)
	y, mo, d := a.Date()
	for day := time.Date(y, mo, d, 12, 0, 0, 0, loc); day.After(limit); day = day.AddDate(0, 0, -1) {
		dy, dmo, dd := day.Date()
		if !c.dayMatches(dy, dmo, dd, loc) {
			continue
		}
		for h := 23; h >= 0; h-- {
			if !has(c.hour, h) {
				continue
			}
			for m := 59; m >= 0; m-- {
				if !has(c.minute, m) {
					continue
				}
				if t := instant(dy, dmo, dd, h, m, loc); !t.After(at) {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}
