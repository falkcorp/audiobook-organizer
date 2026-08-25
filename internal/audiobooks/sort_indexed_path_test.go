// file: internal/audiobooks/sort_indexed_path_test.go
// version: 1.0.0
// guid: c15a83f7-90e2-4b64-a7d3-6f0e2b94d517
// last-edited: 2026-08-25

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// Enabling a sort index must not BREAK that sort.
//
// It did. A key with an index takes a different route through GetAudiobooks --
// database.CanPushDownSort is true, so heavySorting is false and the request
// goes down the light branch. That branch did not set alreadySortedAndPaginated,
// so the store's correctly ordered page was re-sorted by the trailing
// applySorting against fields bookSummariesToBooks does not carry. Every pair
// tied and SortBooks' ID tiebreaker rewrote it into insertion order.
//
// The keys affected are exactly those that CAN be indexed and are ABSENT from
// BookSummary: year, bitrate, bitrate_kbps. "year" is the shipped default for
// enabled_sort_indexes, so it was the one sort a default install had enabled.
//
// No other test in the repo calls database.SetEnabledSortIndexes, so every
// existing sort test runs with an empty enabled set and exercises only the
// unindexed route. That is why this shipped green.
//
// sidx* names are task-unique per repo convention for package-shared helpers.

type sidxCase struct {
	field string
	set   func(b *database.Book, rank int)
}

var sidxCases = []sidxCase{
	// BookSummary-ABSENT and indexable -- the broken set.
	{"year", func(b *database.Book, r int) { v := 2000 + r; b.AudiobookReleaseYear = &v }},
	{"bitrate", func(b *database.Book, r int) { v := 64 * (r + 1); b.Bitrate = &v }},
	{"bitrate_kbps", func(b *database.Book, r int) { v := 64 * (r + 1); b.Bitrate = &v }},
	// BookSummary-CARRIED and indexable -- these always worked; they are the
	// control that proves the fixture and the enabled index are wired up.
	{"narrator", func(b *database.Book, r int) { v := fmt.Sprintf("%d-narr", r); b.Narrator = &v }},
	{"duration", func(b *database.Book, r int) { v := 100 * (r + 1); b.Duration = &v }},
	{"file_size", func(b *database.Book, r int) { v := int64(1000 * (r + 1)); b.FileSize = &v }},
}

func TestSortIsCorrectWithTheIndexEnabled(t *testing.T) {
	for _, tc := range sidxCases {
		t.Run(tc.field, func(t *testing.T) {
			// MUST precede store open: memdbSchema() reads the enabled set once,
			// when NewPebbleStore builds it.
			unknown := database.SetEnabledSortIndexes([]string{tc.field})
			require.Emptyf(t, unknown, "%q must be a recognised index name", tc.field)
			t.Cleanup(func() { database.SetEnabledSortIndexes(nil) })

			require.Truef(t, database.CanPushDownSort(tc.field),
				"%q must be push-downable here, or this test is silently exercising the "+
					"unindexed route and proves nothing about the indexed one", tc.field)

			ps, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = ps.Close() })
			ps.WaitForWarmup()

			// Insertion order 1,2,0 so "no ordering applied" is distinguishable
			// from "correct descending".
			marks := map[string]string{}
			for _, rank := range []int{1, 2, 0} {
				primary := true
				b := &database.Book{
					Title:            fmt.Sprintf("ins%d", rank),
					FilePath:         fmt.Sprintf("/tmp/sidx_%s_%d.m4b", tc.field, rank),
					IsPrimaryVersion: &primary,
				}
				tc.set(b, rank)
				created, cerr := ps.CreateBook(b)
				require.NoError(t, cerr)
				marks[created.ID] = fmt.Sprintf("rank%d", rank)
			}
			ps.WaitForWarmup()

			svc := NewAudiobookService(ps)
			got, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
				ListFilters{SortBy: tc.field, SortOrder: "asc"})
			require.NoError(t, err)
			require.Len(t, got, 3)

			order := make([]string, 0, 3)
			for _, b := range got {
				order = append(order, marks[b.ID])
			}
			require.Equalf(t, []string{"rank0", "rank1", "rank2"}, order,
				"sort_by=%s with its index ENABLED; [rank1 rank2 rank0] means the store's "+
					"ordered page was re-sorted into insertion order", tc.field)

			// Paging must also come from the ordered set.
			page, err := svc.GetAudiobooks(context.Background(), 1, 1, "", nil, nil,
				ListFilters{SortBy: tc.field, SortOrder: "asc"})
			require.NoError(t, err)
			require.Len(t, page, 1)
			require.Equalf(t, "rank1", marks[page[0].ID],
				"sort_by=%s limit=1 offset=1 must be the middle row of the ORDERED set", tc.field)
		})
	}
}
