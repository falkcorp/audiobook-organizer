// file: internal/audiobooks/sort_indexed_path_test.go
// version: 1.1.0
// guid: c15a83f7-90e2-4b64-a7d3-6f0e2b94d517
// last-edited: 2026-10-11

package audiobooks

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

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
	// set gives the row its sort value for rank. nil means the store stamps
	// the value itself (created_at: CreateBook sets CreatedAt = now), so the
	// rows are inserted in rank order instead of the scrambled 1,2,0.
	set func(b *database.Book, rank int)
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
	// In the shipped default since 2026-10-11 (ABS "Recently Added", Library
	// date-added). Insertion, ID and creation order all coincide here, so the
	// ascending check alone cannot tell "ordered" from "insertion order"; the
	// descending check below can.
	{"created_at", nil},
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
			insertOrder := []int{1, 2, 0}
			if tc.set == nil {
				insertOrder = []int{0, 1, 2}
			}
			for _, rank := range insertOrder {
				primary := true
				b := &database.Book{
					Title:            fmt.Sprintf("ins%d", rank),
					FilePath:         fmt.Sprintf("/tmp/sidx_%s_%d.m4b", tc.field, rank),
					IsPrimaryVersion: &primary,
				}
				if tc.set != nil {
					tc.set(b, rank)
				} else {
					// Distinct store-stamped timestamps, never a tie.
					time.Sleep(2 * time.Millisecond)
				}
				created, cerr := ps.CreateBook(b)
				require.NoError(t, cerr)
				marks[created.ID] = fmt.Sprintf("rank%d", rank)
			}
			ps.WaitForWarmup()

			svc := NewAudiobookService(ps)
			asc := []string{"rank0", "rank1", "rank2"}
			for _, dir := range []string{"asc", "desc"} {
				want := asc
				if dir == "desc" {
					want = slices.Clone(asc)
					slices.Reverse(want)
				}
				got, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
					ListFilters{SortBy: tc.field, SortOrder: dir})
				require.NoError(t, err)
				require.Len(t, got, 3)

				order := make([]string, 0, 3)
				for _, b := range got {
					order = append(order, marks[b.ID])
				}
				require.Equalf(t, want, order,
					"sort_by=%s %s with its index ENABLED; insertion order means the store's "+
						"ordered page was re-sorted", tc.field, dir)

				// Paging must also come from the ordered set.
				page, err := svc.GetAudiobooks(context.Background(), 1, 1, "", nil, nil,
					ListFilters{SortBy: tc.field, SortOrder: dir})
				require.NoError(t, err)
				require.Len(t, page, 1)
				require.Equalf(t, "rank1", marks[page[0].ID],
					"sort_by=%s %s limit=1 offset=1 must be the middle row of the ORDERED set", tc.field, dir)
			}
		})
	}
}
