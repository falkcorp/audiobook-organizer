// file: internal/database/pebble_store_metadata_cache_test.go
// version: 1.0.0
// guid: 6d2a8f41-3b07-4c95-a1e6-9f5c0b7d3e28
// last-edited: 2026-10-10

package database

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A retitle or re-credit keeps the book's cache row and marks it Stale
// (stageCacheStaleStamp) instead of deleting it, which lost 1,180 candidate
// rows on 2026-10-06. The apply gate refuses a Stale row and the candidate
// fetch refetches it.

// seedFingerprintedRow stores a one-candidate row whose SearchFingerprint is fp.
func seedFingerprintedRow(t *testing.T, s *PebbleStore, bookID, fp string) []json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"title": "Title 000001", "author": "Author 07", "score": 0.9})
	require.NoError(t, err)
	cands := []json.RawMessage{raw}
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{
		BookID: bookID, FetchedAt: time.Now().UTC().Truncate(time.Second), Candidates: cands,
		SourceHash: "h", SearchFingerprint: fp,
	}))
	return cands
}

func rawCacheRow(t *testing.T, s *PebbleStore, bookID string) []byte {
	t.Helper()
	val, closer, err := s.db.Get(metadataCacheKey(bookID))
	require.NoError(t, err)
	defer closer.Close()
	return append([]byte(nil), val...)
}

func TestUpdateBook_RetitleKeepsCacheRowAsStale(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000001", FilePath: "/lib/s/a.m4b", Format: "m4b"})
	require.NoError(t, err)
	cands := seedFingerprintedRow(t, s, b.ID, "fp-old")

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.Title = "Title 000002"; return nil })
	require.NoError(t, err)

	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.NotNil(t, entry, "the retitle must keep the row")
	require.True(t, entry.Stale)
	require.Equal(t, "fp-old", entry.StaleQuestionFP)
	require.Equal(t, "fp-old", entry.SearchFingerprint, "the row's own fingerprint is untouched")
	require.Len(t, entry.Candidates, len(cands))
	for i := range cands {
		require.Equal(t, string(cands[i]), string(entry.Candidates[i]), "candidates are byte-identical")
	}
}

func TestUpdateBook_RetitleTwiceKeepsOldestFingerprint(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000001", FilePath: "/lib/s/b.m4b", Format: "m4b"})
	require.NoError(t, err)
	seedFingerprintedRow(t, s, b.ID, "fp-oldest")

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.Title = "Title 000002"; return nil })
	require.NoError(t, err)
	g := s.MetadataCacheGeneration()
	first := rawCacheRow(t, s, b.ID)
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.Title = "Title 000003"; return nil })
	require.NoError(t, err)

	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.True(t, entry.Stale)
	require.Equal(t, "fp-oldest", entry.StaleQuestionFP, "an already-stale row keeps the oldest fingerprint")
	require.Equal(t, first, rawCacheRow(t, s, b.ID), "an already-stale row is not rewritten")
	require.Equal(t, g, s.MetadataCacheGeneration(), "nothing changed, so the generation does not move")
}

func TestUpdateBook_RelinkToSameNameAuthorDoesNotStale(t *testing.T) {
	s := newKeepCacheStore(t)
	a1, err := s.CreateAuthor("Author 07")
	require.NoError(t, err)
	a2, err := s.CreateAuthor("Author 07 Duplicate")
	require.NoError(t, err)
	require.NoError(t, s.UpdateAuthorName(a2.ID, "Author 07 "))
	b, err := s.CreateBook(&Book{Title: "Title 000001", FilePath: "/lib/s/c.m4b", Format: "m4b", AuthorID: &a1.ID})
	require.NoError(t, err)
	seedFingerprintedRow(t, s, b.ID, "fp-old")
	before := rawCacheRow(t, s, b.ID)
	g := s.MetadataCacheGeneration()

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.AuthorID = &a2.ID; return nil })
	require.NoError(t, err)

	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.False(t, entry.Stale, "a relink to a same-named author asks the same question")
	require.Equal(t, before, rawCacheRow(t, s, b.ID))
	require.Equal(t, g, s.MetadataCacheGeneration())
}

func TestUpdateBook_RetitleAndASINReplaceStampsBothInOneWrite(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000001", FilePath: "/lib/s/d.m4b", Format: "m4b", ASIN: strp("B00OLDASIN")})
	require.NoError(t, err)
	seedFingerprintedRow(t, s, b.ID, "fp-old")
	g := s.MetadataCacheGeneration()

	_, err = s.ModifyBook(b.ID, func(cur *Book) error {
		cur.Title = "Title 000002"
		cur.ASIN = strp("B00NEWASIN")
		return nil
	})
	require.NoError(t, err)

	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.True(t, entry.Stale)
	require.Equal(t, "fp-old", entry.StaleQuestionFP)
	require.Equal(t, "B00OLDASIN", entry.FetchedForASIN, "the replaced ASIN is stamped in the same rewrite")
	require.Equal(t, g+1, s.MetadataCacheGeneration(), "one write moves the generation once")
}

func TestUpdateBook_RetitleWithNoCacheRowWritesNoTombstone(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000001", FilePath: "/lib/s/e.m4b", Format: "m4b"})
	require.NoError(t, err)
	g := s.MetadataCacheGeneration()

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.Title = "Title 000002"; return nil })
	require.NoError(t, err)

	_, closer, gerr := s.db.Get(metadataCacheKey(b.ID))
	if gerr == nil {
		_ = closer.Close()
	}
	require.Error(t, gerr, "no key may exist afterwards")
	require.ErrorContains(t, gerr, "not found")
	require.Equal(t, g, s.MetadataCacheGeneration(), "nothing was written, so the generation does not move")
	rows, err := s.ListMetadataCacheKeys()
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestUpdateBook_RetitleBumpsCacheGeneration(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000001", FilePath: "/lib/s/f.m4b", Format: "m4b"})
	require.NoError(t, err)
	seedFingerprintedRow(t, s, b.ID, "fp-old")
	g := s.MetadataCacheGeneration()

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.Title = "Title 000002"; return nil })
	require.NoError(t, err)

	require.Equal(t, g+1, s.MetadataCacheGeneration())
	ids, _, ok := s.MetadataCacheChangedSince(g)
	require.True(t, ok)
	require.Equal(t, []string{b.ID}, ids, "the change log names the retitled book")
	rows, err := s.ListMetadataCacheKeys()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Stale, "the summary index sees the row as stale")
}

func TestUpdateBook_Retitle1000KeepsAllRowsStale(t *testing.T) {
	s := newKeepCacheStore(t)
	const n = 1000
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		b, err := s.CreateBook(&Book{Title: fmt.Sprintf("Title %06d", i), FilePath: fmt.Sprintf("/lib/s/%06d.m4b", i), Format: "m4b"})
		require.NoError(t, err)
		seedFingerprintedRow(t, s, b.ID, fmt.Sprintf("fp-%06d", i))
		ids = append(ids, b.ID)
	}
	for i, id := range ids {
		title := fmt.Sprintf("Retitled %06d", i)
		_, err := s.ModifyBook(id, func(cur *Book) error { cur.Title = title; return nil })
		require.NoError(t, err)
	}
	stale, deleted := 0, 0
	for i, id := range ids {
		entry, err := s.GetMetadataCache(id)
		require.NoError(t, err)
		switch {
		case entry == nil:
			deleted++
		case entry.Stale && entry.StaleQuestionFP == fmt.Sprintf("fp-%06d", i) && len(entry.Candidates) == 1:
			stale++
		}
	}
	t.Logf("stale rows=%d deleted=%d", stale, deleted)
	require.Equal(t, n, stale)
	require.Zero(t, deleted)
}

func TestUpdateBook_NonIdentityWriteLeavesRowUntouched(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "Title 000001", FilePath: "/lib/s/g.m4b", Format: "m4b"})
	require.NoError(t, err)
	seedFingerprintedRow(t, s, b.ID, "fp-old")
	before := rawCacheRow(t, s, b.ID)
	g := s.MetadataCacheGeneration()

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.Format = "mp3"; cur.Narrator = strp("Narrator 01"); return nil })
	require.NoError(t, err)

	entry, err := s.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.False(t, entry.Stale)
	require.Equal(t, before, rawCacheRow(t, s, b.ID), "the row is byte-identical")
	require.Equal(t, g, s.MetadataCacheGeneration())
}
