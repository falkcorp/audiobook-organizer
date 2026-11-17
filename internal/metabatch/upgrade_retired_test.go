// file: internal/metabatch/upgrade_retired_test.go
// version: 1.0.0
// guid: 5b7e1d93-4c28-4a60-b1f5-8e3a9c0d2f47
// last-edited: 2026-11-17

package metabatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The nightly upgrade searches a book, then REPLACES its filled fields. A
// retired book (in the trash, or absorbed into another book) gets neither the
// search nor the write. One retirement field per case.
func TestUpgrade_RefusesRetiredBook(t *testing.T) {
	cases := map[string]func(b *database.Book){
		"soft_deleted": func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes },
		"merged_into":  func(b *database.Book) { into := "survivor-book-id"; b.MergedIntoBookID = &into },
	}
	for name, retire := range cases {
		t.Run(name, func(t *testing.T) {
			f := syntheticRankFixture()
			retire(f.book)
			svc, fetcher := f.service(syntheticCandidate("Audible", 0.95))
			out, _ := svc.tryUpgradeBook(context.Background(), f.book.ID, "open_library")
			require.False(t, out.Upgraded, "a retired book was upgraded")
			require.Zero(t, fetcher.searches, "a provider was searched for a retired book")
			require.Nil(t, f.written(), "a retired book was written")
		})
	}
}
