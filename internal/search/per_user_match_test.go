// file: internal/search/per_user_match_test.go
// version: 1.0.0
// guid: 6f3d8b2a-1c4e-4a9f-9d2b-7e5a1f3c8b60
// last-edited: 2026-07-10

package search

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// testNow is the fixed instant the matcher tests resolve relative dates against.
var testNow = time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)

func filter(field, value string, negated bool) PerUserFilter {
	return PerUserFilter{Node: &FieldNode{Field: field, Value: value}, Negated: negated}
}

func TestMatchPerUserFilters_NilStateZeroValue(t *testing.T) {
	// nil state == zero-value UserBookState: Status == "".
	filters := []PerUserFilter{filter("read_status", "finished", false)}
	if MatchPerUserFilters(nil, filters, testNow) {
		t.Error("nil state should NOT match read_status:finished")
	}
}

func TestMatchPerUserFilters_NilStateNegatedMatches(t *testing.T) {
	// A NEGATED filter (`NOT read_status:finished`) DOES match a book
	// with no state.
	filters := []PerUserFilter{filter("read_status", "finished", true)}
	if !MatchPerUserFilters(nil, filters, testNow) {
		t.Error("nil state should match NOT read_status:finished")
	}
}

func TestMatchPerUserFilters_ReadStatusMatch(t *testing.T) {
	state := &database.UserBookState{Status: database.UserBookStatusFinished}
	filters := []PerUserFilter{filter("read_status", "finished", false)}
	if !MatchPerUserFilters(state, filters, testNow) {
		t.Error("expected finished state to match read_status:finished")
	}
}

func TestMatchPerUserFilters_ReadStatusCaseInsensitive(t *testing.T) {
	state := &database.UserBookState{Status: "Finished"}
	filters := []PerUserFilter{filter("read_status", "finished", false)}
	if !MatchPerUserFilters(state, filters, testNow) {
		t.Error("expected read_status match to be case-insensitive")
	}
}

func TestMatchPerUserFilters_Negation(t *testing.T) {
	state := &database.UserBookState{Status: database.UserBookStatusInProgress}
	filters := []PerUserFilter{filter("read_status", "finished", true)}
	if !MatchPerUserFilters(state, filters, testNow) {
		t.Error("in-progress state should match NOT read_status:finished")
	}

	finishedState := &database.UserBookState{Status: database.UserBookStatusFinished}
	if MatchPerUserFilters(finishedState, filters, testNow) {
		t.Error("finished state should NOT match NOT read_status:finished")
	}
}

func TestMatchPerUserFilters_NumericRange(t *testing.T) {
	filters := []PerUserFilter{
		{Node: &FieldNode{Field: "progress_pct", Op: "range", RangeMin: "50", RangeMax: "100"}},
	}
	if !MatchPerUserFilters(&database.UserBookState{ProgressPct: 75}, filters, testNow) {
		t.Error("progress_pct 75 should match progress_pct:50..100")
	}
	if MatchPerUserFilters(&database.UserBookState{ProgressPct: 10}, filters, testNow) {
		t.Error("progress_pct 10 should NOT match progress_pct:50..100")
	}
	// Boundaries are inclusive.
	if !MatchPerUserFilters(&database.UserBookState{ProgressPct: 50}, filters, testNow) {
		t.Error("progress_pct 50 should match progress_pct:50..100 (inclusive lower bound)")
	}
	if !MatchPerUserFilters(&database.UserBookState{ProgressPct: 100}, filters, testNow) {
		t.Error("progress_pct 100 should match progress_pct:50..100 (inclusive upper bound)")
	}
}

func TestMatchPerUserFilters_LastPlayedZeroNeverMatches(t *testing.T) {
	filters := []PerUserFilter{
		{Node: &FieldNode{Field: "last_played", Op: ">", Value: "2020-01-01"}},
	}
	// Zero LastActivityAt (nil state, or a state that never played)
	// never matches a last_played filter, negated or not being
	// evaluated at the FieldNode layer (negation applied by caller).
	if MatchPerUserFilters(nil, filters, testNow) {
		t.Error("zero LastActivityAt should never match a last_played filter")
	}
	state := &database.UserBookState{LastActivityAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !MatchPerUserFilters(state, filters, testNow) {
		t.Error("2024 last_played should match last_played:>2020-01-01")
	}
}

func TestMatchPerUserFilters_AndAcrossFilters(t *testing.T) {
	state := &database.UserBookState{
		Status:      database.UserBookStatusInProgress,
		ProgressPct: 60,
	}
	filters := []PerUserFilter{
		filter("read_status", "in_progress", false),
		{Node: &FieldNode{Field: "progress_pct", Op: "range", RangeMin: "50", RangeMax: "100"}},
	}
	if !MatchPerUserFilters(state, filters, testNow) {
		t.Error("state satisfying both filters should match (AND semantics)")
	}

	// Fail one of the two filters -> overall no match.
	failFilters := []PerUserFilter{
		filter("read_status", "finished", false),
		{Node: &FieldNode{Field: "progress_pct", Op: "range", RangeMin: "50", RangeMax: "100"}},
	}
	if MatchPerUserFilters(state, failFilters, testNow) {
		t.Error("state failing one of two AND'd filters should NOT match")
	}
}

func TestMatchPerUserFilters_EmptyFiltersAlwaysMatch(t *testing.T) {
	if !MatchPerUserFilters(nil, nil, testNow) {
		t.Error("no filters should always match")
	}
}

// ── relative dates (D76) ────────────────────────────────────────────────────

func opFilter(field, op, value string) []PerUserFilter {
	return []PerUserFilter{{Node: &FieldNode{Field: field, Op: op, Value: value}}}
}

func rangeFilter(field, lo, hi string) []PerUserFilter {
	return []PerUserFilter{{Node: &FieldNode{Field: field, Op: "range", RangeMin: lo, RangeMax: hi}}}
}

// TestMatchPerUserFilters_LastPlayedRelative is the matcher half of the
// acceptance: with a fixed now, last_played:>-30d matches a book played 29 days
// before it and not one played 31 days before it.
func TestMatchPerUserFilters_LastPlayedRelative(t *testing.T) {
	played29 := &database.UserBookState{LastActivityAt: testNow.AddDate(0, 0, -29)}
	played31 := &database.UserBookState{LastActivityAt: testNow.AddDate(0, 0, -31)}
	f := opFilter("last_played", ">", "-30d")

	if !MatchPerUserFilters(played29, f, testNow) {
		t.Error("a book played 29 days ago must match last_played:>-30d")
	}
	if MatchPerUserFilters(played31, f, testNow) {
		t.Error("a book played 31 days ago must NOT match last_played:>-30d")
	}
	// The same state against a later now: the window moved, the row did not.
	if MatchPerUserFilters(played29, f, testNow.AddDate(0, 0, 2)) {
		t.Error("two days later the 29-day-old play is 31 days old and must fall out of the window")
	}
}

func TestMatchPerUserFilters_LastPlayedRelativeRange(t *testing.T) {
	f := rangeFilter("last_played", "-30d", "now")
	in := &database.UserBookState{LastActivityAt: testNow.AddDate(0, 0, -10)}
	atNow := &database.UserBookState{LastActivityAt: testNow}
	old := &database.UserBookState{LastActivityAt: testNow.AddDate(0, 0, -40)}
	future := &database.UserBookState{LastActivityAt: testNow.Add(time.Hour)}

	for name, tc := range map[string]struct {
		state *database.UserBookState
		want  bool
	}{"inside": {in, true}, "at now (inclusive)": {atNow, true}, "too old": {old, false}, "after now": {future, false}} {
		if got := MatchPerUserFilters(tc.state, f, testNow); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestMatchPerUserFilters_LastPlayedAbsoluteStillWorks(t *testing.T) {
	played := &database.UserBookState{LastActivityAt: time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)}
	if !MatchPerUserFilters(played, opFilter("last_played", ">", "2026-09-01"), testNow) {
		t.Error("last_played:>2026-09-01 must match a 2026-09-15 play")
	}
	if MatchPerUserFilters(played, opFilter("last_played", ">", "2026-10-01"), testNow) {
		t.Error("last_played:>2026-10-01 must not match a 2026-09-15 play")
	}
	// A bare date names a day: equality matches anywhere inside it.
	if !MatchPerUserFilters(played, opFilter("last_played", "=", "2026-09-15"), testNow) {
		t.Error("last_played:=2026-09-15 must match a play at 08:00 that day")
	}
	if MatchPerUserFilters(played, opFilter("last_played", "=", "2026-09-16"), testNow) {
		t.Error("last_played:=2026-09-16 must not match a play on the 15th")
	}
}

func TestMatchPerUserFilters_LastPlayedBadTermMatchesNothing(t *testing.T) {
	played := &database.UserBookState{LastActivityAt: testNow}
	for _, v := range []string{"30d", "-30x", "garbage"} {
		if MatchPerUserFilters(played, opFilter("last_played", ">", v), testNow) {
			t.Errorf("last_played:>%s must match nothing", v)
		}
	}
}

// TestMatchPerUserFilters_UnstartedMatchesNoRowAndUnstartedRow is the matcher
// edge the Collections page depends on: read_status:unstarted must match a book
// the user has no row for AND a row that says unstarted, and not a row that says
// anything else. Before the fix the empty status of a missing row never equalled
// "unstarted", so the filter missed most of the library.
func TestMatchPerUserFilters_UnstartedMatchesNoRowAndUnstartedRow(t *testing.T) {
	f := []PerUserFilter{filter("read_status", "unstarted", false)}

	noRow := (*database.UserBookState)(nil)
	unstartedRow := &database.UserBookState{Status: database.UserBookStatusUnstarted}
	emptyStatusRow := &database.UserBookState{}
	inProgress := &database.UserBookState{Status: database.UserBookStatusInProgress}
	finished := &database.UserBookState{Status: database.UserBookStatusFinished}

	for name, tc := range map[string]struct {
		state *database.UserBookState
		want  bool
	}{
		"no row":               {noRow, true},
		"row says unstarted":   {unstartedRow, true},
		"row with empty state": {emptyStatusRow, true},
		"in_progress":          {inProgress, false},
		"finished":             {finished, false},
	} {
		if got := MatchPerUserFilters(tc.state, f, testNow); got != tc.want {
			t.Errorf("read_status:unstarted vs %s = %v, want %v", name, got, tc.want)
		}
	}

	// The negation is the mirror image.
	neg := []PerUserFilter{filter("read_status", "unstarted", true)}
	if MatchPerUserFilters(noRow, neg, testNow) || !MatchPerUserFilters(inProgress, neg, testNow) {
		t.Error("-read_status:unstarted must reject a book with no row and keep an in-progress one")
	}
	// And it must not make an empty status match other words.
	if MatchPerUserFilters(noRow, []PerUserFilter{filter("read_status", "in_progress", false)}, testNow) {
		t.Error("a book with no row must not match read_status:in_progress")
	}
}
