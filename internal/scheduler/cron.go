// file: internal/scheduler/cron.go
// version: 1.0.0
// guid: 7c2f9a14-5be3-4d68-9a07-e1b4c3d85f26
// last-edited: 2026-10-17

package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSearchYears bounds Next and Prev: an expression with no fire time in
// this window (0 0 30 2 *) reports none rather than looping.
const cronSearchYears = 5

// cronBounds are the inclusive ranges of the five fields. Day-of-week accepts
// 7 as Sunday, as standard cron does; it is folded onto 0 when parsed.
var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

var cronFieldNames = [5]string{"minute", "hour", "day-of-month", "month", "day-of-week"}

// CronSpec is a parsed 5-field expression ("m h dom mon dow"). Each field is a
// bitset of the values it accepts. Like standard cron, when both day-of-month
// and day-of-week are restricted a day matches if EITHER does; when one of
// them starts with "*" the other alone decides.
type CronSpec struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

// ParseCron parses "m h dom mon dow". It returns an error naming the field for
// anything it does not accept. Supported: "*", "a", "a-b", "a,b,c", "*/n",
// "a-b/n". Not supported (error): names (JAN, MON), "?", "L", "W", "#",
// seconds, years, and macros such as @daily.
func ParseCron(expr string) (CronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return CronSpec{}, fmt.Errorf("cron %q: want 5 fields, got %d", expr, len(fields))
	}
	var sets [5]uint64
	for i, f := range fields {
		set, err := parseCronField(f, cronBounds[i])
		if err != nil {
			return CronSpec{}, fmt.Errorf("cron %q: %s field %q: %w", expr, cronFieldNames[i], f, err)
		}
		sets[i] = set
	}
	// 7 is Sunday: fold it onto 0.
	if sets[4]&(1<<7) != 0 {
		sets[4] = sets[4]&^(1<<7) | 1
	}
	return CronSpec{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: strings.HasPrefix(fields[2], "*"), dowStar: strings.HasPrefix(fields[4], "*"),
	}, nil
}

func parseCronField(f string, bounds [2]int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(f, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}
		lo, hi := bounds[0], bounds[1]
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = cronNum(a, bounds); err != nil {
				return 0, err
			}
			if hi, err = cronNum(b, bounds); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("range %q is backwards", rng)
			}
		default:
			n, err := cronNum(rng, bounds)
			if err != nil {
				return 0, err
			}
			lo, hi = n, n
			if hasStep {
				hi = bounds[1] // "a/n" runs from a to the end, as in Vixie cron
			}
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

func cronNum(s string, bounds [2]int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number (names, ?, L, W and # are not supported)", s)
	}
	if n < bounds[0] || n > bounds[1] {
		return 0, fmt.Errorf("value %d out of range %d-%d", n, bounds[0], bounds[1])
	}
	return n, nil
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
	a := after.In(loc)
	limit := a.AddDate(cronSearchYears, 0, 0)
	y, mo, d := a.Date()
	for day := time.Date(y, mo, d, 12, 0, 0, 0, loc); day.Before(limit); day = day.AddDate(0, 0, 1) {
		dy, dmo, dd := day.Date()
		if !c.dayMatches(dy, dmo, dd, loc) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !has(c.hour, h) {
				continue
			}
			for m := 0; m < 60; m++ {
				if !has(c.minute, m) {
					continue
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
