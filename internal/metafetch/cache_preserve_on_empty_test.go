// file: internal/metafetch/cache_preserve_on_empty_test.go
// version: 1.0.0
// guid: 5c1e93a7-4b62-4d08-9f3a-1e7d0b56c284
// last-edited: 2026-09-08

// An empty search result used to DESTROY the candidates an earlier search had
// found. cacheSearchResponse wrote unconditionally -- its doc comment said
// "always replaces" -- so one provider outage, one rate-limit window, one
// mis-parsed title was enough to replace a good entry with `Candidates: []` and
// stamp it with a fresh FetchedAt on the way out.
//
// Two things made that invisible rather than merely wasteful. A book's review
// verdict lives on the BOOK (database.Book.MetadataReviewStatus), not on the
// cache entry, so the verdict outlived the evidence behind it: production on
// 2026-09-08 held 212 books carrying a matched/no_match/audio_confirmed verdict
// with zero candidates to justify it. And the review endpoint files a
// zero-candidate row under "unreviewable", so the book did not appear as
// damaged -- it simply left the review queue.
//
// These tests use a real PebbleStore because the stored entry is the subject:
// the bug was entirely in what got persisted, and a fake that returns whatever
// it was handed cannot fail them.
package metafetch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const (
	preserveBookID   = "book-preserve-1"
	preserveQuery    = "The Long Way to a Small Angry Planet"
	preserveAuthor   = "Becky Chambers"
	preserveNarrator = "Patricia Rodriguez"
	preserveSeries   = "Wayfarers"
)

func preserveFixture(t *testing.T) *Service {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store)
}

// seed writes one good candidate the way a successful search would, and returns
// the entry that got persisted.
func seedGoodEntry(t *testing.T, mfs *Service) *MetadataCandidateCache {
	t.Helper()
	resp := &SearchMetadataResponse{Results: []MetadataCandidate{{Title: preserveQuery}}}
	entry := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, preserveNarrator, preserveSeries, resp)
	require.Len(t, entry.Candidates, 1, "fixture must start from a cache entry that HAS a candidate")
	return entry
}

func TestCacheSearchResponse_EmptyResultKeepsExistingCandidates(t *testing.T) {
	mfs := preserveFixture(t)
	seeded := seedGoodEntry(t, mfs)

	// The same book, the same search inputs, and this time the providers have
	// nothing. This is the call that used to wipe the entry.
	empty := &SearchMetadataResponse{Results: nil}
	got := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, preserveNarrator, preserveSeries, empty)

	require.Len(t, got.Candidates, 1,
		"an empty search must not erase candidates an earlier search found")

	// Read it back rather than trusting the returned value: the bug was in what
	// reached the database, and a correct return value with a destructive write
	// would still lose the data.
	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Len(t, stored.Candidates, 1, "the PERSISTED entry must still hold the candidate")

	// FetchedAt dates the candidates, and these are the ones the earlier search
	// returned -- moving it would relabel them as freshly fetched and hide their
	// real age from the review UI.
	require.True(t, stored.FetchedAt.Equal(seeded.FetchedAt),
		"FetchedAt must not move on an empty result: it dates the CANDIDATES, which did not change")

	// The fruitless look is still recorded, so a stale-refetch pass can tell
	// "nobody has looked in 30 days" from "we looked and there is nothing".
	// Without this the preserved entry would sit permanently past the TTL and
	// every refetch pass would pick the same unmatchable books forever.
	require.NotNil(t, stored.LastEmptyFetchAt,
		"an empty search must record LastEmptyFetchAt so it is not retried forever")
	require.False(t, stored.LastEmptyFetchAt.Before(stored.FetchedAt),
		"LastEmptyFetchAt should be at or after the candidates it accompanies")
}

func TestCacheSearchResponse_EmptyResultReplacesWhenSearchInputsChanged(t *testing.T) {
	mfs := preserveFixture(t)
	seedGoodEntry(t, mfs)

	// Different author: the book's identity drifted underneath the cache, so the
	// stored candidates answer a question nobody is asking any more. Preserving
	// them here would be the opposite bug -- serving a reviewer candidates for a
	// book that no longer exists under that name.
	empty := &SearchMetadataResponse{Results: nil}
	got := mfs.cacheSearchResponse(preserveBookID, preserveQuery, "A Completely Different Author", preserveNarrator, preserveSeries, empty)

	require.Empty(t, got.Candidates,
		"changed search inputs must still replace: SourceHash is the discriminator, not the emptiness")

	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Empty(t, stored.Candidates, "the PERSISTED entry must be replaced when the inputs changed")
	// Still an empty search, so it is still recorded as one. LastEmptyFetchAt
	// means "the last search for these inputs found nothing" -- not "we
	// preserved something" -- so it is set on every empty result regardless of
	// what happened to the candidates.
	require.NotNil(t, stored.LastEmptyFetchAt,
		"a search that returned nothing is an empty fetch even when it replaced")
}

func TestCacheSearchResponse_NonEmptyResultStillReplaces(t *testing.T) {
	mfs := preserveFixture(t)
	seeded := seedGoodEntry(t, mfs)

	// Guard against over-correcting. Preserve-on-empty must not turn the cache
	// into an append-only store: a search that DOES find something is still the
	// newest truth and must overwrite, with FetchedAt moving to match.
	require.Eventually(t, func() bool { return !nowUTC().Equal(seeded.FetchedAt) }, time.Second, time.Millisecond,
		"need a distinguishable clock tick before asserting FetchedAt moved")

	resp := &SearchMetadataResponse{Results: []MetadataCandidate{
		{Title: "A Newer, Better Match"},
		{Title: "Runner Up"},
	}}
	got := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, preserveNarrator, preserveSeries, resp)
	require.Len(t, got.Candidates, 2)

	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Len(t, stored.Candidates, 2, "a search WITH results must replace, exactly as before")
	require.True(t, stored.FetchedAt.After(seeded.FetchedAt),
		"FetchedAt must move when new candidates are actually stored")
	require.Nil(t, stored.LastEmptyFetchAt,
		"finding candidates CLEARS the empty-fetch marker rather than carrying a stale one forward")
}

func TestCacheSearchResponse_EmptyResultOnEmptyCacheStillWrites(t *testing.T) {
	mfs := preserveFixture(t)

	// Nothing stored yet and nothing found: the entry must still be written, so
	// the row exists and its FetchedAt records that we looked. Skipping the
	// write here would leave the book invisible to the review endpoint entirely
	// rather than merely unreviewable.
	empty := &SearchMetadataResponse{Results: nil}
	got := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, preserveNarrator, preserveSeries, empty)
	require.Empty(t, got.Candidates)

	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.NotNil(t, stored, "an empty first fetch must still create the cache row")
	require.Empty(t, stored.Candidates)
	require.False(t, stored.FetchedAt.IsZero())
	require.NotNil(t, stored.LastEmptyFetchAt,
		"the invariant is uniform: every empty result records when it happened")
}
