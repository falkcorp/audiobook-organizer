// file: internal/database/deletebook_dedup_cascade_test.go
// version: 1.0.0
// guid: 7c2a9e14-6b83-4f51-9d27-3ea5c8b04f16
// last-edited: 2026-08-19

package database

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// cascadeKeyPresent reports whether a raw key exists in the DB.
func cascadeKeyPresent(t *testing.T, db *pebble.DB, key []byte) bool {
	t.Helper()
	_, closer, err := db.Get(key)
	if err == pebble.ErrNotFound {
		return false
	}
	require.NoError(t, err)
	closer.Close()
	return true
}

func mkCascadeBook(t *testing.T, s *PebbleStore, id, title string) {
	t.Helper()
	_, err := s.CreateBook(&Book{ID: id, Title: title, FilePath: "/tmp/" + id + ".m4b"})
	require.NoError(t, err)
}

// TestDeleteBook_CascadesToDedupCandidates locks in that hard-deleting a book
// tears down every dedup candidate referencing it, on EITHER side of the pair.
//
// Before this cascade existed, only one of DeleteBook's 16 call sites cleaned up
// after itself, and `dedup.breakdown-backfill` reported skipped_no_book=2504 —
// candidates pointing at books that no longer exist. Such a row can never be
// actioned (Merge 500s "book not found") and can never be re-scored, because
// every producer iterates live books only, so it sits in the pending queue
// forever.
func TestDeleteBook_CascadesToDedupCandidates(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir() + "/db")
	require.NoError(t, err)
	defer store.Close()
	db := store.DB()
	emb := NewEmbeddingStore(db)

	mkCascadeBook(t, store, "bA", "Doomed")
	mkCascadeBook(t, store, "bB", "Other")
	mkCascadeBook(t, store, "bC", "Bystander")
	mkCascadeBook(t, store, "bD", "Bystander Two")

	sim := 0.9
	// bA appears as entity_a here...
	idA, _, err := emb.UpsertCandidateNew(DedupCandidate{
		EntityType: "book", EntityAID: "bA", EntityBID: "bB",
		Layer: "embedding", Similarity: &sim,
	})
	require.NoError(t, err)
	// ...and as entity_b here. Both must be torn down: the row is indexed under
	// both sides, and scanning by the deleted book's ID finds only one of them
	// in the key — the other has to come from the record.
	idB, _, err := emb.UpsertCandidateNew(DedupCandidate{
		EntityType: "book", EntityAID: "bC", EntityBID: "bA",
		Layer: "embedding", Similarity: &sim,
	})
	require.NoError(t, err)
	// Planted control: touches neither bA nor anything being deleted. If the
	// cascade over-reaches, this dies and the test catches it. Without this a
	// "delete everything" bug would pass every other assertion here.
	idCtl, _, err := emb.UpsertCandidateNew(DedupCandidate{
		EntityType: "book", EntityAID: "bC", EntityBID: "bD",
		Layer: "embedding", Similarity: &sim,
	})
	require.NoError(t, err)

	require.True(t, cascadeKeyPresent(t, db, dedupRecKey(idA)), "precondition: candidate A written")
	require.True(t, cascadeKeyPresent(t, db, dedupRecKey(idB)), "precondition: candidate B written")

	require.NoError(t, store.DeleteBook("bA"))

	// Both candidates referencing bA are gone, in every key that named them.
	for _, tc := range []struct {
		id       int64
		aID, bID string
	}{
		{idA, "bA", "bB"},
		{idB, "bC", "bA"},
	} {
		require.False(t, cascadeKeyPresent(t, db, dedupRecKey(tc.id)), "record key %d", tc.id)
		require.False(t, cascadeKeyPresent(t, db, dedupPairKey("book", tc.aID, tc.bID)), "pair key %d", tc.id)
		require.False(t, cascadeKeyPresent(t, db, dedupEntityKey("book", tc.aID, tc.id)), "entity A key %d", tc.id)
		require.False(t, cascadeKeyPresent(t, db, dedupEntityKey("book", tc.bID, tc.id)), "entity B key %d", tc.id)
	}

	// The control survives intact.
	require.True(t, cascadeKeyPresent(t, db, dedupRecKey(idCtl)), "unrelated candidate must survive")
	require.True(t, cascadeKeyPresent(t, db, dedupEntityKey("book", "bC", idCtl)), "unrelated entity index must survive")

	// And it is still readable through the normal reader, not just as raw keys.
	got, err := emb.ListCandidatesForEntity("book", "bC", "")
	require.NoError(t, err)
	require.Len(t, got, 1, "bC should retain exactly its one unrelated candidate")
	require.Equal(t, idCtl, got[0].ID)
}

// TestDeleteBook_CascadeRemovesStaleEntityIndexEntry covers the trap that
// ListCandidatesForEntity deliberately tolerates: an entity-index entry whose
// record is already gone. A reader may skip it, but teardown must not — skipping
// converts a candidate orphan into a stale *index* orphan, which is strictly
// worse because no later pass ever revisits it.
func TestDeleteBook_CascadeRemovesStaleEntityIndexEntry(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir() + "/db")
	require.NoError(t, err)
	defer store.Close()
	db := store.DB()

	mkCascadeBook(t, store, "bA", "Doomed")

	// Hand-write an index entry pointing at a candidate record that does not
	// exist, exactly as a partially-torn-down row would look.
	staleID := int64(424242)
	staleKey := dedupEntityKey("book", "bA", staleID)
	require.NoError(t, db.Set(staleKey, []byte{}, pebble.Sync))
	require.True(t, cascadeKeyPresent(t, db, staleKey), "precondition: stale index entry written")

	require.NoError(t, store.DeleteBook("bA"))

	require.False(t, cascadeKeyPresent(t, db, staleKey),
		"stale entity-index entry must be removed, not skipped")
}
