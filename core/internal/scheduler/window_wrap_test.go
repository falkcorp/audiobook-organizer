// file: core/internal/scheduler/window_wrap_test.go
// version: 1.0.0
// guid: 804363d4-fcc3-4319-b89b-3a9011d6843a
// last-edited: 2026-11-11

package scheduler

import (
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// sweepWindow drives decideWindow the way pollWindow does (one stored clock,
// a poll every step from first to last, the first poll seeding) and returns
// the opening instant each fire was for.
func sweepWindow(t *testing.T, w WindowConfig, loc *time.Location, first, last time.Time, step time.Duration) []time.Time {
	t.Helper()
	var fires []time.Time
	var clock tasks.Clock
	var had bool
	for now := first; !now.After(last); now = now.Add(step) {
		d := decideWindow(clock, had, now, w, loc)
		if !d.write {
			continue
		}
		clock, had = d.next, true
		if d.fire {
			fires = append(fires, d.dueAt)
		}
	}
	return fires
}

// TestDecideWindow_WrapWindowFiresEveryOpening is the review's blocker: a
// window whose hours wrap midnight (23-4) fires once per opening and never
// skips one, however the process is seeded (a restart inside an opening, after
// midnight, or at noon).
func TestDecideWindow_WrapWindowFiresEveryOpening(t *testing.T) {
	wrap := WindowConfig{Start: 23, End: 4, Enabled: true}
	opening := func(d int) time.Time { return day(d, 23, 0) }
	cases := []struct {
		name string
		seed time.Time
		want []time.Time // openings that fire, inside 01-02 00:00 .. 01-06 12:00
	}{
		// Seeded at noon of day 2: the 23:00 opening of day 2 is the first.
		{"seeded at noon", day(2, 12, 0), []time.Time{opening(2), opening(3), opening(4), opening(5)}},
		// Seeded at 00:30 inside the opening that began at 23:00 on day 1: that
		// opening does not run (no boot stampede), the next night's must.
		{"restart after midnight inside an opening", day(2, 0, 30), []time.Time{opening(2), opening(3), opening(4), opening(5)}},
		// Seeded at 23:30 inside day 2's opening: it does not run, day 3's does.
		{"restart before midnight inside an opening", day(2, 23, 30), []time.Time{opening(3), opening(4), opening(5)}},
		// Seeded at 22:59, a minute before an opening: it runs.
		{"seeded just before an opening", day(2, 22, 59), []time.Time{opening(2), opening(3), opening(4), opening(5)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sweepWindow(t, wrap, time.UTC, c.seed, day(6, 12, 0), 5*time.Minute)
			if !slices.EqualFunc(got, c.want, time.Time.Equal) {
				t.Fatalf("fires for openings %v, want %v", got, c.want)
			}
		})
	}

	// The review's first scenario at the clock level: the 23:30 fire stamps
	// the opening's start, so a restart at 00:30 (a first sight) and the next
	// night both behave.
	fired := decideWindow(tasks.Clock{LastDue: time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC)}, true, day(1, 23, 30), wrap, time.UTC)
	if !fired.fire || !fired.next.LastDue.Equal(opening(1)) {
		t.Fatalf("a fire must stamp the opening's start, not the poll: %+v", fired)
	}
	if d := decideWindow(fired.next, true, day(2, 0, 30), wrap, time.UTC); d.fire {
		t.Fatalf("fired again after midnight inside one opening: %+v", d)
	}
	if d := decideWindow(fired.next, true, day(2, 23, 5), wrap, time.UTC); !d.fire || !d.dueAt.Equal(opening(2)) {
		t.Fatalf("the next night's opening did not fire: %+v", d)
	}
	// A first sight stamps the start of the latest opening at or before now.
	if d := decideWindow(tasks.Clock{}, false, day(2, 0, 30), wrap, time.UTC); !d.next.LastDue.Equal(opening(1)) {
		t.Fatalf("seed at 00:30 stamped %s, want the opening that began 01-01 23:00", d.next.LastDue)
	}
	if d := decideWindow(tasks.Clock{}, false, day(2, 12, 0), wrap, time.UTC); !d.next.LastDue.Equal(opening(1)) {
		t.Fatalf("seed at noon stamped %s, want 01-01 23:00", d.next.LastDue)
	}
}

// TestDecideWindow_PlainWindowEveryOpening: the non-wrapping window keeps its
// once-a-night rule, seeded or restarted at any hour.
func TestDecideWindow_PlainWindowEveryOpening(t *testing.T) {
	night := WindowConfig{Start: 1, End: 4, Enabled: true}
	opening := func(d int) time.Time { return day(d, 1, 0) }
	for name, seed := range map[string]time.Time{
		"noon":            day(2, 12, 0),
		"inside, 02:00":   day(3, 2, 0),
		"just before":     day(3, 0, 59),
		"at the boundary": day(3, 1, 0),
	} {
		got := sweepWindow(t, night, time.UTC, seed, day(6, 12, 0), 5*time.Minute)
		var want []time.Time
		for d := 2; d <= 6; d++ {
			if opening(d).After(seed) {
				want = append(want, opening(d))
			}
		}
		// Seeded inside an opening (the boundary included): that opening does
		// not run.
		if seed.Equal(day(3, 1, 0)) || seed.Equal(day(3, 2, 0)) {
			want = slices.DeleteFunc(want, func(o time.Time) bool { return o.Equal(opening(3)) })
		}
		if !slices.EqualFunc(got, want, time.Time.Equal) {
			t.Errorf("%s: fires for %v, want %v", name, got, want)
		}
	}
}

// TestDecideWindow_WrapWindowAcrossDST: a wrapping window fires once per
// opening across the spring-forward and fall-back nights of the Gotham zone
// (UTC-5, UTC-4 from 2026-03-08 02:00 to 2026-11-01 02:00).
func TestDecideWindow_WrapWindowAcrossDST(t *testing.T) {
	loc := GothamZone(t)
	wrap := WindowConfig{Start: 23, End: 4, Enabled: true}
	local := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }

	// Spring forward: the night of 03-07/03-08 is 22 hours of wall clock
	// between 23:00 and 04:00 minus the skipped hour; 03-08 23:00 is EDT.
	got := sweepWindow(t, wrap, loc, local(2026, 3, 6, 12, 0), local(2026, 3, 10, 12, 0), 7*time.Minute)
	want := []time.Time{local(2026, 3, 6, 23, 0), local(2026, 3, 7, 23, 0), local(2026, 3, 8, 23, 0), local(2026, 3, 9, 23, 0)}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("spring-forward week: openings %v, want %v", got, want)
	}

	// Fall back: the night of 10-31/11-01 repeats 01:00-02:00; the opening is
	// still one opening, and the one before and after fire.
	got = sweepWindow(t, wrap, loc, local(2026, 10, 30, 12, 0), local(2026, 11, 3, 12, 0), 7*time.Minute)
	want = []time.Time{local(2026, 10, 30, 23, 0), local(2026, 10, 31, 23, 0), local(2026, 11, 1, 23, 0), local(2026, 11, 2, 23, 0)}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("fall-back week: openings %v, want %v", got, want)
	}

	// A restart inside the repeated hour of the fall-back night (the second
	// 01:30) is a first sight inside the 10-31 23:00 opening: it does not run,
	// and the next night does.
	secondOneThirty := local(2026, 11, 1, 1, 30).Add(time.Hour)
	if h, m := secondOneThirty.In(loc).Hour(), secondOneThirty.In(loc).Minute(); h != 1 || m != 30 {
		t.Fatalf("test setup: %s is not the second 01:30", secondOneThirty.In(loc))
	}
	got = sweepWindow(t, wrap, loc, secondOneThirty, local(2026, 11, 3, 12, 0), 7*time.Minute)
	want = []time.Time{local(2026, 11, 1, 23, 0), local(2026, 11, 2, 23, 0)}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("restart in the repeated hour: openings %v, want %v", got, want)
	}

	// A window that starts in the skipped hour opens at the transition: 2-4
	// on 03-08 opens at 03:00 EDT, once, and 03-09 opens at 02:00 EDT; 2-3
	// has no local hour that day.
	skipping := WindowConfig{Start: 2, End: 4, Enabled: true}
	got = sweepWindow(t, skipping, loc, local(2026, 3, 7, 12, 0), local(2026, 3, 9, 12, 0), 5*time.Minute)
	transition := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC) // 03:00 EDT
	nextDay := time.Date(2026, 3, 9, 6, 0, 0, 0, time.UTC)    // 02:00 EDT
	if want := []time.Time{transition, nextDay}; !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("2-4 across the gap: %v, want %v", got, want)
	}
	gapOnly := WindowConfig{Start: 2, End: 3, Enabled: true}
	got = sweepWindow(t, gapOnly, loc, local(2026, 3, 7, 12, 0), local(2026, 3, 9, 12, 0), 5*time.Minute)
	if want := []time.Time{nextDay}; !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("2-3 across the gap: %v, want only the 03-09 opening (03-08 has no 02:xx)", got)
	}
}

// TestPollWindow_WrapWindowSurvivesRestart: the blocker end to end through the
// scheduler and the executor's store. The window fires at 23:30, the process
// restarts at 00:30 (no second fire), and the next night's opening fires.
func TestPollWindow_WrapWindowSurvivesRestart(t *testing.T) {
	f := newFixture(t, nil, fixtureOpts{
		windows: map[string]WindowConfig{"nightly": {Start: 23, End: 4, Enabled: true}},
	})
	f.boot(day(1, 12, 0))
	f.poll(day(1, 12, 0)) // seeds the last opening (12-31 23:00)
	f.rec.take()
	f.poll(day(1, 23, 30))
	f.expect(
		"01-01T23:30 advance core.window.nightly last_due=01-01T23:00",
		"01-01T23:30 window nightly []",
		"01-01T23:30 enqueue core.window.nightly window",
	)
	f.finish(tasks.WindowNightly, tasks.StateSucceeded)
	f.boot(day(2, 0, 30)) // a restart inside the same opening
	f.poll(day(2, 0, 30))
	f.expect()
	f.poll(day(2, 3, 0))
	f.expect()
	f.poll(day(2, 23, 0)) // the next night opens
	f.expect(
		"01-02T23:00 advance core.window.nightly last_due=01-02T23:00",
		"01-02T23:00 window nightly []",
		"01-02T23:00 enqueue core.window.nightly window",
	)
	if rs := runsOf(t, f.st, tasks.WindowNightly); len(rs) != 2 {
		t.Fatalf("%d window runs over two nights, want 2", len(rs))
	}
}
