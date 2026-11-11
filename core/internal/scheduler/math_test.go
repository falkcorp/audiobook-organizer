// file: core/internal/scheduler/math_test.go
// version: 1.0.0
// guid: d0feac80-13ca-4ad9-8d21-62931949112f
// last-edited: 2026-11-11

package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseCron_Grammar(t *testing.T) {
	good := []string{"* * * * *", "0 3 * * *", "*/15 1-4 * * 1-5", "5,10,15 0 1 1 0", "0 0 * * 7", "1-59/2 * * * *", "30/10 * * * *", "0 0 29 2 *"}
	for _, e := range good {
		if _, err := parseCron(e); err != nil {
			t.Errorf("parseCron(%q) = %v", e, err)
		}
	}
	bad := []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8",
		"*/0 * * * *", "5-1 * * * *", "MON * * * *", "* * ? * *", "* * L * *", "+5 * * * *", "* * * * 1#2", "-1 * * * *", "1- * * * *"}
	for _, e := range bad {
		if _, err := parseCron(e); err == nil {
			t.Errorf("parseCron(%q) accepted", e)
		}
	}
	c, _ := parseCron("0 0 * * 7")
	if !has(c.dow, 0) || has(c.dow, 7) {
		t.Fatal("day-of-week 7 is not folded onto Sunday")
	}
	c, _ = parseCron("50/5 * * * *")
	for _, m := range []int{50, 55} {
		if !has(c.minute, m) {
			t.Fatalf("a/n does not run to the field's end: %d missing", m)
		}
	}
	if has(c.minute, 45) || has(c.minute, 51) {
		t.Fatal("a/n matched outside its steps")
	}
}

func TestCronNext_DaysAndRollover(t *testing.T) {
	cases := []struct{ expr, after, want string }{
		{"0 3 * * *", "2026-01-01T02:59:00Z", "2026-01-01T03:00:00Z"},
		{"0 3 * * *", "2026-01-01T03:00:00Z", "2026-01-02T03:00:00Z"}, // strictly after
		{"*/15 * * * *", "2026-01-01T10:07:00Z", "2026-01-01T10:15:00Z"},
		{"0 0 1 1 *", "2026-06-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"0 0 29 2 *", "2026-03-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		// both day fields restricted: either matches (2026-01-05 is a Monday)
		{"0 0 13 * 1", "2026-01-01T00:00:00Z", "2026-01-05T00:00:00Z"},
		// day-of-week starred: day-of-month alone decides
		{"0 0 13 * *", "2026-01-01T00:00:00Z", "2026-01-13T00:00:00Z"},
		{"0 0 * * 1", "2026-01-01T00:00:00Z", "2026-01-05T00:00:00Z"},
	}
	for _, c := range cases {
		s, err := parseCron(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := s.next(utc(c.after), time.UTC)
		if !ok || !got.Equal(utc(c.want)) {
			t.Errorf("%q after %s = %s %v, want %s", c.expr, c.after, got, ok, c.want)
		}
	}
	never, _ := parseCron("0 0 31 2 *")
	if got, ok := never.next(utc("2026-01-01T00:00:00Z"), time.UTC); ok {
		t.Fatalf("0 0 31 2 * came due at %s", got)
	}
}

func TestWallInstant_DST(t *testing.T) {
	loc := GothamZone(t)
	cases := []struct {
		name    string
		y       int
		mo      time.Month
		d, h, m int
		want    string
	}{
		{"plain winter", 2026, 1, 15, 1, 30, "2026-01-15T06:30:00Z"},
		{"plain summer", 2026, 7, 15, 1, 30, "2026-07-15T05:30:00Z"},
		{"gap start resolves to the transition", 2026, 3, 8, 2, 0, "2026-03-08T07:00:00Z"},
		{"inside the gap resolves to the transition", 2026, 3, 8, 2, 30, "2026-03-08T07:00:00Z"},
		{"after the gap", 2026, 3, 8, 3, 0, "2026-03-08T07:00:00Z"},
		{"before the gap", 2026, 3, 8, 1, 59, "2026-03-08T06:59:00Z"},
		{"repeated hour: first occurrence", 2026, 11, 1, 1, 30, "2026-11-01T05:30:00Z"},
		{"repeated hour start: first occurrence", 2026, 11, 1, 1, 0, "2026-11-01T05:00:00Z"},
		{"after the repeat", 2026, 11, 1, 2, 0, "2026-11-01T07:00:00Z"},
		{"day overflow normalises", 2026, 1, 32, 0, 0, "2026-02-01T05:00:00Z"},
	}
	for _, c := range cases {
		if got := wallInstant(c.y, c.mo, c.d, c.h, c.m, loc); !got.Equal(utc(c.want)) {
			t.Errorf("%s: %s, want %s", c.name, got.UTC(), c.want)
		}
	}
}

func TestDailyAt_ParseAndDST(t *testing.T) {
	for _, bad := range []string{"", "3:00", "24:00", "12:60", "12-00", "ab:cd", "12:00:00", "+1:00"} {
		if _, err := parseDailyAt(bad); err == nil {
			t.Errorf("parseDailyAt(%q) accepted", bad)
		}
	}
	loc := GothamZone(t)
	d, err := parseDailyAt("02:30")
	if err != nil {
		t.Fatal(err)
	}
	// Spring forward: 02:30 does not exist; it fires at 03:00 GDT, then the
	// next day at 02:30 GDT.
	got, _ := d.next(utc("2026-03-08T06:00:00Z"), loc)
	if !got.Equal(utc("2026-03-08T07:00:00Z")) {
		t.Fatalf("spring-forward day: %s", got.UTC())
	}
	got, _ = d.next(got.Add(time.Minute), loc)
	if !got.Equal(utc("2026-03-09T06:30:00Z")) {
		t.Fatalf("day after: %s", got.UTC())
	}
	d, _ = parseDailyAt("01:30")
	// Fall back: 01:30 happens twice; a stamp after the first means
	// tomorrow, never the second copy an hour later.
	got, _ = d.next(utc("2026-11-01T05:31:00Z"), loc)
	if !got.Equal(utc("2026-11-02T06:30:00Z")) {
		t.Fatalf("fall-back day after the first 01:30: %s", got.UTC())
	}
	got, _ = d.next(utc("2026-11-01T04:00:00Z"), loc)
	if !got.Equal(utc("2026-11-01T05:30:00Z")) {
		t.Fatalf("fall-back day before: %s", got.UTC())
	}
}

func TestEffectiveOf_Rules(t *testing.T) {
	poll := time.Minute
	bad := []tasks.ScheduleConfig{
		{Enabled: true},
		{Enabled: true, Every: config.Duration(time.Hour), DailyAt: "03:00"},
		{Enabled: true, Cron: "0 3 * * *", Window: "nightly"},
		{Enabled: true, Every: config.Duration(30 * time.Second)},
		{Enabled: true, Every: config.Duration(-time.Hour)},
		{Enabled: true, DailyAt: "25:00"},
		{Enabled: true, Cron: "bat signal"},
		{Enabled: true, Window: "Night-ly"},
	}
	for _, c := range bad {
		if _, err := effectiveOf(c, poll, time.UTC); !errors.Is(err, errConfig) {
			t.Errorf("effectiveOf(%+v) = %v, want errConfig", c, err)
		}
	}
	if e, err := effectiveOf(tasks.ScheduleConfig{Every: config.Duration(time.Second), Cron: "x"}, poll, time.UTC); err != nil || e.enabled {
		t.Fatalf("a disabled value was checked: %+v %v", e, err)
	}
	ok := []tasks.ScheduleConfig{
		{Enabled: true, Every: config.Duration(time.Minute)},
		{Enabled: true, DailyAt: "03:00"},
		{Enabled: true, Cron: "0 3 * * *"},
		{Enabled: true, Window: "nightly"},
	}
	seen := map[string]bool{}
	for _, c := range ok {
		e, err := effectiveOf(c, poll, time.UTC)
		if err != nil || !e.enabled || e.hash == "" || seen[e.hash] {
			t.Fatalf("effectiveOf(%+v) = %+v %v", c, e, err)
		}
		seen[e.hash] = true
	}
	a, _ := effectiveOf(tasks.ScheduleConfig{Enabled: true, DailyAt: "03:00"}, poll, time.UTC)
	b, _ := effectiveOf(tasks.ScheduleConfig{Enabled: true, DailyAt: "03:00"}, poll, GothamZone(t))
	if a.hash == b.hash {
		t.Fatal("the zone is not part of a DailyAt hash")
	}
}

func TestDecide_Table(t *testing.T) {
	poll := time.Minute
	t0 := utc("2026-01-01T00:00:00Z")
	every := func(d time.Duration) effective {
		return effective{enabled: true, t: interval(d), hash: "h"}
	}
	in := func(now time.Time, e effective, m tasks.MissedPolicy) pollInput {
		return pollInput{now: now, poll: poll, loc: time.UTC, eff: e, missed: m}
	}
	t.Run("first sight seeds and does not fire", func(t *testing.T) {
		d := decide(tasks.Clock{}, false, in(t0, every(time.Hour), tasks.Coalesce))
		if !d.write || d.fire || !d.next.LastDue.Equal(t0) || d.next.ScheduleHash != "h" || !d.next.GateClosedAt.IsZero() {
			t.Fatalf("%+v", d)
		}
		d = decide(tasks.Clock{}, false, in(t0, effective{}, tasks.Coalesce))
		if !d.write || d.fire || !d.next.GateClosedAt.Equal(t0) {
			t.Fatalf("disabled first sight: %+v", d)
		}
	})
	prev := tasks.Clock{LastDue: t0, ScheduleHash: "h"}
	t.Run("not yet due writes nothing", func(t *testing.T) {
		if d := decide(prev, true, in(t0.Add(59*time.Minute), every(time.Hour), tasks.Coalesce)); d.write || d.fire {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("due exactly at the instant", func(t *testing.T) {
		now := t0.Add(time.Hour)
		d := decide(prev, true, in(now, every(time.Hour), tasks.Coalesce))
		if !d.write || !d.fire || d.missed != 0 || !d.dueAt.Equal(now) || !d.next.LastDue.Equal(now) {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("coalesce counts the missed", func(t *testing.T) {
		now := t0.Add(3*time.Hour + 30*time.Minute)
		d := decide(tasks.Clock{LastDue: t0, Missed: 4}, true, in(now, every(time.Hour), tasks.Coalesce))
		if !d.fire || d.missed != 2 || d.next.Missed != 6 || !d.dueAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("skip missed waits after downtime", func(t *testing.T) {
		now := t0.Add(3*time.Hour + 30*time.Minute)
		d := decide(prev, true, in(now, every(time.Hour), tasks.SkipMissed))
		if d.fire || !d.write || d.missed != 3 || !d.next.LastDue.Equal(now) {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("skip missed fires when on time", func(t *testing.T) {
		now := t0.Add(3*time.Hour + 30*time.Second)
		d := decide(prev, true, in(now, every(time.Hour), tasks.SkipMissed))
		if !d.fire || d.missed != 2 || !d.dueAt.Equal(t0.Add(3*time.Hour)) {
			t.Fatalf("%+v", d)
		}
		now = t0.Add(3*time.Hour + poll)
		if d := decide(prev, true, in(now, every(time.Hour), tasks.SkipMissed)); !d.fire {
			t.Fatalf("one poll late is on time: %+v", d)
		}
		now = t0.Add(3*time.Hour + poll + time.Second)
		if d := decide(prev, true, in(now, every(time.Hour), tasks.SkipMissed)); d.fire {
			t.Fatalf("more than one poll late is missed: %+v", d)
		}
	})
	t.Run("disabled stamps the close edge once", func(t *testing.T) {
		now := t0.Add(5 * time.Hour)
		d := decide(prev, true, in(now, effective{}, tasks.Coalesce))
		if !d.write || d.fire || !d.next.GateClosedAt.Equal(now) || !d.next.LastDue.Equal(t0) {
			t.Fatalf("%+v", d)
		}
		if d := decide(d.next, true, in(now.Add(time.Hour), effective{}, tasks.Coalesce)); d.write || d.fire {
			t.Fatalf("second closed poll: %+v", d)
		}
	})
	t.Run("re-open fires once with nothing missed", func(t *testing.T) {
		closed := tasks.Clock{LastDue: t0, GateClosedAt: t0.Add(30 * time.Minute)}
		now := t0.Add(10 * time.Hour)
		d := decide(closed, true, in(now, every(time.Hour), tasks.Coalesce))
		if !d.fire || d.missed != 0 || !d.next.GateClosedAt.IsZero() || d.next.Missed != 0 {
			t.Fatalf("%+v", d)
		}
		if d := decide(closed, true, in(t0.Add(10*time.Minute), every(time.Hour), tasks.Coalesce)); !d.write || d.fire || !d.next.GateClosedAt.IsZero() {
			t.Fatalf("re-open before due clears the edge only: %+v", d)
		}
	})
	t.Run("hash change keeps LastDue", func(t *testing.T) {
		old := tasks.Clock{LastDue: t0, ScheduleHash: "old"}
		now := t0.Add(2 * time.Hour)
		d := decide(old, true, in(now, every(time.Hour), tasks.Coalesce))
		if !d.fire || d.next.ScheduleHash != "h" || !d.dueAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("%+v", d)
		}
		imported := tasks.Clock{LastDue: t0}
		if d := decide(imported, true, in(t0.Add(30*time.Minute), every(time.Hour), tasks.Coalesce)); d.write {
			t.Fatalf("an imported clock was re-seeded: %+v", d)
		}
	})
	t.Run("no next instant never fires", func(t *testing.T) {
		never, _ := parseCron("0 0 31 2 *")
		if d := decide(prev, true, in(t0.Add(1000*time.Hour), effective{enabled: true, t: never, hash: "h"}, tasks.Coalesce)); d.write || d.fire {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("count is capped", func(t *testing.T) {
		n, _ := countDue(interval(time.Minute), t0, t0.Add(10000*time.Minute), time.UTC)
		if n != maxCount {
			t.Fatalf("interval count %d, want %d", n, maxCount)
		}
		c, _ := parseCron("* * * * *")
		n, _ = countDue(c, t0, t0.Add(10000*time.Minute), time.UTC)
		if n != maxCount {
			t.Fatalf("cron count %d, want %d", n, maxCount)
		}
	})
}

func TestWindowOpen(t *testing.T) {
	night := WindowConfig{Start: 1, End: 4, Enabled: true}
	wrap := WindowConfig{Start: 23, End: 4, Enabled: true}
	cases := []struct {
		w      WindowConfig
		now    string
		in     bool
		opened string
	}{
		{night, "2026-01-02T00:59:00Z", false, ""},
		{night, "2026-01-02T01:00:00Z", true, "2026-01-02T01:00:00Z"},
		{night, "2026-01-02T03:59:00Z", true, "2026-01-02T01:00:00Z"},
		{night, "2026-01-02T04:00:00Z", false, ""},
		{wrap, "2026-01-02T22:59:00Z", false, ""},
		{wrap, "2026-01-02T23:30:00Z", true, "2026-01-02T23:00:00Z"},
		{wrap, "2026-01-03T00:30:00Z", true, "2026-01-02T23:00:00Z"},
		{wrap, "2026-01-03T04:00:00Z", false, ""},
		{WindowConfig{Start: 5, End: 5}, "2026-01-03T04:00:00Z", true, "2026-01-02T05:00:00Z"},
	}
	for _, c := range cases {
		in, opened := windowOpen(utc(c.now), c.w, time.UTC)
		if in != c.in || (c.in && !opened.Equal(utc(c.opened))) {
			t.Errorf("%+v at %s = %v %s, want %v %s", c.w, c.now, in, opened, c.in, c.opened)
		}
	}
}

func TestDecideWindow_OncePerOpeningByDate(t *testing.T) {
	night := WindowConfig{Start: 1, End: 4, Enabled: true}
	wrap := WindowConfig{Start: 23, End: 4, Enabled: true}
	at := utc
	if d := decideWindow(tasks.Clock{}, false, at("2026-01-02T02:00:00Z"), night, time.UTC); !d.write || d.fire {
		t.Fatalf("first sight inside the window fired: %+v", d)
	}
	if d := decideWindow(tasks.Clock{}, false, at("2026-01-02T12:00:00Z"), night, time.UTC); !d.write || d.fire {
		t.Fatalf("first sight outside the window: %+v", d)
	}
	ranD1 := tasks.Clock{LastDue: at("2026-01-01T01:05:00Z")}
	d := decideWindow(ranD1, true, at("2026-01-02T01:00:00Z"), night, time.UTC)
	if !d.fire || !d.dueAt.Equal(at("2026-01-02T01:00:00Z")) || !d.next.LastDue.Equal(at("2026-01-02T01:00:00Z")) {
		t.Fatalf("next opening: %+v", d)
	}
	if d := decideWindow(d.next, true, at("2026-01-02T03:00:00Z"), night, time.UTC); d.fire || d.write {
		t.Fatalf("second poll of one opening fired: %+v", d)
	}
	if d := decideWindow(ranD1, true, at("2026-01-02T12:00:00Z"), night, time.UTC); d.write {
		t.Fatalf("outside the window: %+v", d)
	}
	if d := decideWindow(ranD1, true, at("2026-01-02T02:00:00Z"), WindowConfig{Start: 1, End: 4}, time.UTC); d.write {
		t.Fatalf("a disabled window fired: %+v", d)
	}
	// Wrapping window: a run at 23:30 is not repeated after midnight.
	ran := tasks.Clock{LastDue: at("2026-01-02T23:30:00Z")}
	if d := decideWindow(ran, true, at("2026-01-03T00:30:00Z"), wrap, time.UTC); d.fire {
		t.Fatalf("wrapping window fired twice in one opening: %+v", d)
	}
	// The cutover's import: a window that ran at 23:30 local on D is
	// imported as local midnight of D, and is not due again on D (west of
	// UTC, the Gotham zone).
	loc := GothamZone(t)
	imported := tasks.Clock{LastDue: time.Date(2026, 1, 2, 0, 0, 0, 0, loc), LastRun: time.Date(2026, 1, 2, 0, 0, 0, 0, loc)}
	if d := decideWindow(imported, true, time.Date(2026, 1, 2, 23, 45, 0, 0, loc), wrap, loc); d.fire {
		t.Fatalf("imported window clock fired again the same day: %+v", d)
	}
	if d := decideWindow(imported, true, time.Date(2026, 1, 3, 23, 5, 0, 0, loc), wrap, loc); !d.fire {
		t.Fatalf("imported window clock did not fire the next day: %+v", d)
	}
	importedNight := tasks.Clock{LastDue: time.Date(2026, 1, 2, 0, 0, 0, 0, loc)}
	if d := decideWindow(importedNight, true, time.Date(2026, 1, 2, 2, 0, 0, 0, loc), night, loc); d.fire {
		t.Fatalf("imported window clock fired at 02:00 the day it ran: %+v", d)
	}
}

func TestValidWindowHours(t *testing.T) {
	for _, w := range []WindowConfig{{Start: -1, End: 4}, {Start: 1, End: 24}, {Start: 24, End: 1}} {
		if validWindowHours(w) {
			t.Errorf("%+v accepted", w)
		}
	}
	if !validWindowHours(WindowConfig{Start: 0, End: 23}) {
		t.Error("0-23 refused")
	}
}

func TestErrorWrappers(t *testing.T) {
	base := errors.New("cave flooded")
	if e := (&clockReadError{err: base}); !errors.Is(e, base) || !strings.Contains(e.Error(), "read clock") {
		t.Fatal(e)
	}
	if e := (&startError{err: base}); !errors.Is(e, base) || !strings.Contains(e.Error(), "start") {
		t.Fatal(e)
	}
}
