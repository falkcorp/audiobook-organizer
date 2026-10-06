// file: internal/database/metadata_cache_asin_stamp_test.go
// version: 1.0.0
// guid: 9c3f1a7e-52d8-4b6a-8e0f-d41b7c2a9e53
// last-edited: 2026-10-05

package database

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// A kept cache row records the ASIN its candidates were fetched for
// (FetchedForASIN), so a candidate with no ASIN of its own reads as stale once
// the book's ASIN is replaced (MetadataCandidateCache.ASINReplaced).

func cacheStamp(t *testing.T, s *PebbleStore, bookID string) string {
	t.Helper()
	entry, err := s.GetMetadataCache(bookID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	return entry.FetchedForASIN
}

// TestUpdateBook_ReplacedASINStampsTheKeptRow: replacing or clearing a
// non-empty ASIN stamps an unstamped kept row with the old ASIN (and moves
// the cache generation); filling an empty one, or rewriting it with another
// case, does not touch the row. A later replacement keeps the first stamp,
// so restoring the original ASIN reads as current again.
func TestUpdateBook_ReplacedASINStampsTheKeptRow(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/s/x.m4b", Format: "m4b"})
	require.NoError(t, err)
	seedCachedCandidate(t, s, b.ID, "")

	g := s.MetadataCacheGeneration()
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00FIRSTAS"); return nil })
	require.NoError(t, err)
	require.Empty(t, cacheStamp(t, s, b.ID), "filling an empty ASIN records nothing")
	require.Equal(t, g, s.MetadataCacheGeneration(), "a fill rewrites no cache row")

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp(" b00firstas "); return nil })
	require.NoError(t, err)
	require.Empty(t, cacheStamp(t, s, b.ID), "the same ASIN in another case is not a replacement")

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00SECONDA"); return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 1, "a replaced ASIN keeps the row")
	// The book held " b00firstas " (the case-only rewrite above), stored
	// trimmed; the stamp is that value.
	require.Equal(t, "b00firstas", cacheStamp(t, s, b.ID), "the row records the ASIN it was fetched for")
	require.Equal(t, g+1, s.MetadataCacheGeneration(), "the rewritten row moves the generation")
	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	was, replaced := entry.ASINReplaced(strp("B00SECONDA"))
	require.True(t, replaced)
	require.Equal(t, "b00firstas", was)

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00THIRDAS"); return nil })
	require.NoError(t, err)
	require.Equal(t, "b00firstas", cacheStamp(t, s, b.ID), "a second replacement keeps the first stamp")

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00FIRSTAS"); return nil })
	require.NoError(t, err)
	entry, err = s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	_, replaced = entry.ASINReplaced(strp("B00FIRSTAS"))
	require.False(t, replaced, "the ASIN the row was fetched for is current again")
}

// TestUpdateBook_ClearedASINStampsTheKeptRow: clearing the ASIN takes the
// record the candidates were matched against off the book.
func TestUpdateBook_ClearedASINStampsTheKeptRow(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/s/y.m4b", Format: "m4b", ASIN: strp("B00GONEASI")})
	require.NoError(t, err)
	seedCachedCandidate(t, s, b.ID, "")
	_, err = s.UpdateBook(b.ID, func() *Book { c := *b; c.ASIN = nil; return &c }())
	require.NoError(t, err)
	require.Equal(t, "B00GONEASI", cacheStamp(t, s, b.ID))
	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	_, replaced := entry.ASINReplaced(nil)
	require.True(t, replaced)
}

// TestUpdateBook_ReplacedASINKeepsAnExistingStamp: a row the fetch already
// stamped (with the ASIN it searched under) is not rewritten.
func TestUpdateBook_ReplacedASINKeepsAnExistingStamp(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/s/z.m4b", Format: "m4b", ASIN: strp("B00BOOKASI")})
	require.NoError(t, err)
	raw, _ := json.Marshal(map[string]any{"title": "The Book"})
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: b.ID, FetchedAt: time.Now(),
		Candidates: []json.RawMessage{raw}, SourceHash: "h", FetchedForASIN: "B00FETCHED"}))
	g := s.MetadataCacheGeneration()
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00NEWONEA"); return nil })
	require.NoError(t, err)
	require.Equal(t, "B00FETCHED", cacheStamp(t, s, b.ID))
	require.Equal(t, g, s.MetadataCacheGeneration(), "an already-stamped row is not rewritten")
}

// TestUpdateBook_ReplacedASINWithoutRowWritesNothing: no row, nothing to
// stamp, and no cache key is created.
func TestUpdateBook_ReplacedASINWithoutRowWritesNothing(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/s/w.m4b", Format: "m4b", ASIN: strp("B00NOROWAS")})
	require.NoError(t, err)
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00NOROWAT"); return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 0, "no row is created")
}

// TestUpdateBook_ASINStampDoesNotClobberAConcurrentFetch: a fetch that writes
// the row while the book write holds the stamped copy in its batch must not
// be overwritten by the older candidates. The fetch's PutMetadataCache
// starts inside the book write's pre-commit window; with the per-book cache
// lock it waits for the commit and lands after it, so the fresh row is what
// remains. Without the lock it would land first and the commit would put the
// stale candidates back.
func TestUpdateBook_ASINStampDoesNotClobberAConcurrentFetch(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/s/v.m4b", Format: "m4b", ASIN: strp("B00RACEOLD")})
	require.NoError(t, err)
	seedCachedCandidate(t, s, b.ID, "")

	fresh, _ := json.Marshal(map[string]any{"title": "Fresh One"})
	freshTwo, _ := json.Marshal(map[string]any{"title": "Fresh Two"})
	var wg sync.WaitGroup
	putDone := make(chan struct{})
	bookWriteBatchPreCommitHook = func(op, id string, _ *pebble.Batch) {
		if op != "update" || id != b.ID {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(putDone)
			_ = s.PutMetadataCache(&MetadataCandidateCache{BookID: b.ID, FetchedAt: time.Now(),
				Candidates: []json.RawMessage{fresh, freshTwo}, SourceHash: "h2", FetchedForASIN: "B00RACENEW"})
		}()
		// Give an unlocked Put every chance to land before the commit.
		select {
		case <-putDone:
		case <-time.After(150 * time.Millisecond):
		}
	}
	t.Cleanup(func() { bookWriteBatchPreCommitHook = nil })

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00RACENEW"); return nil })
	require.NoError(t, err)
	wg.Wait()
	bookWriteBatchPreCommitHook = nil

	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Len(t, entry.Candidates, 2, "the concurrent fetch's candidates must survive the book write")
	require.Equal(t, "B00RACENEW", entry.FetchedForASIN)
}

func TestMetadataCandidateCache_ASINReplaced(t *testing.T) {
	cases := []struct {
		stamp string
		book  *string
		want  bool
	}{
		{"", nil, false},
		{"", strp("B00ANYASIN"), false},
		{"B00ONEASIN", strp("B00ONEASIN"), false},
		{" b00oneasin", strp("B00ONEASIN "), false},
		{"B00ONEASIN", strp("B00TWOASIN"), true},
		{"B00ONEASIN", nil, true},
		{"B00ONEASIN", strp("  "), true},
	}
	for _, tc := range cases {
		_, got := (&MetadataCandidateCache{FetchedForASIN: tc.stamp}).ASINReplaced(tc.book)
		require.Equal(t, tc.want, got, "stamp %q book %v", tc.stamp, tc.book)
	}
	_, got := (*MetadataCandidateCache)(nil).ASINReplaced(strp("B00ANYASIN"))
	require.False(t, got)
}
