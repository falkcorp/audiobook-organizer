// file: core/internal/scheduler/math_test.go
// version: 1.0.1
// guid: d0feac80-13ca-4ad9-8d21-62931949112f
// last-edited: 2026-11-11

package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/schedspec"
	"github.com/falkcorp/mediacore/tasks"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
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
	})
	t.Run("first sight while disabled writes nothing", func(t *testing.T) {
		// HEAD never seeds a closed kind (pollV3Entry returns before the claim
		// while the gate is closed): the first poll that finds it enabled
		// seeds it, and it runs one period after that, not on that poll.
		d := decide(tasks.Clock{}, false, in(t0, effective{}, tasks.Coalesce))
		if d.write || d.fire {
			t.Fatalf("disabled first sight: %+v", d)
		}
		enabled := t0.Add(6 * time.Hour)
		d = decide(tasks.Clock{}, false, in(enabled, every(time.Hour), tasks.Coalesce))
		if !d.write || d.fire || !d.next.LastDue.Equal(enabled) {
			t.Fatalf("the first enabled poll must seed and wait: %+v", d)
		}
		if d := decide(d.next, true, in(enabled.Add(59*time.Minute), every(time.Hour), tasks.Coalesce)); d.fire {
			t.Fatalf("fired before one period after enable: %+v", d)
		}
		if d := decide(d.next, true, in(enabled.Add(time.Hour), every(time.Hour), tasks.Coalesce)); !d.fire {
			t.Fatalf("did not fire one period after enable: %+v", d)
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
	t.Run("skip missed measures freshness from the previous poll", func(t *testing.T) {
		// A poll 90 minutes after the previous one (the process was up, the
		// poll was delayed) is not downtime: the instant that came due
		// between the two polls fires even though it is older than one poll.
		now := t0.Add(3*time.Hour + 30*time.Minute)
		late := in(now, every(time.Hour), tasks.SkipMissed)
		late.since = now.Add(-90 * time.Minute)
		if d := decide(prev, true, late); !d.fire || !d.dueAt.Equal(t0.Add(3*time.Hour)) {
			t.Fatalf("a delayed poll lost its fire: %+v", d)
		}
		// A boot after the instant (since is the boot time) is downtime.
		down := in(now, every(time.Hour), tasks.SkipMissed)
		down.since = t0.Add(3*time.Hour + 10*time.Minute)
		if d := decide(prev, true, down); d.fire || d.missed != 3 {
			t.Fatalf("an instant that passed before the boot fired: %+v", d)
		}
		// An instant at the previous poll itself came due while up.
		edge := in(now, every(time.Hour), tasks.SkipMissed)
		edge.since = t0.Add(3 * time.Hour)
		if d := decide(prev, true, edge); !d.fire {
			t.Fatalf("an instant at the previous poll is up time: %+v", d)
		}
	})
	t.Run("re-open edge is the due time", func(t *testing.T) {
		closed := tasks.Clock{LastDue: t0, GateClosedAt: t0.Add(30 * time.Minute)}
		// Coalesce fires once, due at the edge, whatever passed while off.
		now := t0.Add(10*time.Hour + 20*time.Minute)
		d := decide(closed, true, in(now, every(time.Hour), tasks.Coalesce))
		if !d.fire || !d.dueAt.Equal(now) || d.missed != 0 {
			t.Fatalf("coalesce: %+v", d)
		}
		// SkipMissed applies its policy: a stale instant does not fire, a
		// fresh one does (due at that instant).
		if d := decide(closed, true, in(now, every(time.Hour), tasks.SkipMissed)); d.fire || d.missed != 0 {
			t.Fatalf("skip, stale: %+v", d)
		}
		fresh := t0.Add(10*time.Hour + 30*time.Second)
		if d := decide(closed, true, in(fresh, every(time.Hour), tasks.SkipMissed)); !d.fire || !d.dueAt.Equal(t0.Add(10*time.Hour)) || d.missed != 0 {
			t.Fatalf("skip, fresh: %+v", d)
		}
	})
	t.Run("a changed schedule counts nothing missed", func(t *testing.T) {
		old := tasks.Clock{LastDue: t0, ScheduleHash: "old"}
		now := t0.Add(5 * time.Hour)
		d := decide(old, true, in(now, every(time.Hour), tasks.Coalesce))
		if !d.fire || d.missed != 0 || d.next.Missed != 0 {
			t.Fatalf("%+v", d)
		}
		same := tasks.Clock{LastDue: t0, ScheduleHash: "h"}
		if d := decide(same, true, in(now, every(time.Hour), tasks.Coalesce)); d.missed != 4 {
			t.Fatalf("an unchanged schedule counts missed: %+v", d)
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
		never, _ := schedspec.ParseCron("0 0 31 2 *")
		if d := decide(prev, true, in(t0.Add(1000*time.Hour), effective{enabled: true, t: never, hash: "h"}, tasks.Coalesce)); d.write || d.fire {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("count is capped", func(t *testing.T) {
		n, _ := countDue(interval(time.Minute), t0, t0.Add(10000*time.Minute), time.UTC)
		if n != maxCount {
			t.Fatalf("interval count %d, want %d", n, maxCount)
		}
		c, _ := schedspec.ParseCron("* * * * *")
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
