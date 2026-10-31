// file: internal/audiobooks/filter_per_user_values_test.go
// version: 1.1.0
// guid: 2d8f4a67-b1c3-4e90-85a6-7c0e3d9f1b52
// last-edited: 2026-10-30

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// B-1: the Library search box sends read_status, progress_pct and last_played as
// filters= pairs, not in search=. The value check a query string gets
// (search.CheckPerUserValues) runs on them too, so a value the matcher cannot
// evaluate is a 400 at the boundary. Before, -read_status:completed returned the
// whole library (the viewer's finished books included) and progress_pct:[* TO 50]
// matched nothing, both with a 200.
func TestFilterValues_PerUserValuesAreRefusedLikeAQueryString(t *testing.T) {
	for _, tc := range []struct {
		f    FieldFilter
		want string
	}{
		{FieldFilter{Field: "read_status", Value: "completed", Negated: true}, "read_status must be one of"},
		{FieldFilter{Field: "read_status", Value: "completed"}, "read_status must be one of"},
		{FieldFilter{Field: "read_status", Value: "in progress", Quoted: true}, "read_status must be one of"},
		{FieldFilter{Field: "read_status", Value: "fin*"}, "patterns"},
		{FieldFilter{Field: "read_status", Value: "(finished|in-progress)"}, "in-progress"},
		{FieldFilter{Field: "progress_pct", Value: "[* TO 50]"}, "open end"},
		{FieldFilter{Field: "progress_pct", Value: "!=100"}, "progress_pct takes a number"},
		{FieldFilter{Field: "progress_pct", Value: "==50", Negated: true}, "progress_pct takes a number"},
		{FieldFilter{Field: "progress_pct", Value: ">half"}, "progress_pct takes a number"},
		{FieldFilter{Field: "progress_pct", Value: "(>50|<10)"}, "inside ( | )"},
		{FieldFilter{Field: "read_status", Value: "()"}, "lists no values"},
		{FieldFilter{Field: "last_played", Value: "(yesterday|-3d)"}, "last_played"},
	} {
		err := FirstInvalidFilterValue([]FieldFilter{tc.f})
		require.Error(t, err, "%s:%s", tc.f.Field, tc.f.Value)
		assert.Contains(t, err.Error(), tc.want, "%s:%s", tc.f.Field, tc.f.Value)
		assert.True(t, search.IsPerUserQueryError(err), "%s:%s: %v", tc.f.Field, tc.f.Value, err)
	}
	for _, f := range []FieldFilter{
		{Field: "read_status", Value: "finished"},
		{Field: "read_status", Value: "In_Progress", Negated: true},
		{Field: "read_status", Value: "(finished|in_progress)"},
		{Field: "read_status", Value: "(finished | in_progress)", Negated: true},
		{Field: "progress_pct", Value: ">50"},
		{Field: "progress_pct", Value: "[10 TO 90]"},
		{Field: "progress_pct", Value: "(10|20)"},
		{Field: "last_played", Value: "*"},
		{Field: "last_played", Value: "(-30d|2026-01-01)"},
	} {
		assert.NoError(t, FirstInvalidFilterValue([]FieldFilter{f}), "%s:%s", f.Field, f.Value)
	}
}

// read_status:(a|b) is accepted in the box (the docs list it) and is ONE filter that
// ORs inside itself. It used to fail in the matcher ("is not a single value"),
// which logged an error and matched nothing.
func TestListFilter_ReadStatusAlternationOrsInsideOneFilter(t *testing.T) {
	summaries := []database.BookSummary{{ID: "b1"}, {ID: "b2"}, {ID: "b3"}, {ID: "b4"}}
	states := map[string]*database.UserBookState{
		"b1": {UserID: "alice", BookID: "b1", Status: database.UserBookStatusFinished},
		"b2": {UserID: "alice", BookID: "b2", Status: database.UserBookStatusInProgress},
		"b3": {UserID: "alice", BookID: "b3", Status: database.UserBookStatusAbandoned},
		"b4": nil, // never opened
	}
	run := func(f FieldFilter) []string {
		got, err := listWithSummaries(t, summaries, ListFilters{UserID: "alice", PerUserFilters: []FieldFilter{f}}, states)
		require.NoError(t, err)
		return ids(got)
	}
	assert.ElementsMatch(t, []string{"b1", "b2"}, run(FieldFilter{Field: "read_status", Value: "(finished|in_progress)"}))
	assert.ElementsMatch(t, []string{"b3", "b4"}, run(FieldFilter{Field: "read_status", Value: "(finished|in_progress)", Negated: true}))
	assert.ElementsMatch(t, []string{"b1", "b3"}, run(FieldFilter{Field: "read_status", Value: "(finished | abandoned)"}))
	assert.ElementsMatch(t, []string{"b2", "b3", "b4"}, run(FieldFilter{Field: "read_status", Value: "finished", Negated: true}))
}

// A cache keyed on the filters alone must not store a result that depends on the
// clock: that includes the clock inside an alternation.
func TestHasRelativeDateFilter_SeesAnAlternationOfRelativeDates(t *testing.T) {
	assert.True(t, HasRelativeDateFilter([]FieldFilter{{Field: "last_played", Value: "(-30d|-60d)"}}))
	assert.True(t, HasRelativeDateFilter(nil, []FieldFilter{{Field: "last_played", Value: "(2026-01-01|-3d)"}}))
	assert.False(t, HasRelativeDateFilter([]FieldFilter{{Field: "last_played", Value: "(2026-01-01|2026-02-01)"}}))
}

// NIT 8 (S-D): "every caller of the search" includes the two substring fallbacks
// that take over while the index is nil or rebuilding. They do not evaluate
// per-user terms, but a query the index path would refuse is refused there too,
// not answered as plain text. (An OR with a sibling, or a NOT over a group, is no
// longer one of them: the index path evaluates it.)
func TestSubstringFallbacksRefuseAPerUserTermTheIndexPathRefuses(t *testing.T) {
	store := mocks.NewMockStore(t) // nothing may be searched for a refused query
	svc := NewAudiobookService(store)
	for _, q := range []string{
		"read_status:completed OR title:Hush",
		"read_status:completed",
		"READ_STATUS:finished",
	} {
		got, err := svc.GetAudiobooks(context.Background(), 20, 0, q, nil, nil, ListFilters{UserID: "alice"})
		require.Error(t, err, "unscoped %q", q)
		assert.True(t, search.IsPerUserQueryError(err), "%q: %v", q, err)
		assert.Empty(t, got)

		scoped, total, err := svc.searchWithinIDs(context.Background(), q, map[string]struct{}{"b1": {}}, "alice", hydratePage)
		require.Error(t, err, "scoped %q", q)
		assert.True(t, search.IsPerUserQueryError(err), "%q: %v", q, err)
		assert.Empty(t, scoped)
		assert.Zero(t, total)
	}
}

func TestSearchPerUserProblem_JudgesOnlyWhatParsesAndNamesAField(t *testing.T) {
	assert.NoError(t, SearchPerUserProblem(""))
	assert.NoError(t, SearchPerUserProblem("The Long Halloween"))
	assert.NoError(t, SearchPerUserProblem("title:Hush read_status:finished"))
	assert.NoError(t, SearchPerUserProblem("title:(unclosed"), "a query the DSL parser rejects takes the substring fallback")
	assert.NoError(t, SearchPerUserProblem("read_status:finished OR title:Hush"), "an OR over a per-user term is evaluated")
	assert.NoError(t, SearchPerUserProblem("-(read_status:finished title:Hush)"))
	assert.Error(t, SearchPerUserProblem("read_status:completed OR title:Hush"), "its value is still checked")
}
