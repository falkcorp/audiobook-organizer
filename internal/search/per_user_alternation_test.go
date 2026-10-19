// file: internal/search/per_user_alternation_test.go
// version: 1.0.0
// guid: d92f6a18-3b75-4c0e-8a41-6e5b1c9d7f03
// last-edited: 2026-10-18

package search

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// matchPerUser translates q and runs only its per-user filters against state, the
// way a dynamic collection or a smart playlist does after Bleve returns candidates.
func matchPerUser(t *testing.T, state *database.UserBookState, q string) bool {
	t.Helper()
	ast, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("%s: parse: %v", q, err)
	}
	_, pu, err := TranslateAt(ast, dateTestNow)
	if err != nil {
		t.Fatalf("%s: translate: %v", q, err)
	}
	return MatchPerUserFilters(state, pu, dateTestNow)
}

// An alternation is ONE filter: OR inside it, AND across filters. Each
// alternative used to be its own ANDed filter, so a value list on a per-user field
// could only match a state that satisfied every value at once.
func TestPerUserAlternation_IsOrWithinTheGroup(t *testing.T) {
	finished := &database.UserBookState{Status: database.UserBookStatusFinished}
	inProgress := &database.UserBookState{Status: database.UserBookStatusInProgress}
	unstarted := &database.UserBookState{}

	for _, tc := range []struct {
		name  string
		q     string
		state *database.UserBookState
		want  bool
	}{
		{"first alternative", "read_status:(finished|in_progress)", finished, true},
		{"second alternative", "read_status:(finished|in_progress)", inProgress, true},
		{"neither", "read_status:(finished|in_progress)", unstarted, false},
		{"no row at all", "read_status:(finished|in_progress)", nil, false},
		{"unstarted is one of the alternatives for a book never opened", "read_status:(unstarted|finished)", nil, true},
		{"one alternative", "read_status:(finished)", finished, true},
		// NOT is over the whole group: none of them.
		{"negated, matches one of them", "-read_status:(finished|in_progress)", finished, false},
		{"negated, matches the other", "-read_status:(finished|in_progress)", inProgress, false},
		{"negated, matches none", "-read_status:(finished|in_progress)", unstarted, true},
		{"NOT spelling", "NOT read_status:(finished|in_progress)", unstarted, true},
		{"negated, no row", "-read_status:(finished|in_progress)", nil, true},
		// Groups are ANDed with each other.
		{"two groups, both hold", "read_status:(finished|in_progress) progress_pct:(10|20)",
			&database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 20}, true},
		{"two groups, one fails", "read_status:(finished|in_progress) progress_pct:(10|20)",
			&database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 30}, false},
		{"a group and a plain filter", "read_status:(finished|in_progress) -progress_pct:>50",
			&database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 40}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchPerUser(t, tc.state, tc.q); got != tc.want {
				t.Errorf("%s: matched=%v, want %v", tc.q, got, tc.want)
			}
		})
	}
}

func TestPerUserAlternation_LastPlayed(t *testing.T) {
	at := func(y int, m time.Month, d, h int) *database.UserBookState {
		return &database.UserBookState{LastActivityAt: time.Date(y, m, d, h, 0, 0, 0, time.UTC)}
	}
	q := "last_played:(2026-03-31|2026-01-01)"
	for name, tc := range map[string]struct {
		state *database.UserBookState
		want  bool
	}{
		"inside the first day":  {at(2026, time.March, 31, 15), true},
		"inside the second day": {at(2026, time.January, 1, 10), true},
		"between them":          {at(2026, time.February, 10, 10), false},
		"never played":          {nil, false},
	} {
		if got := matchPerUser(t, tc.state, q); got != tc.want {
			t.Errorf("%s: %s matched=%v, want %v", name, q, got, tc.want)
		}
	}
	// Negated: played on neither day.
	if got := matchPerUser(t, at(2026, time.February, 10, 10), "-"+q); !got {
		t.Error("-last_played:(a|b) must match a book played on neither day")
	}
	if got := matchPerUser(t, at(2026, time.March, 31, 15), "-"+q); got {
		t.Error("-last_played:(a|b) must not match a book played on one of the days")
	}
	// A relative alternative resolves against the evaluation instant.
	// (An alternative is a value, not a comparison: a relative one is the exact instant.)
	recent := &database.UserBookState{LastActivityAt: dateTestNow.AddDate(0, 0, -2)}
	if !matchPerUser(t, recent, "last_played:(-2d|2020-01-01)") {
		t.Error("last_played:(-2d|2020-01-01) must match a play exactly two days ago")
	}
	if matchPerUser(t, recent, "last_played:(-3d|2020-01-01)") {
		t.Error("last_played:(-3d|2020-01-01) must not match a play two days ago")
	}
}

// For one instant and one alternation the Go matcher (last_played, per-user) and
// the Bleve translation (date_added, indexed) must give the same answer: the two
// engines read their bounds from one interval, and an alternation is an OR in both.
func TestPerUserAlternation_AgreesWithBleveOnDates(t *testing.T) {
	instants := []time.Time{
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 15, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 23, 59, 59, 999_999_999, time.UTC),
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 14, 8, 0, 0, 0, time.UTC),
	}
	terms := []string{
		"(2026-03-31|2026-01-01)",
		"(2026-03-31|2026-04-01)",
		"(2026-03-31T15:00:00Z|2026-01-01)",
		"(2026-01-01|2026-03-31T23:59:59.999999999Z)",
		"(2026-03-30|2026-02-14)",
		"(2026-02-14)",
	}
	for _, at := range instants {
		idx := openTestIndex(t)
		a := at
		if err := idx.IndexBook(BookDocument{BookID: "b1", Title: "Edge", DateAdded: &a}); err != nil {
			t.Fatal(err)
		}
		state := &database.UserBookState{LastActivityAt: at}
		for _, term := range terms {
			bleve := len(dateSearch(t, idx, "date_added:"+term)) == 1
			goMatch := matchPerUser(t, state, "last_played:"+term)
			if bleve != goMatch {
				t.Errorf("%s %s: bleve date_added=%v but matcher last_played=%v", at.Format(time.RFC3339Nano), term, bleve, goMatch)
			}
			// And the negated forms agree with each other and invert the plain one.
			bleveNot := len(dateSearch(t, idx, "-date_added:"+term)) == 1
			goNot := matchPerUser(t, state, "-last_played:"+term)
			if bleveNot != goNot || goNot == goMatch {
				t.Errorf("%s -%s: bleve=%v matcher=%v (plain %v)", at.Format(time.RFC3339Nano), term, bleveNot, goNot, goMatch)
			}
		}
	}
}

// The translator hands the matcher one group per alternation, not one filter per value.
func TestTranslate_PerUserAlternationIsOneFilter(t *testing.T) {
	ast, err := ParseQuery("read_status:(finished|in_progress) -progress_pct:>50")
	if err != nil {
		t.Fatal(err)
	}
	_, pu, err := TranslateAt(ast, dateTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(pu) != 2 {
		t.Fatalf("got %d filters, want 2 (one group, one plain)", len(pu))
	}
	if len(pu[0].Alternatives) != 2 || pu[0].Node != nil || pu[0].Negated {
		t.Errorf("first filter = %+v, want a 2-way group", pu[0])
	}
	if pu[1].Node == nil || !pu[1].Negated {
		t.Errorf("second filter = %+v, want the negated progress_pct term", pu[1])
	}
}
