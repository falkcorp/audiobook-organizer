// file: core/internal/scheduler/daily.go
// version: 1.0.0
// guid: 52a236a4-3599-42ed-b162-7d00f3cab861
// last-edited: 2026-11-11

package scheduler

import (
	"fmt"
	"time"
)

// dailyAt is ScheduleConfig.DailyAt, "HH:MM" on the 24-hour clock in
// core.tasks.timezone: once a day at that wall time (HEAD's DailyAt trigger,
// internal/scheduler daily_at.go, re-implemented).
//
// It is the cron spec "MM HH * * *", so DailyAt and Cron share one DST rule
// (wallInstant): each candidate is built from the wall date, never by adding
// 24h to the last one (a local day is 23h or 25h across a transition), a
// time the zone skips fires at the first instant after the gap, and a time
// the zone repeats fires at its first occurrence only. HEAD's DailyAt
// resolved a skipped time to before the gap (01:30 standard time for 02:30 in
// a US zone); 05-tasks 5T.3.8 says after, and that is what this does.
type dailyAt struct {
	hour, minute int
	spec         cronSpec
}

// parseDailyAt parses "HH:MM" (two digits each; 00:00-23:59).
func parseDailyAt(s string) (dailyAt, error) {
	if len(s) != 5 || s[2] != ':' {
		return dailyAt{}, fmt.Errorf("daily_at %q is not a 24-hour HH:MM value", s)
	}
	h, errH := cronNumber(s[:2])
	m, errM := cronNumber(s[3:])
	if errH != nil || errM != nil || h > 23 || m > 59 {
		return dailyAt{}, fmt.Errorf("daily_at %q is not a 24-hour HH:MM value", s)
	}
	all := func(lo, hi int) uint64 {
		var set uint64
		for v := lo; v <= hi; v++ {
			set |= 1 << uint(v)
		}
		return set
	}
	return dailyAt{hour: h, minute: m, spec: cronSpec{
		minute: 1 << uint(m), hour: 1 << uint(h), dom: all(1, 31), month: all(1, 12), dow: all(0, 6),
		domStar: true, dowStar: true,
	}}, nil
}

// next is the first occurrence strictly after `after` (see dailyAt).
func (d dailyAt) next(after time.Time, loc *time.Location) (time.Time, bool) {
	return d.spec.next(after, loc)
}
