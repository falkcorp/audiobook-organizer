// file: internal/search/relative_date_test.go
// version: 1.0.0
// guid: 17be40d9-6a35-4c8e-9d02-5f31a8c7e064
// last-edited: 2026-10-18

package search

import (
	"strings"
	"testing"
	"time"
)

func utc(y int, m time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, m, d, h, mi, s, 0, time.UTC)
}

func TestParseDateTerm(t *testing.T) {
	now := utc(2026, time.October, 18, 12, 30, 0)

	cases := []struct {
		in   string
		want time.Time
	}{
		// Absolute forms and "now".
		{"2026-09-01", utc(2026, 9, 1, 0, 0, 0)},
		{"2026-09-01T10:30:00Z", utc(2026, 9, 1, 10, 30, 0)},
		{"2026-09-01T10:30:00+02:00", utc(2026, 9, 1, 8, 30, 0)},
		{"now", now},
		{"NOW", now},
		{"  now  ", now},
		// Every unit, both signs.
		{"-30d", utc(2026, 9, 18, 12, 30, 0)},
		{"+30d", utc(2026, 11, 17, 12, 30, 0)},
		{"-0d", now},
		{"-1w", utc(2026, 10, 11, 12, 30, 0)},
		{"+2w", utc(2026, 11, 1, 12, 30, 0)},
		{"-6m", utc(2026, 4, 18, 12, 30, 0)},
		{"+3m", utc(2027, 1, 18, 12, 30, 0)},
		{"-1y", utc(2025, 10, 18, 12, 30, 0)},
		{"+2y", utc(2028, 10, 18, 12, 30, 0)},
		{"-7D", utc(2026, 10, 11, 12, 30, 0)},
		// Month-end rollover follows AddDate: the day is kept and overflows
		// into the next month.
		{"-1m@2026-03-31", utc(2026, 3, 3, 12, 30, 0)},
		{"+1m@2026-01-31", utc(2026, 3, 3, 12, 30, 0)},
		{"+1m@2024-01-31", utc(2024, 3, 2, 12, 30, 0)},
		// Leap day.
		{"+1y@2024-02-29", utc(2025, 3, 1, 12, 30, 0)},
		{"-1y@2028-02-29", utc(2027, 3, 1, 12, 30, 0)},
		{"+4y@2024-02-29", utc(2028, 2, 29, 12, 30, 0)},
		{"+1d@2024-02-28", utc(2024, 2, 29, 12, 30, 0)},
		{"+2d@2024-02-28", utc(2024, 3, 1, 12, 30, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			in, base := tc.in, now
			// "<offset>@<date>" runs the offset from another "now".
			if i := strings.Index(in, "@"); i >= 0 {
				d, err := time.Parse("2006-01-02", in[i+1:])
				if err != nil {
					t.Fatalf("bad case %q: %v", tc.in, err)
				}
				base = d.Add(12*time.Hour + 30*time.Minute)
				in = in[:i]
			}
			got, ok := ParseDateTerm(in, base)
			if !ok {
				t.Fatalf("ParseDateTerm(%q) refused", in)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("ParseDateTerm(%q, %v) = %v, want %v", in, base, got, tc.want)
			}
		})
	}
}

// A non-UTC now is converted first: the offset is a UTC calendar offset.
func TestParseDateTerm_NonUTCNowIsConvertedToUTC(t *testing.T) {
	zone := time.FixedZone("x", -8*3600)
	now := time.Date(2026, 10, 18, 20, 0, 0, 0, zone) // 2026-10-19 04:00 UTC
	got, ok := ParseDateTerm("-1d", now)
	if !ok || !got.Equal(utc(2026, 10, 18, 4, 0, 0)) {
		t.Fatalf("got %v ok=%v", got, ok)
	}
	if got.Location() != time.UTC {
		t.Fatalf("result location = %v, want UTC", got.Location())
	}
}

func TestParseDateTermErr_Rejects(t *testing.T) {
	now := utc(2026, 10, 18, 0, 0, 0)
	cases := []struct {
		in       string
		wantText string // substring the message must contain
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"30d", "-30d"}, // unsigned: the message shows the signed form
		{"2w", "-2w"},   // ... for every unit
		{"1m", "-1m"},
		{"1y", "-1y"},
		{"-30x", "invalid date"},  // unknown unit
		{"-30h", "invalid date"},  // hours are not a unit
		{"-d", "invalid date"},    // no number
		{"--30d", "invalid date"}, // double sign
		{"-+30d", "invalid date"},
		{"-3.5d", "invalid date"}, // not a whole number
		{"-30", "invalid date"},   // no unit
		{"yesterday", "invalid date"},
		{"2026-13-01", "invalid date"},
		{"2026-9-1", "invalid date"},
		{"-999999999999d", "too large"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got, ok := ParseDateTerm(tc.in, now); ok {
				t.Fatalf("ParseDateTerm(%q) = %v, want refusal", tc.in, got)
			}
			_, err := ParseDateTermErr(tc.in, now)
			if err == nil {
				t.Fatalf("ParseDateTermErr(%q) returned no error", tc.in)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("error %q does not contain %q", err, tc.wantText)
			}
		})
	}
}

func TestParseDateTerm_DoesNotReadTheClock(t *testing.T) {
	a, _ := ParseDateTerm("-1d", utc(2026, 1, 10, 0, 0, 0))
	b, _ := ParseDateTerm("-1d", utc(2026, 1, 10, 0, 0, 0))
	if !a.Equal(b) {
		t.Fatalf("same now gave different instants: %v vs %v", a, b)
	}
}

func TestIsDateField(t *testing.T) {
	for _, f := range []string{"last_played", "date_added", "added", "date_modified", "modified"} {
		if !IsDateField(f) {
			t.Errorf("%s should be a date field", f)
		}
	}
	for _, f := range []string{"itunes_last_played", "title", "year", "duration", ""} {
		if IsDateField(f) {
			t.Errorf("%s should not be a date field", f)
		}
	}
}

func TestHasRelativeDate(t *testing.T) {
	cases := map[string]bool{
		"last_played:>-30d":                 true,
		"date_added:>=-1y":                  true,
		"added:<+2w":                        true,
		"last_played:-30d..now":             true,
		"date_modified:2026-01-01..now":     true,
		"date_added:now":                    true,
		"title:x -date_added:>-7d":          true,
		"(title:x OR modified:>-1m)":        true,
		"date_added:>2026-01-01":            false,
		"date_added:2026-01-01..2026-02-01": false,
		"last_played:>2026-09-01T10:00:00Z": false,
		"title:-30d":                        false, // not a date field
		"date_added:garbage":                false,
		"title:foo":                         false,
		"foo bar":                           false,
	}
	for q, want := range cases {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		if got := HasRelativeDate(ast); got != want {
			t.Errorf("HasRelativeDate(%q) = %v, want %v", q, got, want)
		}
	}
	if HasRelativeDate(nil) {
		t.Error("nil query has no relative date")
	}
}
