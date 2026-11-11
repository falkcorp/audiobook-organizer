// file: core/internal/scheduler/cron.go
// version: 1.0.0
// guid: 229fe347-2ff3-4325-ac35-cc0f6214aa01
// last-edited: 2026-11-11

package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec is a parsed 5-field cron expression, ScheduleConfig.Cron, evaluated
// in core.tasks.timezone. It re-implements HEAD's grammar (internal/scheduler
// cron.go over pkg/ops' field parser) and copies none of its code: per field
// "*", "a", "a-b", "a,b,c", "*/n", "a-b/n" and "a/n" (from a to the field's
// end, as in Vixie cron); no names, no "?", "L", "W" or "#". Day-of-week 7 is
// Sunday, folded onto 0. When both day fields are restricted a day matches
// either (Vixie cron's OR); when either starts with "*" it matches both (AND).
type cronSpec struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

// cronFields are the five fields' names and inclusive bounds.
func cronFields() [5]struct {
	name   string
	lo, hi int
} {
	return [5]struct {
		name   string
		lo, hi int
	}{{"minute", 0, 59}, {"hour", 0, 23}, {"day-of-month", 1, 31}, {"month", 1, 12}, {"day-of-week", 0, 7}}
}

// parseCron parses expr.
func parseCron(expr string) (cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSpec{}, fmt.Errorf("cron %q: want 5 fields, got %d", expr, len(fields))
	}
	var sets [5]uint64
	for i, f := range cronFields() {
		set, err := parseCronField(fields[i], f.lo, f.hi)
		if err != nil {
			return cronSpec{}, fmt.Errorf("cron %q: %s field %q: %w", expr, f.name, fields[i], err)
		}
		sets[i] = set
	}
	if sets[4]&(1<<7) != 0 {
		sets[4] = sets[4]&^(1<<7) | 1
	}
	return cronSpec{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: strings.HasPrefix(fields[2], "*"), dowStar: strings.HasPrefix(fields[4], "*"),
	}, nil
}

// parseCronField parses one comma-separated field into a bit set over
// [lo, hi].
func parseCronField(f string, lo, hi int) (uint64, error) {
	var set uint64
	for part := range strings.SplitSeq(f, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := cronNumber(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}
		a, b := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var err error
			if a, err = cronValue(x, lo, hi); err != nil {
				return 0, err
			}
			if b, err = cronValue(y, lo, hi); err != nil {
				return 0, err
			}
			if a > b {
				return 0, fmt.Errorf("range %q is backwards", rng)
			}
		default:
			n, err := cronValue(rng, lo, hi)
			if err != nil {
				return 0, err
			}
			a, b = n, n
			if hasStep {
				b = hi
			}
		}
		for v := a; v <= b; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

// cronNumber parses an unsigned decimal (strconv.Atoi alone would take "+5").
func cronNumber(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a number", s)
		}
	}
	return strconv.Atoi(s)
}

func cronValue(s string, lo, hi int) (int, error) {
	n, err := cronNumber(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number (names, signs, ?, L, W and # are not supported)", s)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%d is outside %d-%d", n, lo, hi)
	}
	return n, nil
}

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

// dayMatches reports whether the local date y-mo-d matches the day fields.
func (c cronSpec) dayMatches(y int, mo time.Month, d int) bool {
	if !has(c.month, int(mo)) {
		return false
	}
	domOK := has(c.dom, d)
	dowOK := has(c.dow, int(time.Date(y, mo, d, 12, 0, 0, 0, time.UTC).Weekday()))
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// cronHorizon bounds the search for the next match: a spec that matches no
// date inside it (0 0 31 2 *) never comes due.
const cronHorizon = 5 * 366

// next is the first instant strictly after `after` whose wall clock in loc
// matches c, with DST resolved by wallInstant: a wall time the zone skips
// resolves to the first instant after the gap, and a repeated wall time to
// its first occurrence only. ok is false when nothing matches within
// cronHorizon days.
func (c cronSpec) next(after time.Time, loc *time.Location) (time.Time, bool) {
	a := after.In(loc)
	y, mo, d := a.Date()
	for i := range cronHorizon {
		day := time.Date(y, mo, d+i, 12, 0, 0, 0, time.UTC) // calendar arithmetic only
		dy, dmo, dd := day.Date()
		if !c.dayMatches(dy, dmo, dd) {
			continue
		}
		for h := range 24 {
			if !has(c.hour, h) {
				continue
			}
			for m := range 60 {
				if !has(c.minute, m) {
					continue
				}
				if t := wallInstant(dy, dmo, dd, h, m, loc); t.After(after) {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// wallInstant is the instant the wall clock y-mo-d h:m first reads in loc.
// A wall time the zone skips (a spring-forward gap) has no such instant; it
// resolves to the first instant after the gap, the transition itself (05-tasks
// 5T.3.8: "a skipped wall time fires at the first instant after"). A wall
// time the zone repeats (a fall-back overlap) resolves to its first
// occurrence, so a schedule at that time fires once ("a repeated one fires
// once"). time.Date leaves both cases unspecified, so the candidates are
// built from the zone's offsets on either side and checked.
func wallInstant(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
	naive := time.Date(y, mo, d, h, m, 0, 0, time.UTC)
	reads := func(t time.Time) bool {
		l := t.In(loc)
		ly, lmo, ld := l.Date()
		return ly == naive.Year() && lmo == naive.Month() && ld == naive.Day() && l.Hour() == h && l.Minute() == m
	}
	// The offsets in force a day either side bound every candidate: a zone
	// changes offset at most once in a day in practice, and both sides are
	// tried.
	_, offBefore := naive.Add(-26 * time.Hour).In(loc).Zone()
	_, offAfter := naive.Add(26 * time.Hour).In(loc).Zone()
	early := naive.Add(-time.Duration(max(offBefore, offAfter)) * time.Second)
	late := naive.Add(-time.Duration(min(offBefore, offAfter)) * time.Second)
	switch {
	case reads(early):
		return early
	case reads(late):
		return late
	}
	// Neither offset reads the wall time: it falls in a gap. The instant
	// computed with the offset after the gap reads earlier than the wall
	// time (it is before the transition), the one computed with the offset
	// before reads later (after it); the transition, the first instant
	// after the gap, is the first instant between them whose offset is the
	// later one. time.Time.ZoneBounds cannot be used: it reports no bounds
	// for a zone that runs on its POSIX rule alone.
	lo := naive.Add(-time.Duration(offAfter) * time.Second)
	hi := naive.Add(-time.Duration(offBefore) * time.Second)
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2).Truncate(time.Second)
		if _, off := mid.In(loc).Zone(); off == offAfter {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}
