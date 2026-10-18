// file: internal/audiobooks/filter_date_test.go
// version: 1.0.0
// guid: 194b01b9-3cfa-4f88-a4f6-be4b4427cc9b
// last-edited: 2026-10-18

package audiobooks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// perUserMatches runs one per-user FieldFilter through the matcher the Library
// list uses (newPerUserMatcher), against a state.
func perUserMatches(t *testing.T, st *database.UserBookState, f FieldFilter) bool {
	t.Helper()
	m, err := newPerUserMatcher([]FieldFilter{f})
	require.NoError(t, err)
	return m.matches(st)
}

func daysAgo(n int) *time.Time {
	t := time.Now().UTC().AddDate(0, 0, -n)
	return &t
}

func ids(books []database.Book) []string {
	out := make([]string, 0, len(books))
	for _, b := range books {
		out = append(out, b.ID)
	}
	return out
}

// listWithSummaries runs the Library list over synthetic summaries.
func listWithSummaries(t *testing.T, summaries []database.BookSummary, f ListFilters, states map[string]*database.UserBookState) ([]database.Book, error) {
	t.Helper()
	store := mocks.NewMockStore(t)
	store.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil).Maybe()
	for id, st := range states {
		store.EXPECT().GetUserBookState("alice", id).Return(st, nil).Maybe()
	}
	return NewAudiobookService(store).GetAudiobooks(context.Background(), 0, 0, "", nil, nil, f)
}

// The Library page sends date_added:>-1y as a filters JSON field/value pair, so
// this is the path the search help's own example takes.
func TestListFilter_DateAddedRelative(t *testing.T) {
	summaries := []database.BookSummary{
		{ID: "b1", CreatedAt: daysAgo(10)},
		{ID: "b2", CreatedAt: daysAgo(400)},
		{ID: "b3"}, // never stamped
	}
	for _, tc := range []struct {
		name string
		f    FieldFilter
		want []string
	}{
		{"within the last year", FieldFilter{Field: "date_added", Value: ">-1y"}, []string{"b1"}},
		{"the alias", FieldFilter{Field: "added", Value: ">-1y"}, []string{"b1"}},
		{"older than 30 days", FieldFilter{Field: "date_added", Value: "<-30d"}, []string{"b2"}},
		{"a range ending now", FieldFilter{Field: "date_added", Value: "-1y..now"}, []string{"b1"}},
		{"negated keeps an unstamped book", FieldFilter{Field: "date_added", Value: ">-1y", Negated: true}, []string{"b2", "b3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listWithSummaries(t, summaries, ListFilters{FieldFilters: []FieldFilter{tc.f}}, nil)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, ids(got))
		})
	}
}

func TestListFilter_DateModifiedAndAbsoluteRange(t *testing.T) {
	jan := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	may := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	summaries := []database.BookSummary{
		{ID: "b1", CreatedAt: &jan, UpdatedAt: &may},
		{ID: "b2", CreatedAt: &may, UpdatedAt: &jan},
	}
	got, err := listWithSummaries(t, summaries, ListFilters{FieldFilters: []FieldFilter{
		{Field: "date_added", Value: "2026-01-01..2026-03-31"},
	}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1"}, ids(got), "date_added reads CreatedAt")

	got, err = listWithSummaries(t, summaries, ListFilters{FieldFilters: []FieldFilter{
		{Field: "modified", Value: "2026-01-01..2026-03-31"},
	}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"b2"}, ids(got), "modified reads UpdatedAt")

	// A bare day is that whole UTC day.
	got, err = listWithSummaries(t, summaries, ListFilters{FieldFilters: []FieldFilter{
		{Field: "date_added", Value: "2026-01-15"},
	}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1"}, ids(got))
}

func TestListFilter_LastPlayedIsADateNotPresence(t *testing.T) {
	summaries := []database.BookSummary{{ID: "b1"}, {ID: "b2"}, {ID: "b3"}}
	states := map[string]*database.UserBookState{
		"b1": {UserID: "alice", BookID: "b1", LastActivityAt: *daysAgo(5)},
		"b2": {UserID: "alice", BookID: "b2", LastActivityAt: *daysAgo(200)},
		"b3": nil, // never played
	}
	run := func(f FieldFilter) []string {
		got, err := listWithSummaries(t, summaries, ListFilters{UserID: "alice", PerUserFilters: []FieldFilter{f}}, states)
		require.NoError(t, err)
		return ids(got)
	}
	// Until now any value meant "has been played", so this returned b1 AND b2.
	assert.Equal(t, []string{"b1"}, run(FieldFilter{Field: "last_played", Value: ">-30d"}))
	assert.Equal(t, []string{"b1"}, run(FieldFilter{Field: "last_played", Value: "-30d..now"}))
	assert.ElementsMatch(t, []string{"b2", "b3"}, run(FieldFilter{Field: "last_played", Value: ">-30d", Negated: true}))
	// -last_played:>-6m: not played in six months, which includes never played.
	assert.ElementsMatch(t, []string{"b2", "b3"}, run(FieldFilter{Field: "last_played", Value: ">-6m", Negated: true}))
	// last_played:* is "has been played at all".
	assert.ElementsMatch(t, []string{"b1", "b2"}, run(FieldFilter{Field: "last_played", Value: "*"}))
	assert.Equal(t, []string{"b3"}, run(FieldFilter{Field: "last_played", Value: "*", Negated: true}))
}

// read_status:unstarted must find a book the user has no row for - most of a
// library. The list path carried its own copy of the matcher with the same bug
// the search matcher had.
func TestListFilter_UnstartedMatchesABookWithNoRow(t *testing.T) {
	summaries := []database.BookSummary{{ID: "b1"}, {ID: "b2"}, {ID: "b3"}}
	states := map[string]*database.UserBookState{
		"b1": nil,
		"b2": {UserID: "alice", BookID: "b2", Status: database.UserBookStatusUnstarted},
		"b3": {UserID: "alice", BookID: "b3", Status: database.UserBookStatusInProgress},
	}
	got, err := listWithSummaries(t, summaries, ListFilters{UserID: "alice", PerUserFilters: []FieldFilter{
		{Field: "read_status", Value: "unstarted"},
	}}, states)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b1", "b2"}, ids(got))
}

func TestListFilter_DateValuesAreValidatedAs400s(t *testing.T) {
	for _, tc := range []struct {
		f    FieldFilter
		want string
	}{
		{FieldFilter{Field: "date_added", Value: ">30d"}, "-30d"},
		{FieldFilter{Field: "last_played", Value: "30d"}, "-30d"},
		{FieldFilter{Field: "date_modified", Value: "yesterday"}, "invalid date"},
		{FieldFilter{Field: "date_added", Value: "2026-02-31"}, "invalid date"},
		{FieldFilter{Field: "last_played", Value: "-30d..soon"}, "soon"},
		// The indexed dates are always set, so * is not a form for them.
		{FieldFilter{Field: "date_added", Value: "*"}, "empty date"},
	} {
		err := ValidateFilterValue(tc.f)
		require.Error(t, err, "%s:%s", tc.f.Field, tc.f.Value)
		assert.Contains(t, err.Error(), tc.want)
	}
	for _, f := range []FieldFilter{
		{Field: "date_added", Value: ">-1y"},
		{Field: "added", Value: "2026-01-01..2026-03-31"},
		{Field: "last_played", Value: "*"},
		{Field: "last_played", Value: ">=2026-01-01"},
	} {
		assert.NoError(t, ValidateFilterValue(f), "%s:%s", f.Field, f.Value)
	}
}

func TestListFilter_DateFieldsAreKnown(t *testing.T) {
	for _, f := range []string{"date_added", "added", "date_modified", "modified"} {
		assert.True(t, FieldIsKnown(f), f)
		assert.Contains(t, KnownFilterFields(), f)
	}
}

func TestHasRelativeDateFilter(t *testing.T) {
	rel := []FieldFilter{{Field: "date_added", Value: ">-1y"}}
	assert.True(t, HasRelativeDateFilter(rel))
	assert.True(t, HasRelativeDateFilter(nil, []FieldFilter{{Field: "last_played", Value: "-30d..now"}}))
	assert.True(t, HasRelativeDateFilter([]FieldFilter{{Field: "added", Value: "<now"}}))
	assert.False(t, HasRelativeDateFilter([]FieldFilter{{Field: "date_added", Value: ">2026-01-01"}}))
	assert.False(t, HasRelativeDateFilter([]FieldFilter{{Field: "last_played", Value: "*"}}))
	assert.False(t, HasRelativeDateFilter([]FieldFilter{{Field: "title", Value: "-30d"}}))
	assert.False(t, HasRelativeDateFilter(nil))
}
