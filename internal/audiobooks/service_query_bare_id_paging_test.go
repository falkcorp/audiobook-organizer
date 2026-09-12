// file: internal/audiobooks/service_query_bare_id_paging_test.go
// version: 1.0.0
// guid: 203a19b0-9aea-4fe9-95ee-c0da0cfaff0c
// last-edited: 2026-09-12

// Regression tests for BARE author_id / series_id listings paging.
//
// Before 2026-09-12 GET /api/v1/audiobooks?author_id=N (or ?series_id=N) with
// no other filter skipped the post-filter block in GetAudiobooksWithTotal: it
// returned the author's/series' whole set, ignored limit/offset, and reported
// total -1. These pin the new contract: pages of `limit`, an exact total, the
// service-wide limit<=0 => 50 rule, sort_by applied across pages rather than
// within each page, a deterministic author order, and quarantine excluded
// from both the page and the total.

package audiobooks

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/require"
)

// TestBareAuthorIDPagesWithExactTotal: author A is the primary credit on all
// 12 fixture books, author B a secondary (junction-only) credit on 6. Pages of
// 5 must be full/full/remainder with no repeats and total == set size. The
// memdb author getter returns map-iteration order, so without a service-side
// order the repeated calls here would repeat and skip rows between pages.
func TestBareAuthorIDPagesWithExactTotal(t *testing.T) {
	fx := newIDFilterFixture(t)
	a, b := fx.authorA, fx.authorB

	t.Run("primary author", func(t *testing.T) {
		want := fx.want(func(string) bool { return true })
		require.Len(t, want, 12)
		assertPagesExact(t, want, 5, func(offset int) ([]database.Book, int, error) {
			return fx.svc.GetAudiobooksWithTotal(context.Background(), 5, offset, "", &a, nil, ListFilters{})
		})
	})
	t.Run("secondary author", func(t *testing.T) {
		want := fx.want(func(id string) bool { return fx.inB[id] })
		require.Len(t, want, 6)
		assertPagesExact(t, want, 4, func(offset int) ([]database.Book, int, error) {
			return fx.svc.GetAudiobooksWithTotal(context.Background(), 4, offset, "", &b, nil, ListFilters{})
		})
	})
	t.Run("pages are stable across repeated calls", func(t *testing.T) {
		first, _, err := fx.svc.GetAudiobooksWithTotal(context.Background(), 5, 5, "", &a, nil, ListFilters{})
		require.NoError(t, err)
		for range 5 {
			again, _, err := fx.svc.GetAudiobooksWithTotal(context.Background(), 5, 5, "", &a, nil, ListFilters{})
			require.NoError(t, err)
			require.Equal(t, idsInOrder(first), idsInOrder(again), "page 2 must be the same rows in the same order every call")
		}
	})
}

// TestBareSeriesIDPagesWithExactTotal: series S1 holds 8 of the 12 books.
// Pages of 5 are 5 then 3, total 8 on both.
func TestBareSeriesIDPagesWithExactTotal(t *testing.T) {
	fx := newIDFilterFixture(t)
	s1 := fx.seriesS1
	want := fx.want(func(id string) bool { return fx.inS1[id] })
	require.Len(t, want, 8)
	assertPagesExact(t, want, 5, func(offset int) ([]database.Book, int, error) {
		return fx.svc.GetAudiobooksWithTotal(context.Background(), 5, offset, "", nil, &s1, ListFilters{})
	})
}

// makeCores builds n BookCores with distinct IDs and titles whose ID order
// and title order disagree, so a test can tell which order it received.
func makeCores(n int) []database.BookCore {
	cores := make([]database.BookCore, n)
	for i := range n {
		cores[i] = database.BookCore{
			ID:    fmt.Sprintf("book-%03d", i),
			Title: fmt.Sprintf("Title %03d", n-1-i),
		}
	}
	return cores
}

func idsInOrder(books []database.Book) []string {
	out := make([]string, len(books))
	for i := range books {
		out[i] = books[i].ID
	}
	return out
}

// TestBareIDLimitZeroMeansDefault: the service has no "0 means all" contract
// anywhere (limit<=0 or >100000 becomes 50, and the HTTP layer defaults to 50
// and caps at 1000). A bare id listing follows it: 60 books at limit 0 give 50
// rows and a total of 60. The 12-book fixture cannot tell 50 from "all".
func TestBareIDLimitZeroMeansDefault(t *testing.T) {
	const n = 60
	authorID, seriesID := 11, 22

	t.Run("author_id", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		mockStore.EXPECT().GetBooksByAuthorIDCore(authorID).Return(makeCores(n), nil)
		svc := NewAudiobookService(mockStore)
		books, total, err := svc.GetAudiobooksWithTotal(context.Background(), 0, 0, "", &authorID, nil, ListFilters{})
		require.NoError(t, err)
		require.Len(t, books, 50)
		require.Equal(t, n, total)
	})
	t.Run("series_id", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		mockStore.EXPECT().GetBooksBySeriesIDCore(seriesID).Return(makeCores(n), nil)
		svc := NewAudiobookService(mockStore)
		books, total, err := svc.GetAudiobooksWithTotal(context.Background(), 0, 0, "", nil, &seriesID, ListFilters{})
		require.NoError(t, err)
		require.Len(t, books, 50)
		require.Equal(t, n, total)
	})
}

// TestBareIDSortByIsGlobalAcrossPages: with sort_by, page 1 + page 2 must be
// the globally sorted set. Sorting after slicing ordered each page within
// itself only — the bare listing used to avoid that by never paginating.
func TestBareIDSortByIsGlobalAcrossPages(t *testing.T) {
	const n, limit = 30, 20
	authorID := 5
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByAuthorIDCore(authorID).Return(makeCores(n), nil).Times(2)
	svc := NewAudiobookService(mockStore)
	f := ListFilters{SortBy: "title", SortOrder: "asc"}

	var titles []string
	for offset := 0; offset < n; offset += limit {
		books, total, err := svc.GetAudiobooksWithTotal(context.Background(), limit, offset, "", &authorID, nil, f)
		require.NoError(t, err)
		require.Equal(t, n, total)
		for _, b := range books {
			titles = append(titles, b.Title)
		}
	}
	require.Len(t, titles, n)
	require.True(t, sort.StringsAreSorted(titles), "concatenated pages must be globally sorted by title: %v", titles)
}

// TestBareSeriesIDKeepsSeriesOrder: without sort_by, a series listing keeps the
// order its getter returns (series order). Only the author base set is put in
// ID order; re-sorting a series by ID would scramble its reading order.
func TestBareSeriesIDKeepsSeriesOrder(t *testing.T) {
	seriesID := 9
	cores := makeCores(6)
	// Reverse ID order stands in for "series order": anything but ID order.
	for i, j := 0, len(cores)-1; i < j; i, j = i+1, j-1 {
		cores[i], cores[j] = cores[j], cores[i]
	}
	wantOrder := make([]string, len(cores))
	for i := range cores {
		wantOrder[i] = cores[i].ID
	}
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksBySeriesIDCore(seriesID).Return(cores, nil).Times(2)
	svc := NewAudiobookService(mockStore)

	var got []string
	for offset := 0; offset < len(cores); offset += 4 {
		books, total, err := svc.GetAudiobooksWithTotal(context.Background(), 4, offset, "", nil, &seriesID, ListFilters{})
		require.NoError(t, err)
		require.Equal(t, len(cores), total)
		got = append(got, idsInOrder(books)...)
	}
	require.Equal(t, wantOrder, got)
}

// TestBareIDExcludesQuarantinedFromPageAndTotal: the HTTP handler sets
// ExcludeQuarantined by default and drops quarantined rows after the service
// returns. The author/series getters do not read that flag, so the service
// must exclude them before counting, or the total would count books the page
// never shows and pages would come back short.
func TestBareIDExcludesQuarantinedFromPageAndTotal(t *testing.T) {
	authorID := 4
	cores := makeCores(10)
	now := time.Now()
	cores[2].QuarantinedAt = &now
	cores[7].QuarantinedAt = &now
	mockStore := mocks.NewMockStore(t)
	mockStore.EXPECT().GetBooksByAuthorIDCore(authorID).Return(cores, nil).Times(2)
	svc := NewAudiobookService(mockStore)

	seen := map[string]bool{}
	for offset := 0; offset < 8; offset += 4 {
		books, total, err := svc.GetAudiobooksWithTotal(context.Background(), 4, offset, "", &authorID, nil,
			ListFilters{ExcludeQuarantined: true})
		require.NoError(t, err)
		require.Equal(t, 8, total)
		require.Len(t, books, 4, "offset=%d: page must be full", offset)
		for _, b := range books {
			require.Nil(t, b.QuarantinedAt)
			seen[b.ID] = true
		}
	}
	require.Len(t, seen, 8)
}
