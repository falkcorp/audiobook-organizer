// file: internal/scheduler/cron_test.go
// version: 1.1.1
// guid: 0e5d8b73-41a9-4f2c-8d16-a93c7b5e2f40
// last-edited: 2026-10-20

package scheduler

import (
	"math/rand"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zoneinfo %s unavailable: %v", name, err)
	}
	return loc
}

func TestParseCron_Accepts(t *testing.T) {
	for _, e := range []string{
		"* * * * *", "0 2 * * 0", "30 2 * * *", "15 2 * * *", "*/15 * * * *",
		"0 0 1,15 * *", "0 9-17/2 * * 1-5", "5 4 * * 7", "0 0 29 2 *",
		"59 23 31 12 6", "0,30 * * * *", "10-20 * * * *", "0 */6 * * *",
		"0 3 * * 1,3,5", "1 1 1 1 *", "0 0 * 1-6 *", "*/5 */2 * * *",
		"0 12 15 * 1", "  0   2 * * 0  ", "7/10 * * * *", "0 0 * * 0-6",
	} {
		if _, err := ParseCron(e); err != nil {
			t.Errorf("ParseCron(%q): %v", e, err)
		}
	}
}

func TestParseCron_Rejects(t *testing.T) {
	for _, e := range []string{
		"", "* * * *", "* * * * * *", "0 0 * JAN *", "0 0 * * MON", "? * * * *",
		"60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 13 *",
		"* * * * 8", "*/0 * * * *", "@daily", "0 0 L * *", "0 0 * * 1#2",
		"5-1 * * * *", "+5 * * * *", "*/+5 * * * *", "1-+5 * * * *", "0 0 15W * *", "1/0 * * * *", "-1 * * * *", "a * * * *",
	} {
		if _, err := ParseCron(e); err == nil {
			t.Errorf("ParseCron(%q) = nil error, want rejection", e)
		}
	}
}

func TestNext_Table(t *testing.T) {
	utc := time.UTC
	at := func(y int, mo time.Month, d, h, m int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, utc) }
	cases := []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		{"* * * * *", at(2026, 1, 1, 0, 0), at(2026, 1, 1, 0, 1)},
		{"* * * * *", at(2026, 12, 31, 23, 59), at(2027, 1, 1, 0, 0)},
		{"30 2 * * *", at(2026, 3, 1, 2, 29), at(2026, 3, 1, 2, 30)},
		{"30 2 * * *", at(2026, 3, 1, 2, 30), at(2026, 3, 2, 2, 30)},
		{"15 2 * * *", at(2026, 3, 1, 3, 0), at(2026, 3, 2, 2, 15)},
		{"0 0 31 * *", at(2026, 4, 1, 0, 0), at(2026, 5, 31, 0, 0)},
		{"0 0 31 * *", at(2026, 1, 31, 0, 0), at(2026, 3, 31, 0, 0)},
		{"0 0 30 * *", at(2026, 1, 30, 0, 0), at(2026, 3, 30, 0, 0)},
		{"0 0 29 2 *", at(2026, 1, 1, 0, 0), at(2028, 2, 29, 0, 0)},
		{"0 0 29 2 *", at(2028, 2, 29, 0, 0), at(2032, 2, 29, 0, 0)},
		{"0 0 1 3 *", at(2028, 2, 29, 12, 0), at(2028, 3, 1, 0, 0)},
		{"0 2 * * 0", at(2026, 10, 17, 0, 0), at(2026, 10, 18, 2, 0)}, // 2026-10-17 is a Saturday
		{"0 2 * * 0", at(2026, 10, 18, 2, 0), at(2026, 10, 25, 2, 0)},
		{"0 2 * * 7", at(2026, 10, 17, 0, 0), at(2026, 10, 18, 2, 0)},
		{"0 9-17/2 * * 1-5", at(2026, 10, 16, 17, 0), at(2026, 10, 19, 9, 0)}, // Fri 17:00 -> Mon 09:00
		{"0 9-17/2 * * 1-5", at(2026, 10, 19, 9, 0), at(2026, 10, 19, 11, 0)},
		{"*/15 * * * *", at(2026, 6, 1, 10, 46), at(2026, 6, 1, 11, 0)},
		{"*/15 * * * *", at(2026, 6, 1, 10, 15), at(2026, 6, 1, 10, 30)},
		{"0 0 1,15 * *", at(2026, 2, 15, 0, 0), at(2026, 3, 1, 0, 0)},
		{"0 0 1,15 * *", at(2026, 2, 2, 0, 0), at(2026, 2, 15, 0, 0)},
		{"0 0 1 1 *", at(2026, 1, 1, 0, 0), at(2027, 1, 1, 0, 0)},
		// day-of-month and day-of-week both restricted: OR.
		{"0 12 15 * 1", at(2026, 10, 1, 0, 0), at(2026, 10, 5, 12, 0)},    // first Monday
		{"0 12 15 * 1", at(2026, 10, 12, 12, 0), at(2026, 10, 15, 12, 0)}, // the 15th (Thu) before next Monday the 19th
		{"0 12 15 * 1", at(2026, 10, 15, 12, 0), at(2026, 10, 19, 12, 0)},
		// day-of-month restricted, day-of-week "*": AND with a full set.
		{"0 12 15 * *", at(2026, 10, 1, 0, 0), at(2026, 10, 15, 12, 0)},
		// "*/2" counts as a star: the other field alone decides.
		{"0 12 */2 * 1", at(2026, 10, 1, 0, 0), at(2026, 10, 5, 12, 0)},
		{"0 0 * 2 *", at(2026, 2, 28, 0, 0), at(2027, 2, 1, 0, 0)},
		{"59 23 31 12 *", at(2026, 1, 1, 0, 0), at(2026, 12, 31, 23, 59)},
		{"0 0 * * 6", at(2026, 12, 31, 0, 0), at(2027, 1, 2, 0, 0)},
		{"5 4 * * *", at(2099, 12, 31, 5, 0), at(2100, 1, 1, 4, 5)},
		{"0 0 29 2 *", at(2100, 1, 1, 0, 0), at(2104, 2, 29, 0, 0)}, // 2100 is not a leap year
		{"0,30 * * * *", at(2026, 5, 5, 5, 30), at(2026, 5, 5, 6, 0)},
		{"10-12 3 * * *", at(2026, 5, 5, 3, 11), at(2026, 5, 5, 3, 12)},
		{"0 0 * 1-6 *", at(2026, 6, 30, 0, 0), at(2027, 1, 1, 0, 0)},
		{"0 */6 * * *", at(2026, 5, 5, 18, 0), at(2026, 5, 6, 0, 0)},
		{"0 3 * * 1,3,5", at(2026, 10, 14, 3, 0), at(2026, 10, 16, 3, 0)}, // Wed -> Fri
	}
	for _, c := range cases {
		spec, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		got, ok := spec.Next(c.after, utc)
		if !ok || !got.Equal(c.want) {
			t.Errorf("Next(%q, %s) = %s,%v want %s", c.expr, c.after.Format(time.RFC3339), got.Format(time.RFC3339), ok, c.want.Format(time.RFC3339))
		}
	}
}

func TestNext_DST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08 02:00 EST jumps to 03:00 EDT. 02:30 does not exist.
	spec, _ := ParseCron("30 2 * * *")
	got, ok := spec.Next(time.Date(2026, 3, 7, 12, 0, 0, 0, ny), ny)
	if !ok {
		t.Fatal("no fire")
	}
	want := time.Date(2026, 3, 8, 3, 0, 0, 0, ny)
	if !got.Equal(want) {
		t.Errorf("gap fire = %s, want first valid time %s", got, want)
	}
	// The next fire is the following day's 02:30, not a second fire in the gap.
	got2, _ := spec.Next(got, ny)
	if want2 := time.Date(2026, 3, 9, 2, 30, 0, 0, ny); !got2.Equal(want2) {
		t.Errorf("after gap fire = %s, want %s", got2, want2)
	}
	// 2026-11-01 02:00 EDT falls back to 01:00 EST. 01:30 occurs twice.
	spec, _ = ParseCron("30 1 * * *")
	first, _ := spec.Next(time.Date(2026, 10, 31, 12, 0, 0, 0, ny), ny)
	if first.UTC() != time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) { // 01:30 EDT
		t.Errorf("fall-back fire = %s, want the first occurrence (05:30Z)", first.UTC())
	}
	next, _ := spec.Next(first, ny)
	if want := time.Date(2026, 11, 2, 1, 30, 0, 0, ny); !next.Equal(want) {
		t.Errorf("after fall-back fire = %s, want %s (it must fire once)", next, want)
	}
	// An every-15-minutes expression crossing the gap fires once at 03:00.
	spec, _ = ParseCron("*/15 * * * *")
	n, _ := spec.Next(time.Date(2026, 3, 8, 1, 45, 0, 0, ny).Add(-time.Second), ny)
	fires := 0
	for ; n.Before(time.Date(2026, 3, 8, 3, 1, 0, 0, ny)); fires++ {
		n, _ = spec.Next(n, ny)
	}
	if fires != 2 { // 01:45 and 03:00
		t.Errorf("fires across the gap = %d, want 2 (01:45, 03:00)", fires)
	}
}

func TestPrevAgreesWithNext(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	loc := mustLoc(t, "America/New_York")
	exprs := []string{"*/15 * * * *", "0 2 * * 0", "30 2 * * *", "0 0 1,15 * *", "0 9-17/2 * * 1-5", "0 12 15 * 1"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 1000 {
		spec, _ := ParseCron(exprs[i%len(exprs)])
		at := base.Add(time.Duration(rng.Int63n(int64(2 * 365 * 24 * time.Hour))))
		p, ok := spec.Prev(at, loc)
		if !ok {
			t.Fatalf("Prev(%s) found nothing", at)
		}
		if p.After(at) {
			t.Fatalf("Prev(%s) = %s is after the argument", at, p)
		}
		n, ok := spec.Next(p, loc)
		if !ok || n.Before(at) && !n.Equal(at) {
			t.Fatalf("Next(Prev(%s)) = %s, want at or after the argument", at, n)
		}
	}
}

func TestNext_NoMatchWithinFiveYears(t *testing.T) {
	spec, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spec.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC); ok {
		t.Errorf("Next found %s for Feb 30", got)
	}
	if got, ok := spec.Prev(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC); ok {
		t.Errorf("Prev found %s for Feb 30", got)
	}
}
