// file: core/internal/schedspec/schedspec_test.go
// version: 1.0.0
// guid: 9e41c0d2-6f3a-4b85-8c17-2a5d7e0b4f68
// last-edited: 2026-11-11

package schedspec

import (
	"encoding/binary"
	"testing"
	"time"
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
		if _, err := ParseCron(e); err != nil {
			t.Errorf("ParseCron(%q) = %v", e, err)
		}
	}
	bad := []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8",
		"*/0 * * * *", "5-1 * * * *", "MON * * * *", "* * ? * *", "* * L * *", "+5 * * * *", "* * * * 1#2", "-1 * * * *", "1- * * * *"}
	for _, e := range bad {
		if _, err := ParseCron(e); err == nil {
			t.Errorf("ParseCron(%q) accepted", e)
		}
	}
	c, _ := ParseCron("0 0 * * 7")
	if !has(c.dow, 0) || has(c.dow, 7) {
		t.Fatal("day-of-week 7 is not folded onto Sunday")
	}
	c, _ = ParseCron("50/5 * * * *")
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
		s, err := ParseCron(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := s.Next(utc(c.after), time.UTC)
		if !ok || !got.Equal(utc(c.want)) {
			t.Errorf("%q after %s = %s %v, want %s", c.expr, c.after, got, ok, c.want)
		}
	}
	never, _ := ParseCron("0 0 31 2 *")
	if got, ok := never.Next(utc("2026-01-01T00:00:00Z"), time.UTC); ok {
		t.Fatalf("0 0 31 2 * came due at %s", got)
	}
}

func TestWallInstant_DST(t *testing.T) {
	loc := gothamZone(t)
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
		if got := WallInstant(c.y, c.mo, c.d, c.h, c.m, loc); !got.Equal(utc(c.want)) {
			t.Errorf("%s: %s, want %s", c.name, got.UTC(), c.want)
		}
	}
}

func TestDailyAt_ParseAndDST(t *testing.T) {
	for _, bad := range []string{"", "3:00", "24:00", "12:60", "12-00", "ab:cd", "12:00:00", "+1:00"} {
		if _, err := ParseDailyAt(bad); err == nil {
			t.Errorf("ParseDailyAt(%q) accepted", bad)
		}
	}
	loc := gothamZone(t)
	d, err := ParseDailyAt("02:30")
	if err != nil {
		t.Fatal(err)
	}
	// Spring forward: 02:30 does not exist; it fires at 03:00 GDT, then the
	// next day at 02:30 GDT.
	got, _ := d.Next(utc("2026-03-08T06:00:00Z"), loc)
	if !got.Equal(utc("2026-03-08T07:00:00Z")) {
		t.Fatalf("spring-forward day: %s", got.UTC())
	}
	got, _ = d.Next(got.Add(time.Minute), loc)
	if !got.Equal(utc("2026-03-09T06:30:00Z")) {
		t.Fatalf("day after: %s", got.UTC())
	}
	d, _ = ParseDailyAt("01:30")
	// Fall back: 01:30 happens twice; a stamp after the first means
	// tomorrow, never the second copy an hour later.
	got, _ = d.Next(utc("2026-11-01T05:31:00Z"), loc)
	if !got.Equal(utc("2026-11-02T06:30:00Z")) {
		t.Fatalf("fall-back day after the first 01:30: %s", got.UTC())
	}
	got, _ = d.Next(utc("2026-11-01T04:00:00Z"), loc)
	if !got.Equal(utc("2026-11-01T05:30:00Z")) {
		t.Fatalf("fall-back day before: %s", got.UTC())
	}
}

func TestCronNext_StartsAtAfter(t *testing.T) {
	// The first date's scan starts at after's wall time; a time later that
	// day, and the next day, are still found.
	c, _ := ParseCron("5,50 * * * *")
	got, ok := c.Next(utc("2026-01-01T10:40:00Z"), time.UTC)
	if !ok || !got.Equal(utc("2026-01-01T10:50:00Z")) {
		t.Fatalf("got %s", got)
	}
	got, _ = c.Next(utc("2026-01-01T23:55:00Z"), time.UTC)
	if !got.Equal(utc("2026-01-02T00:05:00Z")) {
		t.Fatalf("rollover: %s", got)
	}
	// Fall back in the Gotham zone, after the second 01:10 (06:10Z): 01:50
	// resolves to its first occurrence (05:50Z), which is before after, so
	// the next match is 02:05 (07:05Z), never the second 01:50.
	loc := gothamZone(t)
	got, _ = c.Next(utc("2026-11-01T06:10:00Z"), loc)
	if !got.Equal(utc("2026-11-01T07:05:00Z")) {
		t.Fatalf("fall back: %s", got.UTC())
	}
}

func TestKeyOwnerAndSegment(t *testing.T) {
	for key, owner := range map[string]string{
		"core.backup.schedules.create":                        "core",
		"modules.books.scheduler.schedules.library_scan_full": "books",
	} {
		if got, err := KeyOwner(key); err != nil || got != owner {
			t.Errorf("KeyOwner(%q) = %q %v", key, got, err)
		}
	}
	for _, key := range []string{"core.schedules.x", "core.a.timers.b", "modules.gotham.schedules.schedules.x",
		"core.a.schedules.night-patrol", "gotham.a.schedules.b", "", "core.a.schedules.b.c"} {
		if _, err := KeyOwner(key); err == nil {
			t.Errorf("KeyOwner(%q) accepted", key)
		}
	}
	long := "a234567890123456789012345678901234567890123456789"
	for seg, ok := range map[string]bool{"nightly": true, "a_1": true, "": false, "1a": false, "A": false, "a-b": false, long[:48]: true, long: false} {
		if Segment(seg) != ok {
			t.Errorf("Segment(%q) != %v", seg, ok)
		}
	}
}

// gothamZone is the scheduler tests' GothamZone (internal/scheduler
// export_test.go), repeated here: a test file cannot import another
// package's. A fake zone with US-style DST rules and no tzdata behind
// it: GST (UTC-5) and GDT (UTC-4), spring forward at 02:00 on the second
// Sunday of March, fall back at 02:00 on the first Sunday of November. It is
// built from a TZif v2 file with no transitions and only the POSIX TZ footer,
// so every instant takes the footer's rule and the zone never depends on the
// host's zone database.
func gothamZone(t testing.TB) *time.Location {
	t.Helper()
	const footer = "GST5GDT,M3.2.0,M11.1.0"
	header := func(version byte) []byte {
		h := make([]byte, 44)
		copy(h, "TZif")
		h[4] = version
		// isutcnt, isstdcnt, leapcnt, timecnt, typecnt, charcnt
		for i, n := range []uint32{0, 0, 0, 0, 1, 4} {
			binary.BigEndian.PutUint32(h[20+4*i:], n)
		}
		return h
	}
	body := func() []byte {
		b := make([]byte, 6)
		off := int32(-5 * 3600)
		binary.BigEndian.PutUint32(b, uint32(off))
		return append(b, "GST\x00"...)
	}
	var data []byte
	data = append(data, header('2')...)
	data = append(data, body()...)
	data = append(data, header('2')...)
	data = append(data, body()...)
	data = append(data, "\n"+footer+"\n"...)
	loc, err := time.LoadLocationFromTZData("Gotham", data)
	if err != nil {
		t.Fatalf("gothamZone: %v", err)
	}
	return loc
}
