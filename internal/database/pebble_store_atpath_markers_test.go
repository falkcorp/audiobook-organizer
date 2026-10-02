// file: internal/database/pebble_store_atpath_markers_test.go
// version: 1.0.0
// guid: 4b7e0c2d-9f13-4a68-b5d1-7e2c8a90f346
// last-edited: 2026-10-02

package database

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// batchDeletes captures the keys every UpdateBook/DeleteBook batch deletes,
// keyed by op ("update"/"delete"), for the duration of the test.
func batchDeletes(t *testing.T) func(op string) [][]byte {
	t.Helper()
	var mu sync.Mutex
	got := map[string][][]byte{}
	bookWriteBatchPreCommitHook = func(op, _ string, b *pebble.Batch) {
		r := b.Reader()
		for {
			kind, ukey, _, ok, err := r.Next()
			if err != nil {
				t.Errorf("read batch: %v", err)
				return
			}
			if !ok {
				return
			}
			if kind == pebble.InternalKeyKindDelete || kind == pebble.InternalKeyKindSingleDelete {
				mu.Lock()
				got[op] = append(got[op], bytes.Clone(ukey))
				mu.Unlock()
			}
		}
	}
	t.Cleanup(func() { bookWriteBatchPreCommitHook = nil })
	return func(op string) [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return got[op]
	}
}

func deletedWithPrefix(keys [][]byte, prefix string) []string {
	var out []string
	for _, k := range keys {
		if strings.HasPrefix(string(k), prefix) {
			out = append(out, string(k))
		}
	}
	return out
}

// TestUpdateBook_NoMarkerWritesNoMarkerDelete is the 2026-10-02 regression:
// an UpdateBook (and a DeleteBook) on a book with no undecodable marker must
// not stage a Delete for the marker key, and an identity change on a book
// with no metadata cache entry must not stage one for the cache key. Each such
// Delete was a tombstone that every later range scan of the family walked.
func TestUpdateBook_NoMarkerWritesNoMarkerDelete(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	deletes := batchDeletes(t)

	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/lib/a/t.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	asin := "B000TEST01"
	b.ASIN = &asin // identity change: would have deleted metadata_cache:<id>
	b.Title = "t2"
	if _, err := s.UpdateBook(b.ID, b); err != nil {
		t.Fatal(err)
	}
	if got := deletedWithPrefix(deletes("update"), bookAtPathUndecodablePrefix); len(got) != 0 {
		t.Fatalf("UpdateBook staged marker deletes %q for a book with no marker", got)
	}
	if got := deletedWithPrefix(deletes("update"), metadataCacheKeyPrefix); len(got) != 0 {
		t.Fatalf("UpdateBook staged cache deletes %q for a book with no cache entry", got)
	}

	if err := s.DeleteBook(b.ID); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{bookAtPathUndecodablePrefix, metadataCacheKeyPrefix, bookSigKeyPrefix,
		embVecPfx, "chapters:", "book_authors:", "book_narrators:", "user_tag:book:", "alt_titles:book:"} {
		if got := deletedWithPrefix(deletes("delete"), prefix); len(got) != 0 {
			t.Errorf("DeleteBook staged %q deletes %q for keys that do not exist", prefix, got)
		}
	}
}

// TestUpdateBook_ExistingCacheEntryStillDropped: the probe must not skip a
// delete that is owed.
func TestUpdateBook_ExistingCacheEntryStillDropped(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/lib/a/t.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutMetadataCache(&MetadataCandidateCache{BookID: b.ID}); err != nil {
		t.Fatal(err)
	}
	asin := "B000TEST02"
	b.ASIN = &asin
	if _, err := s.UpdateBook(b.ID, b); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetMetadataCache(b.ID); err != nil || got != nil {
		t.Fatalf("cache entry survived an identity change: %v %v", got, err)
	}
}

// TestUpdateBook_MarkerStillCleared: a book that does carry a marker gets the
// Delete, the marker leaves disk, and the id leaves the set.
func TestUpdateBook_MarkerStillCleared(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	deletes := batchDeletes(t)

	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/lib/a/t.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	plantUndecodableMarker(t, s, b.ID)
	if _, err := s.UpdateBook(b.ID, b); err != nil {
		t.Fatal(err)
	}
	if got := deletedWithPrefix(deletes("update"), bookAtPathUndecodablePrefix); len(got) != 1 {
		t.Fatalf("marker deletes = %q, want exactly one", got)
	}
	if hasUndecodableMarker(t, s, b.ID) {
		t.Fatal("marker still on disk after UpdateBook")
	}
	if n := s.undecodableMarkerCount(); n != 0 {
		t.Fatalf("set size = %d after the delete committed, want 0", n)
	}
}

// TestUndecodableMarker_EmptySetReaderDoesNoRangeScan: with the set empty the
// readers must not look at the marker family at all. A marker planted on disk
// BEHIND the set's back (no production writer can do this) is therefore
// invisible: the lookups succeed instead of failing closed. Once the set knows
// about it, the same lookups fail closed again.
func TestUndecodableMarker_EmptySetReaderDoesNoRangeScan(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	if _, err := s.CreateBook(&Book{Title: "t", FilePath: "/lib/a/t.m4b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackfillBookAtPathIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	setRawBookRow(t, s, "01BADROW", "{not json")
	if err := s.db.Set(bookAtPathUndecodableKey("01BADROW"), []byte{}, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if n := s.undecodableMarkerCount(); n != 0 {
		t.Fatalf("set size = %d, want 0", n)
	}
	if _, err := s.LiveBookPathsUnderDir("/lib"); err != nil {
		t.Fatalf("empty-set LiveBookPathsUnderDir read the marker family: %v", err)
	}
	if _, err := s.LiveBookIDsAtPath("/lib/a/t.m4b"); err != nil {
		t.Fatalf("empty-set LiveBookIDsAtPath read the marker family: %v", err)
	}

	if err := s.noteUndecodableMarker("01BADROW"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LiveBookPathsUnderDir("/lib"); err == nil {
		t.Fatal("LiveBookPathsUnderDir must fail closed on a known undecodable row")
	}
	if _, err := s.LiveBookIDsAtPath("/lib/a/t.m4b"); err == nil {
		t.Fatal("LiveBookIDsAtPath must fail closed on a known undecodable row")
	}
}

// TestUndecodableMarker_BackfillKeepsSetExact: the backfill adds every marker
// it writes, and a rebuild drops every id whose marker it range-deleted and
// did not re-add.
func TestUndecodableMarker_BackfillKeepsSetExact(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	seedUndecodableAfterBackfill(t, s)
	if n := s.undecodableMarkerCount(); n != 1 {
		t.Fatalf("set size after backfill = %d, want 1", n)
	}
	if _, ok := s.undecodableMarkerMayExist("01BADROW"); !ok {
		t.Fatal("backfill marker missing from the set")
	}

	// A stale id, then repair the bad row out of band and rebuild: both go.
	if err := s.noteUndecodableMarker("01STALE"); err != nil {
		t.Fatal(err)
	}
	setRawBookRow(t, s, "01BADROW", `{"id":"01BADROW","title":"fixed","file_path":"/fixed"}`)
	if _, err := s.RebuildBookAtPathIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := s.undecodableMarkerCount(); n != 0 {
		t.Fatalf("set size after rebuild = %d, want 0", n)
	}
	if hasUndecodableMarker(t, s, "01BADROW") {
		t.Fatal("rebuild left the marker on disk")
	}
}

// TestUndecodableMarker_ReAddBeatsStaleForget: an UpdateBook that looked
// before a backfill worker re-added the id must not drop it after its own
// commit, because the worker's marker may have committed after the delete.
func TestUndecodableMarker_ReAddBeatsStaleForget(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	plantUndecodableMarker(t, s, "01X")
	gen, ok := s.undecodableMarkerMayExist("01X")
	if !ok {
		t.Fatal("planted id not in the set")
	}
	if err := s.noteUndecodableMarker("01X"); err != nil { // worker re-adds
		t.Fatal(err)
	}
	s.forgetUndecodableMarker("01X", gen) // the earlier writer's post-commit forget
	if _, ok := s.undecodableMarkerMayExist("01X"); !ok {
		t.Fatal("a stale forget dropped a re-added id")
	}
	gen2, _ := s.undecodableMarkerMayExist("01X")
	s.forgetUndecodableMarker("01X", gen2)
	if _, ok := s.undecodableMarkerMayExist("01X"); ok {
		t.Fatal("a current forget did not drop the id")
	}
}

// TestUndecodableMarker_ZeroValueStoreLoadsLazily: a store built without
// newPebbleStore must treat the set as unknown and load it, never as empty.
func TestUndecodableMarker_ZeroValueStoreLoadsLazily(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	if err := s.db.Set(bookAtPathUndecodableKey("01ONDISK"), []byte{}, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	bare := &PebbleStore{db: s.db}
	if _, ok := bare.undecodableMarkerMayExist("01ONDISK"); !ok {
		t.Fatal("lazy load missed an on-disk marker")
	}
}
