// file: internal/database/pebble_fallback_author_sort_test.go
// version: 1.0.0
// guid: 8e4c1b06-2fd7-45a9-b3e1-70c5a9d248f3
// last-edited: 2026-08-25

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Author and series must sort the same on BOTH store paths.
//
// The Pebble fallback decodes a Book straight from its row, where Author and
// Series are nil unless some past writer happened to inline one. Every book
// then compares equal under those comparators and the page comes back in
// Pebble key order -- while HonorsEveryBookSummaryFilter tells the service the
// page is ordered, so the service skips its own sort on the strength of it.
//
// This is not a warmup-window concern. UseMemDB=false is the abandoned-warmup
// state and is permanent for the process (see memSync in memdb_sync.go), and
// the ABS /items path delegates ordering entirely to the store.
//
// pfa* names are task-unique per repo convention for package-shared helpers.

func pfaSeed(t *testing.T, p *PebbleStore) map[string]string {
	t.Helper()
	marks := make(map[string]string, 6)
	// Neither ascending nor descending by name, so key order can satisfy
	// neither direction by accident.
	for _, rank := range []int{3, 0, 5, 1, 4, 2} {
		a, err := p.CreateAuthor(fmt.Sprintf("author-%d", rank))
		require.NoError(t, err)
		s, err := p.CreateSeries(fmt.Sprintf("series-%d", rank), nil)
		require.NoError(t, err)
		primary := true
		created, err := p.CreateBook(&Book{
			Title:            fmt.Sprintf("book-%d", rank),
			FilePath:         fmt.Sprintf("/tmp/pfa_%d.m4b", rank),
			IsPrimaryVersion: &primary,
			AuthorID:         &a.ID,
			SeriesID:         &s.ID,
			// Deliberately NOT setting Book.Author/Book.Series: the name must
			// be resolved from the ID, which is the only thing every row is
			// guaranteed to carry.
		})
		require.NoError(t, err)
		marks[created.ID] = fmt.Sprintf("r%d", rank)
	}
	p.WaitForWarmup()
	return marks
}

func TestAuthorSeriesSortAgreesOnBothStorePaths(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	marks := pfaSeed(t, p)
	want := []string{"r0", "r1", "r2", "r3", "r4", "r5"}

	for _, field := range []string{"author", "series"} {
		for _, useMemDB := range []bool{true, false} {
			name := fmt.Sprintf("%s/memdb=%v", field, useMemDB)
			t.Run(name, func(t *testing.T) {
				p.UseMemDB = useMemDB
				t.Cleanup(func() { p.UseMemDB = true })

				got, err := p.GetAllBookSummariesFiltered(0, 0,
					BookSummaryFilter{SortBy: field, SortAscending: true})
				require.NoError(t, err)
				order := make([]string, 0, len(got))
				for _, s := range got {
					order = append(order, marks[s.ID])
				}
				require.Equalf(t, want, order,
					"sort_by=%s on the %s path; the name must be resolved from the ID, "+
						"not read from an inline pointer the row may not carry",
					field, map[bool]string{true: "memdb", false: "Pebble fallback"}[useMemDB])
			})
		}
	}
}
