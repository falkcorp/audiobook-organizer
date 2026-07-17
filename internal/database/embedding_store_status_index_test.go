// file: internal/database/embedding_store_status_index_test.go
// version: 1.1.0
// guid: 5a2c7e1f-9d3b-4a6e-8c1d-2f4b6a8e0c3d
// last-edited: 2026-07-17

package database

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusIndexRows returns every "dedup:s:" key currently stored, for direct
// assertion on the secondary index contents (bypassing ListCandidates so the
// maintenance tests can verify the index itself, not just its read path).
func statusIndexRows(t *testing.T, s *EmbeddingStore) []string {
	t.Helper()
	prefix := []byte(dedupStatusIdxPfx)
	upper := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	require.NoError(t, err)
	defer iter.Close()

	var rows []string
	for iter.First(); iter.Valid(); iter.Next() {
		rows = append(rows, string(iter.Key()))
	}
	require.NoError(t, iter.Error())
	return rows
}

// seedStatusIndexFixture writes a small, deterministic set of candidates
// spanning multiple statuses/layers/similarities, shared by the parity and
// fallback tests so both exercise the identical dataset.
func seedStatusIndexFixture(t *testing.T, s *EmbeddingStore) {
	t.Helper()
	fixture := []DedupCandidate{
		{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Similarity: floatPtr(0.95), Status: "pending"},
		{EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "embedding", Similarity: floatPtr(0.80), Status: "pending"},
		{EntityType: "book", EntityAID: "b5", EntityBID: "b6", Layer: "exact", Similarity: floatPtr(0.99), Status: "pending"},
		{EntityType: "book", EntityAID: "b7", EntityBID: "b8", Layer: "embedding", Similarity: floatPtr(0.70), Status: "merged"},
		{EntityType: "author", EntityAID: "a1", EntityBID: "a2", Layer: "metadata", Similarity: floatPtr(0.60), Status: "pending"},
		{EntityType: "book", EntityAID: "b9", EntityBID: "b10", Layer: "embedding", Similarity: floatPtr(0.85), Status: "dismissed"},
	}
	for _, c := range fixture {
		require.NoError(t, s.UpsertCandidate(c))
	}
}

// (a) Parity: on the same fixture, the indexed read path (flag set) and the
// full-scan read path (flag unset) must return identical rows/totals/order
// for a status-filtered query. This is the anti-over-suppression proof: no
// row may silently disappear once the index is active.
func TestCandidateStatusIndex_Parity(t *testing.T) {
	store := newTestEmbeddingStore(t)
	seedStatusIndexFixture(t, store)

	filter := CandidateFilter{Status: "pending"}

	// Flag unset — full scan.
	assert.False(t, store.IsCandidateStatusIndexBuilt())
	fullScanResults, fullScanTotal, err := store.ListCandidates(filter)
	require.NoError(t, err)

	// Now flip the flag on (as the backfill op would after a clean run) and
	// re-run the identical query — the index rows are already present
	// because every write path (UpsertCandidate here) maintains them inline.
	require.NoError(t, store.SetCandidateStatusIndexBuilt())
	require.True(t, store.IsCandidateStatusIndexBuilt())
	indexedResults, indexedTotal, err := store.ListCandidates(filter)
	require.NoError(t, err)

	require.Equal(t, fullScanTotal, indexedTotal, "totals must match between full-scan and indexed reads")
	require.Len(t, indexedResults, len(fullScanResults))
	for i := range fullScanResults {
		assert.Equal(t, fullScanResults[i].ID, indexedResults[i].ID, "row order/identity must match at position %d", i)
		assert.Equal(t, fullScanResults[i].EntityAID, indexedResults[i].EntityAID)
		assert.Equal(t, fullScanResults[i].EntityBID, indexedResults[i].EntityBID)
		assert.Equal(t, fullScanResults[i].Status, indexedResults[i].Status)
	}

	// Sanity: exactly the 4 "pending" rows from the fixture (3 book + 1 author).
	assert.Equal(t, 4, fullScanTotal)
}

// (a-continued) Parity must also hold when additional filters (EntityType,
// Layer, similarity range, Band) are combined with the status filter — the
// indexed path applies these as a Go-side post-filter after the prefix scan,
// exactly like the full-scan path does inline.
func TestCandidateStatusIndex_Parity_WithAdditionalFilters(t *testing.T) {
	store := newTestEmbeddingStore(t)
	seedStatusIndexFixture(t, store)

	filter := CandidateFilter{Status: "pending", EntityType: "book", Layer: "embedding"}

	fullScanResults, fullScanTotal, err := store.ListCandidates(filter)
	require.NoError(t, err)

	require.NoError(t, store.SetCandidateStatusIndexBuilt())
	indexedResults, indexedTotal, err := store.ListCandidates(filter)
	require.NoError(t, err)

	assert.Equal(t, fullScanTotal, indexedTotal)
	assert.Equal(t, len(fullScanResults), len(indexedResults))
	// Expect exactly the 2 pending/book/embedding rows (b1/b2 and b3/b4) —
	// the exact-layer pending row and the author row must be excluded.
	assert.Equal(t, 2, indexedTotal)
	for _, c := range indexedResults {
		assert.Equal(t, "book", c.EntityType)
		assert.Equal(t, "embedding", c.Layer)
		assert.Equal(t, "pending", c.Status)
	}
}

// (b) Maintenance: create → status change → delete leaves exactly the
// expected "dedup:s:" rows at every step.
func TestCandidateStatusIndex_Maintenance_CreateChangeDelete(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Create.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2",
		Layer: "embedding", Similarity: floatPtr(0.9), Status: "pending",
	}))
	results, _, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, results, 1)
	id := results[0].ID

	rows := statusIndexRows(t, store)
	require.Len(t, rows, 1, "exactly one status-index row after create")
	assert.Equal(t, string(dedupStatusIdxKey("pending", id)), rows[0])

	// Status change via UpdateCandidateStatus.
	require.NoError(t, store.UpdateCandidateStatus(id, "merged"))
	rows = statusIndexRows(t, store)
	require.Len(t, rows, 1, "old-status row must be replaced, not accumulated")
	assert.Equal(t, string(dedupStatusIdxKey("merged", id)), rows[0])

	// Status change via UpsertCandidateNew's update branch (existing pair).
	_, isNew, err := store.UpsertCandidateNew(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2",
		Layer: "embedding", Similarity: floatPtr(0.9), Status: "dismissed",
	})
	require.NoError(t, err)
	assert.False(t, isNew, "same pair must update in place, not create a new row")
	rows = statusIndexRows(t, store)
	require.Len(t, rows, 1)
	assert.Equal(t, string(dedupStatusIdxKey("dismissed", id)), rows[0])

	// Delete.
	require.NoError(t, store.DeleteCandidate(id))
	rows = statusIndexRows(t, store)
	assert.Empty(t, rows, "delete must remove the status-index row")
}

// (b-continued) A second candidate must not disturb the first's index row,
// and updating one candidate's status must leave unrelated rows untouched.
func TestCandidateStatusIndex_Maintenance_MultipleCandidatesIsolated(t *testing.T) {
	store := newTestEmbeddingStore(t)

	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending",
	}))
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "embedding", Status: "pending",
	}))

	results, _, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, results, 2)

	rows := statusIndexRows(t, store)
	require.Len(t, rows, 2)

	// Move the first to merged; the second must remain untouched.
	var firstID int64
	for _, c := range results {
		if c.EntityAID == "b1" {
			firstID = c.ID
		}
	}
	require.NoError(t, store.UpdateCandidateStatus(firstID, "merged"))

	rows = statusIndexRows(t, store)
	require.Len(t, rows, 2, "total row count unchanged — one moved, one untouched")

	pending, _, err := store.ListCandidates(CandidateFilter{Status: "pending"})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "b3", pending[0].EntityAID)

	merged, _, err := store.ListCandidates(CandidateFilter{Status: "merged"})
	require.NoError(t, err)
	require.Len(t, merged, 1)
	assert.Equal(t, "b1", merged[0].EntityAID)
}

// (c) A dangling index row (dedup:s: entry whose dedup:r: record is missing —
// e.g. a delete that raced the read, or a hand-corrupted store) is skipped
// silently by the indexed read path, never surfaced as an error.
func TestCandidateStatusIndex_DanglingRowSkippedSilently(t *testing.T) {
	store := newTestEmbeddingStore(t)

	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending",
	}))
	results, _, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Manually inject a dangling index row pointing at a non-existent candidate ID.
	const danglingID = int64(999999)
	require.NoError(t, store.db.Set(dedupStatusIdxKey("pending", danglingID), nil, pebble.Sync))

	require.NoError(t, store.SetCandidateStatusIndexBuilt())

	got, total, err := store.ListCandidates(CandidateFilter{Status: "pending"})
	require.NoError(t, err, "a dangling index row must never surface as an error")
	require.Len(t, got, 1, "only the real candidate should be returned")
	assert.Equal(t, 1, total)
	assert.Equal(t, "b1", got[0].EntityAID)
}

// (d) Flag-unset fallback: with the built-flag unset, ListCandidates must
// still honor every CandidateFilter field exactly as it did before the
// index existed — the fallback path is byte-for-byte the original full scan.
func TestCandidateStatusIndex_FlagUnsetFallback_HonorsAllFilters(t *testing.T) {
	store := newTestEmbeddingStore(t)
	seedStatusIndexFixture(t, store)
	require.False(t, store.IsCandidateStatusIndexBuilt())

	minSim := 0.75
	maxSim := 0.90
	results, total, err := store.ListCandidates(CandidateFilter{
		Status:        "pending",
		EntityType:    "book",
		Layer:         "embedding",
		MinSimilarity: &minSim,
		MaxSimilarity: &maxSim,
	})
	require.NoError(t, err)
	// Only b3/b4 (similarity 0.80, pending, book, embedding) satisfies every filter.
	require.Equal(t, 1, total)
	require.Len(t, results, 1)
	assert.Equal(t, "b3", results[0].EntityAID)
	assert.Equal(t, "b4", results[0].EntityBID)
}

// entityIndexRows returns every "dedup:e:" key currently stored, for direct
// assertion on the entity secondary-index contents.
func entityIndexRows(t *testing.T, s *EmbeddingStore) []string {
	t.Helper()
	prefix := []byte(dedupEntityPfx)
	upper := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	require.NoError(t, err)
	defer iter.Close()

	var rows []string
	for iter.First(); iter.Valid(); iter.Next() {
		rows = append(rows, string(iter.Key()))
	}
	require.NoError(t, iter.Error())
	return rows
}

// (F3) MarkCandidatesAsMergedForEntity must move each affected candidate's
// "dedup:s:" row from its old status to "merged" in the same batch as the
// record rewrite — and must leave the "dedup:e:" entity rows intact (the
// candidate still references both entities; only its status changed).
func TestCandidateStatusIndex_MarkCandidatesAsMergedForEntity_MaintainsIndex(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Two pending candidates referencing bX (one on each side), plus one
	// unrelated pending pair that must not be touched.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "bX", EntityBID: "bY", Layer: "embedding", Status: "pending",
	}))
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "bW", EntityBID: "bX", Layer: "embedding", Status: "pending",
	}))
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "p1", EntityBID: "p2", Layer: "embedding", Status: "pending",
	}))

	all, _, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	idsByPair := map[string]int64{}
	for _, c := range all {
		idsByPair[c.EntityAID+":"+c.EntityBID] = c.ID
	}
	entityRowsBefore := entityIndexRows(t, store)
	require.Len(t, entityRowsBefore, 6, "2 entity rows per candidate")

	n, err := store.MarkCandidatesAsMergedForEntity("book", "bX")
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	rows := statusIndexRows(t, store)
	require.Len(t, rows, 3, "one status row per candidate — old rows replaced, not accumulated")
	assert.Contains(t, rows, string(dedupStatusIdxKey("merged", idsByPair["bX:bY"])))
	assert.Contains(t, rows, string(dedupStatusIdxKey("merged", idsByPair["bW:bX"])))
	assert.Contains(t, rows, string(dedupStatusIdxKey("pending", idsByPair["p1:p2"])))
	assert.NotContains(t, rows, string(dedupStatusIdxKey("pending", idsByPair["bX:bY"])))
	assert.NotContains(t, rows, string(dedupStatusIdxKey("pending", idsByPair["bW:bX"])))

	// Entity index untouched — the candidates still exist and reference the
	// same entities.
	assert.ElementsMatch(t, entityRowsBefore, entityIndexRows(t, store))

	// The indexed read path must now see the transition too.
	require.NoError(t, store.SetCandidateStatusIndexBuilt())
	pending, _, err := store.ListCandidates(CandidateFilter{Status: "pending"})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "p1", pending[0].EntityAID)
	merged, _, err := store.ListCandidates(CandidateFilter{Status: "merged"})
	require.NoError(t, err)
	assert.Len(t, merged, 2)
}

// (F4a) RemoveCandidatesForEntity must clean ALL four key classes for each
// deleted candidate (dedup:r:, dedup:p:, dedup:e: both sides, dedup:s:),
// mirroring DeleteCandidate — not just the record + pair keys.
func TestCandidateIndexes_RemoveCandidatesForEntity_CleansAllIndexRows(t *testing.T) {
	store := newTestEmbeddingStore(t)

	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending",
	}))
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "embedding", Status: "merged",
	}))

	n, err := store.RemoveCandidatesForEntity("book", "b1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Status index: only the survivor's row remains.
	survivors, _, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, survivors, 1)
	rows := statusIndexRows(t, store)
	require.Len(t, rows, 1, "deleted candidate's status-index row must be removed")
	assert.Equal(t, string(dedupStatusIdxKey("merged", survivors[0].ID)), rows[0])

	// Entity index: b1/b2 rows gone, b3/b4 rows intact.
	entityRows := entityIndexRows(t, store)
	require.Len(t, entityRows, 2, "deleted candidate's entity-index rows must be removed")
	gone, err := store.ListCandidatesForEntity("book", "b2", "")
	require.NoError(t, err)
	assert.Empty(t, gone, "entity index must not point at the deleted candidate")
}

// (F4b) CanonicalizeCandidates' duplicate-delete branch must clean the
// non-canonical row's dedup:e: and dedup:s: rows too, not just dedup:r: +
// dedup:p: — otherwise every canonicalized duplicate leaks two entity rows
// and one status row forever.
func TestCandidateIndexes_CanonicalizeDuplicateDelete_CleansAllIndexRows(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Canonical row via the normal path (h < w).
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "hello", EntityBID: "world", Layer: "embedding", Status: "pending",
	}))
	all, _, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, all, 1)
	canonID := all[0].ID

	// Non-canonical duplicate written raw (bypasses upsert canonicalization),
	// WITH its secondary-index rows present — simulating a fully indexed
	// pre-canonicalization row.
	const dupID = int64(7777)
	rec := candRec{
		EntityType: "book", EntityAID: "world", EntityBID: "hello",
		Layer: "exact", Status: "pending",
		CreatedAt: time.Now().UnixNano(), UpdatedAt: time.Now().UnixNano(),
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, store.db.Set(dedupRecKey(dupID), data, pebble.Sync))
	require.NoError(t, store.db.Set(dedupPairKey("book", "world", "hello"), []byte(fmt.Sprintf("%016x", dupID)), pebble.Sync))
	require.NoError(t, store.db.Set(dedupEntityKey("book", "world", dupID), nil, pebble.Sync))
	require.NoError(t, store.db.Set(dedupEntityKey("book", "hello", dupID), nil, pebble.Sync))
	require.NoError(t, store.db.Set(dedupStatusIdxKey("pending", dupID), nil, pebble.Sync))

	rewritten, deleted, err := store.CanonicalizeCandidates()
	require.NoError(t, err)
	assert.Equal(t, 0, rewritten)
	assert.Equal(t, 1, deleted)

	rows := statusIndexRows(t, store)
	require.Len(t, rows, 1, "duplicate's status-index row must be removed")
	assert.Equal(t, string(dedupStatusIdxKey("pending", canonID)), rows[0])

	entityRows := entityIndexRows(t, store)
	require.Len(t, entityRows, 2, "duplicate's entity-index rows must be removed")
	assert.Contains(t, entityRows, string(dedupEntityKey("book", "hello", canonID)))
	assert.Contains(t, entityRows, string(dedupEntityKey("book", "world", canonID)))
}

// The backfill write path (WriteCandidateStatusIndexRow, used by the
// dedup.build-candidate-status-index op) writes the same key shape as the
// inline write-path maintenance and is idempotent.
func TestCandidateStatusIndex_WriteCandidateStatusIndexRow(t *testing.T) {
	store := newTestEmbeddingStore(t)

	require.NoError(t, store.WriteCandidateStatusIndexRow(42, "pending"))
	rows := statusIndexRows(t, store)
	require.Len(t, rows, 1)
	assert.Equal(t, string(dedupStatusIdxKey("pending", 42)), rows[0])

	// Idempotent re-write.
	require.NoError(t, store.WriteCandidateStatusIndexRow(42, "pending"))
	rows = statusIndexRows(t, store)
	require.Len(t, rows, 1)

	// Blank status is a no-op guard, not an error.
	require.NoError(t, store.WriteCandidateStatusIndexRow(43, ""))
	rows = statusIndexRows(t, store)
	require.Len(t, rows, 1, "blank status must not write a row")
}
