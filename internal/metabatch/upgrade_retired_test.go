// file: internal/metabatch/upgrade_retired_test.go
// version: 1.0.1
// guid: 5b7e1d93-4c28-4a60-b1f5-8e3a9c0d2f47
// last-edited: 2026-11-17

package metabatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
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

// retiringFetcher retires the book while its search runs: the gate read the
// live row before the search, and the apply's own re-read under the write lock
// is the only check left.
type retiringFetcher struct {
	*rankFetcher
	retire func()
}

func (r retiringFetcher) SearchMetadataForBook(id, q string, opts ...string) (*metafetch.SearchMetadataResponse, error) {
	resp, err := r.rankFetcher.SearchMetadataForBook(id, q, opts...)
	r.retire()
	return resp, err
}

// A book retired AFTER the upgrade read it and BEFORE its write is a skip with
// its own reason (upgradeSkipRetiredDuringRun), not an error and not an
// upgrade, and nothing is written. The book starts live, so the check at the
// top of tryUpgradeBook passes it; only the apply's re-read can refuse it.
func TestUpgrade_BookRetiredWhileBeingSearchedIsASkipNotAWrite(t *testing.T) {
	cases := map[string]func(b *database.Book){
		"soft_deleted": func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes },
		"merged_into":  func(b *database.Book) { into := "survivor-book-id"; b.MergedIntoBookID = &into },
	}
	for name, retire := range cases {
		t.Run(name, func(t *testing.T) {
			f := syntheticRankFixture()
			svc, fetcher := f.service(syntheticCandidate("Audible", 0.95))
			svc.Fetcher = retiringFetcher{rankFetcher: fetcher, retire: func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				retire(f.book)
			}}
			out, err := svc.tryUpgradeBook(context.Background(), f.book.ID, "open_library")
			require.NoError(t, err, "a retire that races the write is a skip, not a failure")
			require.False(t, out.Upgraded)
			require.Equal(t, upgradeSkipRetiredDuringRun, out.Reason)
			require.Equal(t, 1, fetcher.searches, "the book was live when the search started")
			require.Nil(t, f.written(), "a book retired during the search was written")
		})
	}
}

// Control: the same fixture and the same racing fetcher with nothing retired
// upgrades, so the skip above is the retirement check's doing and not a
// fixture that cannot upgrade.
func TestUpgrade_ControlNothingRetiredWhileBeingSearchedUpgrades(t *testing.T) {
	f := syntheticRankFixture()
	svc, fetcher := f.service(syntheticCandidate("Audible", 0.95))
	svc.Fetcher = retiringFetcher{rankFetcher: fetcher, retire: func() {}}
	out, err := svc.tryUpgradeBook(context.Background(), f.book.ID, "open_library")
	require.NoError(t, err)
	require.True(t, out.Upgraded, "control did not upgrade: %+v", out)
	require.NotNil(t, f.written())
}
