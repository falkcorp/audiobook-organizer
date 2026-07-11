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

func filter(field, value string, negated bool) PerUserFilter {
	return PerUserFilter{Node: &FieldNode{Field: field, Value: value}, Negated: negated}
}

func TestMatchPerUserFilters_NilStateZeroValue(t *testing.T) {
	// nil state == zero-value UserBookState: Status == "".
	filters := []PerUserFilter{filter("read_status", "finished", false)}
	if MatchPerUserFilters(nil, filters) {
		t.Error("nil state should NOT match read_status:finished")
	}
}

func TestMatchPerUserFilters_NilStateNegatedMatches(t *testing.T) {
	// A NEGATED filter (`NOT read_status:finished`) DOES match a book
	// with no state.
	filters := []PerUserFilter{filter("read_status", "finished", true)}
	if !MatchPerUserFilters(nil, filters) {
		t.Error("nil state should match NOT read_status:finished")
	}
}

func TestMatchPerUserFilters_ReadStatusMatch(t *testing.T) {
	state := &database.UserBookState{Status: database.UserBookStatusFinished}
	filters := []PerUserFilter{filter("read_status", "finished", false)}
	if !MatchPerUserFilters(state, filters) {
		t.Error("expected finished state to match read_status:finished")
	}
}

func TestMatchPerUserFilters_ReadStatusCaseInsensitive(t *testing.T) {
	state := &database.UserBookState{Status: "Finished"}
	filters := []PerUserFilter{filter("read_status", "finished", false)}
	if !MatchPerUserFilters(state, filters) {
		t.Error("expected read_status match to be case-insensitive")
	}
}

func TestMatchPerUserFilters_Negation(t *testing.T) {
	state := &database.UserBookState{Status: database.UserBookStatusInProgress}
	filters := []PerUserFilter{filter("read_status", "finished", true)}
	if !MatchPerUserFilters(state, filters) {
		t.Error("in-progress state should match NOT read_status:finished")
	}

	finishedState := &database.UserBookState{Status: database.UserBookStatusFinished}
	if MatchPerUserFilters(finishedState, filters) {
		t.Error("finished state should NOT match NOT read_status:finished")
	}
}

func TestMatchPerUserFilters_NumericRange(t *testing.T) {
	filters := []PerUserFilter{
		{Node: &FieldNode{Field: "progress_pct", Op: "range", RangeMin: "50", RangeMax: "100"}},
	}
	if !MatchPerUserFilters(&database.UserBookState{ProgressPct: 75}, filters) {
		t.Error("progress_pct 75 should match progress_pct:50..100")
	}
	if MatchPerUserFilters(&database.UserBookState{ProgressPct: 10}, filters) {
		t.Error("progress_pct 10 should NOT match progress_pct:50..100")
	}
	// Boundaries are inclusive.
	if !MatchPerUserFilters(&database.UserBookState{ProgressPct: 50}, filters) {
		t.Error("progress_pct 50 should match progress_pct:50..100 (inclusive lower bound)")
	}
	if !MatchPerUserFilters(&database.UserBookState{ProgressPct: 100}, filters) {
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
	if MatchPerUserFilters(nil, filters) {
		t.Error("zero LastActivityAt should never match a last_played filter")
	}
	state := &database.UserBookState{LastActivityAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !MatchPerUserFilters(state, filters) {
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
	if !MatchPerUserFilters(state, filters) {
		t.Error("state satisfying both filters should match (AND semantics)")
	}

	// Fail one of the two filters -> overall no match.
	failFilters := []PerUserFilter{
		filter("read_status", "finished", false),
		{Node: &FieldNode{Field: "progress_pct", Op: "range", RangeMin: "50", RangeMax: "100"}},
	}
	if MatchPerUserFilters(state, failFilters) {
		t.Error("state failing one of two AND'd filters should NOT match")
	}
}

func TestMatchPerUserFilters_EmptyFiltersAlwaysMatch(t *testing.T) {
	if !MatchPerUserFilters(nil, nil) {
		t.Error("no filters should always match")
	}
}
