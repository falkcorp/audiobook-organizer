// file: core/internal/schedspec/schedspec.go
// version: 1.0.0
// guid: 5c7e2f19-3b8a-4d64-a0e2-91f4b6d8c3a7
// last-edited: 2026-11-11

// Package schedspec is the grammar of a schedule's values, shared by the two
// packages that read it: core/tasks checks a tasks.ScheduleConfig and a
// schedule key's form with it when config loads a value or a definition is
// built, and core/internal/scheduler evaluates the same parse at every poll.
// One parser, so a value config accepts is a value the scheduler can run.
// It imports only the standard library (layer 0). Package coined by F2-04.
//
// Cron is a 5-field expression re-implementing HEAD's grammar
// (internal/scheduler cron.go over pkg/ops' field parser; none of its code is
// copied): per field "*", "a", "a-b", "a,b,c", "*/n", "a-b/n" and "a/n" (from
// a to the field's end, as in Vixie cron); no names, no "?", "L", "W" or "#".
// Day-of-week 7 is Sunday, folded onto 0. When both day fields are restricted
// a day matches either (Vixie cron's OR); when either starts with "*" it
// matches both (AND). DailyAt "HH:MM" is the cron spec "MM HH * * *", so the
// two share one DST rule (WallInstant).
package schedspec

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed cron expression or DailyAt value.
type Cron struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

type field struct {
	name   string
	lo, hi int
}

// fields are the five fields' names and inclusive bounds.
func fields() [5]field {
	return [5]field{{"minute", 0, 59}, {"hour", 0, 23}, {"day-of-month", 1, 31}, {"month", 1, 12}, {"day-of-week", 0, 7}}
}

// ParseCron parses a 5-field cron expression.
func ParseCron(expr string) (Cron, error) {
	fs := strings.Fields(expr)
	if len(fs) != 5 {
		return Cron{}, fmt.Errorf("cron %q: want 5 fields, got %d", expr, len(fs))
	}
	var sets [5]uint64
	for i, f := range fields() {
		set, err := parseField(fs[i], f.lo, f.hi)
		if err != nil {
			return Cron{}, fmt.Errorf("cron %q: %s field %q: %w", expr, f.name, fs[i], err)
		}
		sets[i] = set
	}
	if sets[4]&(1<<7) != 0 {
		sets[4] = sets[4]&^(1<<7) | 1
	}
	return Cron{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: strings.HasPrefix(fs[2], "*"), dowStar: strings.HasPrefix(fs[4], "*"),
	}, nil
}

// ParseDailyAt parses "HH:MM" (two digits each; 00:00-23:59) as the cron
// spec "MM HH * * *": once a day at that wall time (HEAD's DailyAt trigger,
// daily_at.go, re-implemented). Each candidate is built from the wall date,
// never by adding 24h to the last one (a local day is 23h or 25h across a
// transition). HEAD resolved a skipped time to before the gap (01:30 standard
// time for 02:30 in a US zone); 05-tasks 5T.3.8 says after, and Next does.
func ParseDailyAt(s string) (Cron, error) {
	if len(s) != 5 || s[2] != ':' {
		return Cron{}, fmt.Errorf("daily_at %q is not a 24-hour HH:MM value", s)
	}
	h, errH := number(s[:2])
	m, errM := number(s[3:])
	if errH != nil || errM != nil || h > 23 || m > 59 {
		return Cron{}, fmt.Errorf("daily_at %q is not a 24-hour HH:MM value", s)
	}
	all := func(lo, hi int) uint64 {
		var set uint64
		for v := lo; v <= hi; v++ {
			set |= 1 << uint(v)
		}
		return set
	}
	return Cron{minute: 1 << uint(m), hour: 1 << uint(h), dom: all(1, 31), month: all(1, 12), dow: all(0, 6),
		domStar: true, dowStar: true}, nil
}

// parseField parses one comma-separated field into a bit set over [lo, hi].
func parseField(f string, lo, hi int) (uint64, error) {
	var set uint64
	for part := range strings.SplitSeq(f, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := number(stepStr)
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
			if a, err = value(x, lo, hi); err != nil {
				return 0, err
			}
			if b, err = value(y, lo, hi); err != nil {
				return 0, err
			}
			if a > b {
				return 0, fmt.Errorf("range %q is backwards", rng)
			}
		default:
			n, err := value(rng, lo, hi)
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

// number parses an unsigned decimal (strconv.Atoi alone would take "+5").
func number(s string) (int, error) {
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

func value(s string, lo, hi int) (int, error) {
	n, err := number(s)
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
func (c Cron) dayMatches(y int, mo time.Month, d int) bool {
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

// horizon bounds the search for the next match in days: a spec that matches
// no date inside it (0 0 31 2 *) never comes due.
const horizon = 5 * 366

// Next is the first instant strictly after `after` whose wall clock in loc
// matches c, with DST resolved by WallInstant. ok is false when nothing
// matches within the horizon. The scan of after's own date starts at after's
// wall hour and minute: an earlier wall time on that date is an instant
// before after (a repeated hour resolves to its first occurrence, which is
// earlier still), so it can never be the answer.
func (c Cron) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	a := after.In(loc)
	y, mo, d := a.Date()
	for i := range horizon {
		day := time.Date(y, mo, d+i, 12, 0, 0, 0, time.UTC) // calendar arithmetic only
		dy, dmo, dd := day.Date()
		if !c.dayMatches(dy, dmo, dd) {
			continue
		}
		h0 := 0
		if i == 0 {
			h0 = a.Hour()
		}
		for h := h0; h < 24; h++ {
			if !has(c.hour, h) {
				continue
			}
			m0 := 0
			if i == 0 && h == a.Hour() {
				m0 = a.Minute()
			}
			for m := m0; m < 60; m++ {
				if !has(c.minute, m) {
					continue
				}
				if t := WallInstant(dy, dmo, dd, h, m, loc); t.After(after) {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// WallInstant is the instant the wall clock y-mo-d h:m first reads in loc. A
// wall time the zone skips (a spring-forward gap) has no such instant; it
// resolves to the first instant after the gap, the transition itself (05-tasks
// 5T.3.8: "a skipped wall time fires at the first instant after"). A wall time
// the zone repeats (a fall-back overlap) resolves to its first occurrence, so a
// schedule at that time fires once ("a repeated one fires once"). time.Date
// leaves both cases unspecified, so the candidates are built from the zone's
// offsets on either side and checked. Out-of-range days normalise as
// time.Date does.
func WallInstant(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
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
	// computed with the offset after the gap is before the transition, the one
	// computed with the offset before is after it; the transition, the first
	// instant after the gap, is the first instant between them whose offset is
	// the later one. time.Time.ZoneBounds cannot be used: it reports no bounds
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

// Segment reports whether seg matches the config path grammar
// [a-z][a-z0-9_]{0,47}.
func Segment(seg string) bool {
	if seg == "" || len(seg) > 48 || seg[0] < 'a' || seg[0] > 'z' {
		return false
	}
	for i := 1; i < len(seg); i++ {
		c := seg[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// KeyOwner checks a schedule key's form (R2-7),
// "core.<subsystem>.schedules.<name>" or
// "modules.<id>.<subsystem>.schedules.<name>" with every segment a Segment,
// and returns the owner its namespace names: "core", or the module ID.
func KeyOwner(key string) (string, error) {
	segs := strings.Split(key, ".")
	for i, seg := range segs {
		if !Segment(seg) {
			return "", fmt.Errorf("schedule key %q: segment %d (%q) does not match [a-z][a-z0-9_]{0,47}", key, i+1, seg)
		}
	}
	var owner string
	var rest []string
	switch {
	case len(segs) == 4 && segs[0] == "core":
		owner, rest = "core", segs[1:]
	case len(segs) == 5 && segs[0] == "modules":
		owner, rest = segs[1], segs[2:]
	}
	if rest == nil || rest[1] != "schedules" || rest[0] == "schedules" {
		return "", fmt.Errorf("schedule key %q: want core.<subsystem>.schedules.<name> or modules.<id>.<subsystem>.schedules.<name>", key)
	}
	return owner, nil
}
