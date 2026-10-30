// file: internal/audiobooks/list_query_test.go
// version: 1.0.0
// guid: a9572478-f826-4128-b318-9035db01bf94
// last-edited: 2026-10-30

package audiobooks

import (
	"math"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func batmanAuthors() *AuthorWithCountListResponse {
	al := func(id, authorID int, name string) database.AuthorAlias {
		return database.AuthorAlias{ID: id, AuthorID: authorID, AliasName: name, AliasType: "alias"}
	}
	items := []AuthorWithCount{
		{ID: 1, Name: "Bruce Wayne", BookCount: 4, Aliases: []database.AuthorAlias{al(1, 1, "The Dark Knight")}},
		{ID: 2, Name: "alfred Pennyworth", BookCount: 2},
		{ID: 3, Name: "Selina Kyle", BookCount: 0, Aliases: []database.AuthorAlias{al(2, 3, "Catwoman"), al(3, 3, "Holly Robinson")}},
		{ID: 4, Name: "Dick Grayson", BookCount: 4, Aliases: []database.AuthorAlias{al(4, 4, "Nightwing")}},
		{ID: 5, Name: "Barbara Gordon", BookCount: 0},
		{ID: 6, Name: "Harvey Dent", BookCount: 1},
	}
	for i := range items {
		if items[i].Aliases == nil {
			items[i].Aliases = []database.AuthorAlias{}
		}
	}
	return &AuthorWithCountListResponse{Items: items, Count: len(items), Total: len(items)}
}

func authorIDs(r *AuthorWithCountListResponse) []int {
	out := make([]int, len(r.Items))
	for i, a := range r.Items {
		out[i] = a.ID
	}
	return out
}

func TestAuthorsApply_WindowKeepsMatchCountAndTotal(t *testing.T) {
	r := batmanAuthors()
	got := r.Apply(ListQuery{Paged: true, Limit: 2, Offset: 2})
	assert.Equal(t, []int{3, 4}, authorIDs(got))
	assert.Equal(t, 6, got.Count)
	assert.Equal(t, 6, got.Total)
}

func TestAuthorsApply_OffsetPastEndIsEmpty(t *testing.T) {
	got := batmanAuthors().Apply(ListQuery{Paged: true, Limit: 5, Offset: 99})
	assert.Empty(t, got.Items)
	assert.Equal(t, 6, got.Count)
}

func TestAuthorsApply_FilterThenCountIsMatches(t *testing.T) {
	r := batmanAuthors()
	with := r.Apply(ListQuery{Filter: AuthorFilterWithBooks, Paged: true, Limit: 2})
	assert.Equal(t, []int{1, 2}, authorIDs(with))
	assert.Equal(t, 4, with.Count, "count is the matches across every page")
	assert.Equal(t, 6, with.Total, "total is the whole table")

	zero := r.Apply(ListQuery{Filter: AuthorFilterZeroBooks})
	assert.Equal(t, []int{3, 5}, authorIDs(zero))
}

func TestAuthorsApply_SearchMatchesNameAndAlias(t *testing.T) {
	r := batmanAuthors()
	assert.Equal(t, []int{5}, authorIDs(r.Apply(ListQuery{Search: "BARBARA"})), "case-insensitive name match")
	assert.Equal(t, []int{3}, authorIDs(r.Apply(ListQuery{Search: "catwoman"})), "alias match finds the author")
	assert.Equal(t, []int{1, 4}, authorIDs(r.Apply(ListQuery{Search: "night"})), "aliases \"The Dark Knight\" and \"Nightwing\" both contain night")
	assert.Empty(t, r.Apply(ListQuery{Search: "joker"}).Items)
}

func TestAuthorsApply_SortNameIsCaseInsensitive(t *testing.T) {
	r := batmanAuthors()
	// "alfred" is stored lower-case: a byte-wise sort would put it last.
	assert.Equal(t, []int{2, 5, 1, 4, 6, 3}, authorIDs(r.Apply(ListQuery{Sort: "name"})))
	assert.Equal(t, []int{3, 6, 4, 1, 5, 2}, authorIDs(r.Apply(ListQuery{Sort: "name", Desc: true})))
}

func TestAuthorsApply_SortTiesBreakOnIDInBothDirections(t *testing.T) {
	r := batmanAuthors()
	// book_count 4 appears for ids 1 and 4; 0 for 3 and 5.
	assert.Equal(t, []int{3, 5, 6, 2, 1, 4}, authorIDs(r.Apply(ListQuery{Sort: "book_count"})))
	assert.Equal(t, []int{1, 4, 2, 6, 3, 5}, authorIDs(r.Apply(ListQuery{Sort: "book_count", Desc: true})))
}

func TestAuthorsApply_SortByAliasCountAndID(t *testing.T) {
	r := batmanAuthors()
	assert.Equal(t, []int{3, 1, 4, 2, 5, 6}, authorIDs(r.Apply(ListQuery{Sort: "alias_count", Desc: true})))
	assert.Equal(t, []int{6, 5, 4, 3, 2, 1}, authorIDs(r.Apply(ListQuery{Sort: "id", Desc: true})))
}

// Sorting happens before the window, so page 2 is the second slice of the
// SORTED list, not a sorted second slice of the unsorted one.
func TestAuthorsApply_SortsBeforeWindowing(t *testing.T) {
	r := batmanAuthors()
	got := r.Apply(ListQuery{Sort: "book_count", Desc: true, Paged: true, Limit: 2, Offset: 2})
	assert.Equal(t, []int{2, 6}, authorIDs(got))
}

// The cached list is shared by every request; a sort must not reorder it.
func TestAuthorsApply_DoesNotMutateSource(t *testing.T) {
	r := batmanAuthors()
	_ = r.Apply(ListQuery{Sort: "name", Desc: true})
	assert.Equal(t, []int{1, 2, 3, 4, 5, 6}, authorIDs(r))
}

func TestListQueryValidate(t *testing.T) {
	require.NoError(t, ListQuery{Filter: "zero_books", Sort: "alias_count"}.ValidateAuthors())
	require.Error(t, ListQuery{Filter: "empty"}.ValidateAuthors(), "empty is a series filter")
	require.Error(t, ListQuery{Sort: "created_at"}.ValidateAuthors())
	require.NoError(t, ListQuery{Filter: "empty", Sort: "author_name"}.ValidateSeries())
	require.Error(t, ListQuery{Filter: "zero_books"}.ValidateSeries())
	require.Error(t, ListQuery{Sort: "created_at"}.ValidateSeries(), "series rows carry no creation time")
}

func TestListQueryNarrows(t *testing.T) {
	assert.False(t, ListQuery{}.Narrows())
	assert.False(t, ListQuery{Filter: "all"}.Narrows())
	assert.True(t, ListQuery{Paged: true}.Narrows())
	assert.True(t, ListQuery{Search: "x"}.Narrows())
	assert.True(t, ListQuery{Filter: "with_books"}.Narrows())
	assert.True(t, ListQuery{Sort: "id"}.Narrows())
}

func batmanSeries() *SeriesWithCountsResponse {
	return &SeriesWithCountsResponse{Items: []SeriesWithCount{
		{Series: database.Series{ID: 1, Name: "Year One"}, BookCount: 1, AuthorName: "Frank Miller"},
		{Series: database.Series{ID: 2, Name: "The Long Halloween"}, BookCount: 3, AuthorName: "Jeph Loeb"},
		{Series: database.Series{ID: 3, Name: "hush"}, BookCount: 0, AuthorName: "Jeph Loeb"},
		{Series: database.Series{ID: 4, Name: "Knightfall"}, BookCount: 3},
		{Series: database.Series{ID: 5, Name: "Court of Owls"}, BookCount: 0, AuthorName: "Scott Snyder"},
	}, Count: 5, Total: 5}
}

func seriesIDs(r *SeriesWithCountsResponse) []int {
	out := make([]int, len(r.Items))
	for i, s := range r.Items {
		out[i] = s.ID
	}
	return out
}

func TestSeriesApply_FilterSearchSortWindow(t *testing.T) {
	r := batmanSeries()

	empty := r.Apply(ListQuery{Filter: SeriesFilterEmpty})
	assert.Equal(t, []int{3, 5}, seriesIDs(empty))

	with := r.Apply(ListQuery{Filter: SeriesFilterWithBooks, Sort: "name", Paged: true, Limit: 2})
	assert.Equal(t, []int{4, 2}, seriesIDs(with), "Knightfall, The Long Halloween ... Year One is on page 2")
	assert.Equal(t, 3, with.Count)
	assert.Equal(t, 5, with.Total)

	// Search matches the series name AND the author name.
	assert.Equal(t, []int{2, 3}, seriesIDs(r.Apply(ListQuery{Search: "loeb"})))
	assert.Equal(t, []int{3}, seriesIDs(r.Apply(ListQuery{Search: "HUSH"})))
}

func TestSeriesApply_SortByAuthorAndBookCount(t *testing.T) {
	r := batmanSeries()
	// Empty author name sorts first ascending; ties on author break on id.
	assert.Equal(t, []int{4, 1, 2, 3, 5}, seriesIDs(r.Apply(ListQuery{Sort: "author_name"})))
	assert.Equal(t, []int{2, 4, 1, 3, 5}, seriesIDs(r.Apply(ListQuery{Sort: "book_count", Desc: true})))
	assert.Equal(t, []int{1, 2, 3, 4, 5}, seriesIDs(r), "source order untouched")
}

// window is total over every int: Apply must never panic on a window a caller
// built by hand, even though the handler rejects negatives with a 400.
func TestApply_WindowClampsNegativeHugeAndPastEnd(t *testing.T) {
	a, s := batmanAuthors(), batmanSeries()

	got := a.Apply(ListQuery{Paged: true, Offset: -1, Limit: 2})
	assert.Equal(t, []int{1, 2}, authorIDs(got), "negative offset behaves as 0")
	assert.Equal(t, []int{1, 2}, seriesIDs(s.Apply(ListQuery{Paged: true, Offset: -1, Limit: 2})))

	got = a.Apply(ListQuery{Paged: true, Offset: 4, Limit: math.MaxInt})
	assert.Equal(t, []int{5, 6}, authorIDs(got), "MaxInt limit is the tail; start+limit must not overflow")
	assert.Equal(t, []int{4, 5}, seriesIDs(s.Apply(ListQuery{Paged: true, Offset: 3, Limit: math.MaxInt})))

	got = a.Apply(ListQuery{Paged: true, Offset: 7, Limit: 2})
	assert.Empty(t, got.Items, "offset > n is an empty page")
	assert.Equal(t, 6, got.Count)
	assert.Empty(t, s.Apply(ListQuery{Paged: true, Offset: math.MaxInt, Limit: 2}).Items)

	assert.Len(t, a.Apply(ListQuery{Paged: true, Limit: -5}).Items, 6, "negative limit behaves as no limit")
}
