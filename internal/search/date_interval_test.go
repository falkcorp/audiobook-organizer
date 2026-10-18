// file: internal/search/date_interval_test.go
// version: 1.0.0
// guid: 7c3e9a52-4d18-4b6f-9e21-5a0d8f3b1c74
// last-edited: 2026-10-18

package search

import (
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The instant every interval test uses: mid-afternoon on the last day of March.
var intervalInstant = time.Date(2026, time.March, 31, 15, 0, 0, 0, time.UTC)

// intervalCases is what each operator means for that instant. The Go matcher and
// the Bleve date range both read their bounds from dateTermInterval, so one
// table pins them both.
var intervalCases = []struct {
	term string // the part after the field name
	want bool
}{
	// A bare date names the whole UTC day, so the upper bound of a range and the
	// bound of "<=" include all of that day.
	{"2026-03-31", true},
	{"2026-03-30", false},
	{"2026-04-01", false},
	{"2026-03-01..2026-03-31", true},
	{"2026-03-31..2026-03-31", true},
	{"2026-03-01..2026-03-30", false},
	{"2026-03-31..2026-04-30", true},
	{"<=2026-03-31", true},
	{"<=2026-03-30", false},
	{"<=2026-04-01", true},
	// "<d" is strictly before the day.
	{"<2026-03-31", false},
	{"<2026-04-01", true},
	// ">d" is after the whole day, ">=d" from its start.
	{">2026-03-31", false},
	{">2026-03-30", true},
	{">=2026-03-31", true},
	{">=2026-04-01", false},
	// A full instant is exact: "<=t" includes t, ">t" starts just after it.
	{"<=2026-03-31T15:00:00Z", true},
	{"<2026-03-31T15:00:00Z", false},
	{">=2026-03-31T15:00:00Z", true},
	{">2026-03-31T15:00:00Z", false},
	{"2026-03-31T14:00:00Z..2026-03-31T15:00:00Z", true},
	{"2026-03-31T15:00:01Z..2026-03-31T16:00:00Z", false},
}

func TestDateEdges_BleveDateAdded(t *testing.T) {
	idx := openTestIndex(t)
	at := intervalInstant
	if err := idx.IndexBook(BookDocument{BookID: "b1", Title: "Edge", DateAdded: &at}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range intervalCases {
		got := dateSearch(t, idx, "date_added:"+tc.term)
		if has := len(got) == 1; has != tc.want {
			t.Errorf("date_added:%s on a book added 2026-03-31T15:00Z: matched=%v, want %v", tc.term, has, tc.want)
		}
	}
}

func TestDateEdges_MatcherLastPlayed(t *testing.T) {
	state := &database.UserBookState{LastActivityAt: intervalInstant}
	for _, tc := range intervalCases {
		n, err := FieldNodeForTerm("last_played", tc.term, false)
		if err != nil {
			t.Fatalf("last_played:%s: %v", tc.term, err)
		}
		if got := MatchPerUserFilters(state, []PerUserFilter{{Node: n}}, dateTestNow); got != tc.want {
			t.Errorf("last_played:%s on a book played 2026-03-31T15:00Z: matched=%v, want %v", tc.term, got, tc.want)
		}
	}
}

// "[a TO b]" is the bracketed spelling of a..b and reads the same way.
func TestDateEdges_BracketRangeIncludesTheWholeUpperDay(t *testing.T) {
	idx := openTestIndex(t)
	at := intervalInstant
	if err := idx.IndexBook(BookDocument{BookID: "b1", Title: "Edge", DateAdded: &at}); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]bool{
		"date_added:[2026-03-01 TO 2026-03-31]": true,
		"date_added:[2026-03-01 TO 2026-03-30]": false,
	} {
		got := dateSearch(t, idx, q)
		if has := len(got) == 1; has != want {
			t.Errorf("%s: matched=%v, want %v", q, has, want)
		}
	}
}

// A relative bound resolves to an instant, not a day, so it keeps the exact
// edges: only a bare YYYY-MM-DD widens.
func TestDateEdges_RelativeBoundsAreInstants(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	n, _ := FieldNodeForTerm("last_played", "<=-1d", false)
	f := []PerUserFilter{{Node: n}}
	edge := now.AddDate(0, 0, -1)
	if !MatchPerUserFilters(&database.UserBookState{LastActivityAt: edge}, f, now) {
		t.Error("<=-1d must include the instant exactly one day ago")
	}
	if MatchPerUserFilters(&database.UserBookState{LastActivityAt: edge.Add(time.Second)}, f, now) {
		t.Error("<=-1d must not include a play one second after that instant")
	}
}

// SF1: a malformed per-user date is refused at translation, plain or negated.
// Left to the matcher it matched nothing, and under a minus it matched every book.
func TestTranslate_PerUserDateErrorsAreReported(t *testing.T) {
	for q, want := range map[string]string{
		"last_played:>30d":             "-30d",
		"-last_played:>30d":            "-30d",
		"last_played:<2w":              "-2w",
		"-last_played:<2w":             "-2w",
		"last_played:>=1m":             "-1m",
		"-last_played:>=1m":            "-1m",
		"last_played:garbage":          "invalid date",
		"-last_played:yesterday":       "invalid date",
		"last_played:-30d..soon":       "soon",
		"last_played:(2026-01-01|30d)": "-30d",
		"title:x -last_played:>30d":    "-30d",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		_, _, terr := TranslateAt(ast, dateTestNow)
		if terr == nil || !strings.Contains(terr.Error(), want) {
			t.Errorf("TranslateAt(%q) error = %v, want one containing %q", q, terr, want)
		}
	}
}

func TestTranslate_PerUserDateValidFormsStillTranslate(t *testing.T) {
	for _, q := range []string{
		"last_played:>-30d", "-last_played:>-30d", "last_played:<-2w", "last_played:>=-1m",
		"last_played:-30d..now", "last_played:2026-01-01", "last_played:*", "-last_played:*",
		"last_played:(2026-01-01|-30d)",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		if _, _, terr := TranslateAt(ast, dateTestNow); terr != nil {
			t.Errorf("TranslateAt(%q): %v", q, terr)
		}
	}
}

// NIT5: date_added:(a|b) is two date terms, found through the index, and a bad
// alternative is an error rather than a silent miss.
func TestBleveIndex_DateAlternation(t *testing.T) {
	idx := seedDateIndex(t)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"date_added:(2026-10-15|2026-10-12)", []string{"b1", "b2"}},
		{"date_added:(2026-10-15)", []string{"b1"}},
		{"date_added:(-3d|-6d)", []string{"b1", "b2"}}, // relative terms are instants; the seed rows sit exactly on them
		{"-date_added:(2026-10-15|2026-10-12)", []string{"b3", "b4", "b5"}},
	} {
		got := dateSearch(t, idx, tc.q)
		if !sameStrings(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.q, got, tc.want)
		}
	}
	ast, err := ParseQuery("date_added:(2026-10-15|garbage)")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, terr := TranslateAt(ast, dateTestNow); terr == nil {
		t.Error("an unparseable alternative must be an error, not an empty match")
	}
}

func TestHasRelativeDate_WalksAlternation(t *testing.T) {
	for q, want := range map[string]bool{
		"date_added:(-30d|-1y)":              true,
		"date_added:(2026-01-01|-30d)":       true,
		"date_added:(2026-01-01|2026-02-01)": false,
		"title:(-30d|-1y)":                   false,
		"-added:(-7d|2026-01-01)":            true,
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		if got := HasRelativeDate(ast); got != want {
			t.Errorf("HasRelativeDate(%q) = %v, want %v", q, got, want)
		}
	}
}
