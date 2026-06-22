// file: internal/database/embedding_candidates_test.go
// version: 2.1.0
// guid: f3e2d1c0-b9a8-4765-8e7d-6f5c4b3a2190
// last-edited: 2026-06-22

package database

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// floatPtr is a test helper that returns a pointer to a float64 value.
func floatPtr(f float64) *float64 { return &f }

func TestDedupCandidates_CreateAndList(t *testing.T) {
	store := newTestEmbeddingStore(t)

	c1 := DedupCandidate{
		EntityType: "book",
		EntityAID:  "b1",
		EntityBID:  "b2",
		Layer:      "embedding",
		Similarity: floatPtr(0.95),
		Status:     "pending",
	}
	c2 := DedupCandidate{
		EntityType: "book",
		EntityAID:  "b3",
		EntityBID:  "b4",
		Layer:      "embedding",
		Similarity: floatPtr(0.80),
		Status:     "pending",
	}

	require.NoError(t, store.UpsertCandidate(c1))
	require.NoError(t, store.UpsertCandidate(c2))

	results, total, err := store.ListCandidates(CandidateFilter{
		EntityType: "book",
		Status:     "pending",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, results, 2)

	// Highest similarity should come first.
	assert.Equal(t, "b1", results[0].EntityAID)
	assert.Equal(t, "b3", results[1].EntityAID)
}

func TestDedupCandidates_UpdateStatus(t *testing.T) {
	store := newTestEmbeddingStore(t)

	c := DedupCandidate{
		EntityType: "book",
		EntityAID:  "b1",
		EntityBID:  "b2",
		Layer:      "embedding",
		Status:     "pending",
	}
	require.NoError(t, store.UpsertCandidate(c))

	// Retrieve so we have the auto-assigned ID.
	results, _, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	require.Len(t, results, 1)

	id := results[0].ID
	require.NoError(t, store.UpdateCandidateStatus(id, "merged"))

	got, err := store.GetCandidateByID(id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "merged", got.Status)
}

func TestDedupCandidates_UpsertIdempotent(t *testing.T) {
	store := newTestEmbeddingStore(t)

	base := DedupCandidate{
		EntityType: "book",
		EntityAID:  "b1",
		EntityBID:  "b2",
		Layer:      "embedding",
		Similarity: floatPtr(0.90),
		Status:     "pending",
	}
	require.NoError(t, store.UpsertCandidate(base))

	// Second upsert with updated similarity.
	updated := base
	updated.Similarity = floatPtr(0.99)
	require.NoError(t, store.UpsertCandidate(updated))

	results, total, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "upsert must not create a second row")
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Similarity)
	assert.InDelta(t, 0.99, *results[0].Similarity, 1e-9)
}

func TestDedupCandidates_Stats(t *testing.T) {
	store := newTestEmbeddingStore(t)

	candidates := []DedupCandidate{
		{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending"},
		{EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "embedding", Status: "merged"},
		{EntityType: "author", EntityAID: "a1", EntityBID: "a2", Layer: "metadata", Status: "pending"},
	}
	for _, c := range candidates {
		require.NoError(t, store.UpsertCandidate(c))
	}

	stats, err := store.GetCandidateStats()
	require.NoError(t, err)
	assert.NotEmpty(t, stats)

	// Build a lookup map for easier assertion.
	type key struct{ entityType, layer, status string }
	lookup := make(map[key]int)
	for _, s := range stats {
		lookup[key{s.EntityType, s.Layer, s.Status}] = s.Count
	}

	assert.Equal(t, 1, lookup[key{"book", "embedding", "pending"}])
	assert.Equal(t, 1, lookup[key{"book", "embedding", "merged"}])
	assert.Equal(t, 1, lookup[key{"author", "metadata", "pending"}])
}

func TestDedupCandidates_RemoveForEntity(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// b1 is involved in two pairs; b3/b4 do not involve b1.
	candidates := []DedupCandidate{
		{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending"},
		{EntityType: "book", EntityAID: "b3", EntityBID: "b1", Layer: "embedding", Status: "pending"},
		{EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "embedding", Status: "pending"},
	}
	for _, c := range candidates {
		require.NoError(t, store.UpsertCandidate(c))
	}

	n, err := store.RemoveCandidatesForEntity("book", "b1")
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	remaining, total, err := store.ListCandidates(CandidateFilter{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, remaining, 1)
	assert.Equal(t, "b3", remaining[0].EntityAID)
	assert.Equal(t, "b4", remaining[0].EntityBID)
}

// TestDedupCandidates_MarkAsMergedForEntity covers MAYDEPLOY-B3: after a merge
// collapses book B into book A, any other candidate row referencing book B
// must be flipped to status="merged" so the candidates UI drops the orphan.
func TestDedupCandidates_MarkAsMergedForEntity(t *testing.T) {
	t.Run("zero matches", func(t *testing.T) {
		store := newTestEmbeddingStore(t)
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "b1", EntityBID: "b2",
			Layer: "embedding", Status: "pending",
		}))

		n, err := store.MarkCandidatesAsMergedForEntity("book", "b99")
		require.NoError(t, err)
		assert.Equal(t, 0, n)

		// Existing row untouched.
		results, _, err := store.ListCandidates(CandidateFilter{})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "pending", results[0].Status)
	})

	t.Run("match on entity_a_id side", func(t *testing.T) {
		store := newTestEmbeddingStore(t)
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "bA", EntityBID: "bX",
			Layer: "embedding", Status: "pending",
		}))
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "bY", EntityBID: "bZ",
			Layer: "embedding", Status: "pending",
		}))

		n, err := store.MarkCandidatesAsMergedForEntity("book", "bA")
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		got, _, err := store.ListCandidates(CandidateFilter{EntityType: "book"})
		require.NoError(t, err)
		statuses := map[string]string{}
		for _, c := range got {
			statuses[c.EntityAID+"|"+c.EntityBID] = c.Status
		}
		// UpsertCandidate canonicalizes so the pair becomes (bA, bX).
		assert.Equal(t, "merged", statuses["bA|bX"])
		assert.Equal(t, "pending", statuses["bY|bZ"])
	})

	t.Run("match on entity_b_id side", func(t *testing.T) {
		store := newTestEmbeddingStore(t)
		// UpsertCandidate canonicalizes so the merged-away book ends up on
		// whichever side it sorts to; constructing a pair where the target
		// will land on the B side requires the target ID to sort *after* its
		// partner.
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "aaa", EntityBID: "zzz",
			Layer: "embedding", Status: "pending",
		}))

		n, err := store.MarkCandidatesAsMergedForEntity("book", "zzz")
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		results, _, err := store.ListCandidates(CandidateFilter{})
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "merged", results[0].Status)
	})

	t.Run("both sides match across rows", func(t *testing.T) {
		store := newTestEmbeddingStore(t)
		// Target book = "bMid". It appears on the A side in one row and on
		// the B side in another after canonicalization.
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "bMid", EntityBID: "zOther",
			Layer: "embedding", Status: "pending",
		}))
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "aOther", EntityBID: "bMid",
			Layer: "embedding", Status: "pending",
		}))
		// Unrelated row must stay pending.
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "x1", EntityBID: "x2",
			Layer: "embedding", Status: "pending",
		}))

		n, err := store.MarkCandidatesAsMergedForEntity("book", "bMid")
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		results, _, err := store.ListCandidates(CandidateFilter{})
		require.NoError(t, err)
		require.Len(t, results, 3)
		merged := 0
		pending := 0
		for _, c := range results {
			switch c.Status {
			case "merged":
				merged++
				// Each merged row must touch bMid.
				assert.True(t, c.EntityAID == "bMid" || c.EntityBID == "bMid", "merged row should reference bMid")
			case "pending":
				pending++
				assert.False(t, c.EntityAID == "bMid" || c.EntityBID == "bMid", "pending row should not reference bMid")
			}
		}
		assert.Equal(t, 2, merged)
		assert.Equal(t, 1, pending)
	})

	t.Run("already merged rows untouched in count", func(t *testing.T) {
		store := newTestEmbeddingStore(t)
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "bA", EntityBID: "bX",
			Layer: "embedding", Status: "merged",
		}))
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "bA", EntityBID: "bY",
			Layer: "embedding", Status: "pending",
		}))

		n, err := store.MarkCandidatesAsMergedForEntity("book", "bA")
		require.NoError(t, err)
		// Only the pending row counts as newly transitioned.
		assert.Equal(t, 1, n)

		results, _, err := store.ListCandidates(CandidateFilter{})
		require.NoError(t, err)
		for _, c := range results {
			assert.Equal(t, "merged", c.Status)
		}
	})

	t.Run("entity_type filter respected", func(t *testing.T) {
		store := newTestEmbeddingStore(t)
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "x1", EntityBID: "x2",
			Layer: "embedding", Status: "pending",
		}))
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "author", EntityAID: "x1", EntityBID: "x2",
			Layer: "metadata", Status: "pending",
		}))

		n, err := store.MarkCandidatesAsMergedForEntity("book", "x1")
		require.NoError(t, err)
		assert.Equal(t, 1, n)

		results, _, err := store.ListCandidates(CandidateFilter{})
		require.NoError(t, err)
		statuses := map[string]string{}
		for _, c := range results {
			statuses[c.EntityType] = c.Status
		}
		assert.Equal(t, "merged", statuses["book"])
		assert.Equal(t, "pending", statuses["author"])
	})

	t.Run("empty inputs noop", func(t *testing.T) {
		store := newTestEmbeddingStore(t)
		require.NoError(t, store.UpsertCandidate(DedupCandidate{
			EntityType: "book", EntityAID: "b1", EntityBID: "b2",
			Layer: "embedding", Status: "pending",
		}))

		n, err := store.MarkCandidatesAsMergedForEntity("", "b1")
		require.NoError(t, err)
		assert.Equal(t, 0, n)

		n, err = store.MarkCandidatesAsMergedForEntity("book", "")
		require.NoError(t, err)
		assert.Equal(t, 0, n)
	})
}

// TestDedupCandidates_LayerPrecedence verifies that an upsert with a
// lower-confidence layer does not downgrade an existing higher-confidence
// row. Precedence: exact > llm > embedding. This locks in the fix for a
// bug where FullScan would silently erase the `exact` bucket because
// findSimilarBooks re-upserted the same pair as `embedding` after
// checkExactTitle had just flagged it as `exact`.
func TestDedupCandidates_LayerPrecedence(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Seed the pair as exact (no similarity — Layer 1 doesn't use one).
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_a",
		EntityBID:  "book_b",
		Layer:      "exact",
		Status:     "pending",
	}))

	// Attempt to overwrite as embedding with a similarity score — this is
	// exactly what findSimilarBooks does during a FullScan pass.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_a",
		EntityBID:  "book_b",
		Layer:      "embedding",
		Similarity: floatPtr(0.94),
		Status:     "pending",
	}))

	got, _, err := store.ListCandidates(CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "exact", got[0].Layer, "exact should not be downgraded to embedding")
	assert.Nil(t, got[0].Similarity, "exact layer should keep its nil similarity, not adopt the embedding's 0.94")

	// Overwriting as llm should also leave exact in place.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_a",
		EntityBID:  "book_b",
		Layer:      "llm",
		LLMVerdict: "duplicate",
		LLMReason:  "same book",
		Status:     "pending",
	}))
	got, _, _ = store.ListCandidates(CandidateFilter{EntityType: "book"})
	require.Len(t, got, 1)
	assert.Equal(t, "exact", got[0].Layer, "exact should not be downgraded to llm")
	// LLM verdict and reason are still persisted even when layer stays exact,
	// so future reviewers see the LLM's take as supplementary evidence.
	assert.Equal(t, "duplicate", got[0].LLMVerdict)
	assert.Equal(t, "same book", got[0].LLMReason)
}

// TestDedupCandidates_LayerUpgrade verifies the opposite direction: an
// embedding row correctly gets upgraded to llm (when the LLM reranker
// processes it) and to exact (if Layer 1 later catches the pair).
func TestDedupCandidates_LayerUpgrade(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Seed as embedding.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_a",
		EntityBID:  "book_b",
		Layer:      "embedding",
		Similarity: floatPtr(0.88),
		Status:     "pending",
	}))

	// Upgrade to llm.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_a",
		EntityBID:  "book_b",
		Layer:      "llm",
		LLMVerdict: "duplicate",
		LLMReason:  "same book, different subtitle",
		Status:     "pending",
	}))
	got, _, _ := store.ListCandidates(CandidateFilter{EntityType: "book"})
	require.Len(t, got, 1)
	assert.Equal(t, "llm", got[0].Layer, "llm should upgrade over embedding")
	assert.Equal(t, "duplicate", got[0].LLMVerdict)

	// Upgrade to exact.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_a",
		EntityBID:  "book_b",
		Layer:      "exact",
		Status:     "pending",
	}))
	got, _, _ = store.ListCandidates(CandidateFilter{EntityType: "book"})
	require.Len(t, got, 1)
	assert.Equal(t, "exact", got[0].Layer, "exact should upgrade over llm")
}

// TestDedupCandidates_UpsertCanonicalizes verifies that inserting the same
// logical pair with swapped entity IDs produces exactly one row (in
// canonical form: smaller ID first). Before this fix, FullScan would
// discover a pair once as (A,B) while processing book A, then again as
// (B,A) while processing book B, and each went into its own row because
// the UNIQUE constraint treats (A,B) and (B,A) as distinct — which is
// why the UI showed the same "Foundation and Empire" pair twice.
func TestDedupCandidates_UpsertCanonicalizes(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Insert the pair as (book_z, book_a) — non-canonical direction.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_z",
		EntityBID:  "book_a",
		Layer:      "embedding",
		Similarity: floatPtr(0.92),
		Status:     "pending",
	}))
	// Insert the same pair in canonical direction — should update the
	// existing row, not create a new one.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book",
		EntityAID:  "book_a",
		EntityBID:  "book_z",
		Layer:      "embedding",
		Similarity: floatPtr(0.93),
		Status:     "pending",
	}))

	got, total, err := store.ListCandidates(CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "exactly one row should exist for the pair")
	require.Len(t, got, 1)
	// Canonical form: smaller ID first
	assert.Equal(t, "book_a", got[0].EntityAID)
	assert.Equal(t, "book_z", got[0].EntityBID)
	// Second upsert's similarity should have taken effect.
	require.NotNil(t, got[0].Similarity)
	assert.InDelta(t, 0.93, *got[0].Similarity, 0.0001)
}

// TestCanonicalizeCandidates_Cleanup verifies the one-time cleanup that
// removes duplicate (A,B)/(B,A) rows from deployments that accumulated
// them before UpsertCandidate started canonicalizing on insert. The
// cleanup must (a) swap non-canonical rows in place when no canonical
// sibling exists, and (b) delete the non-canonical row when a canonical
// sibling already exists.
func TestCanonicalizeCandidates_Cleanup(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Bypass the upsert canonicalization by writing raw PebbleDB keys so we can
	// simulate a pre-fix database state where pairs were stored in non-canonical order.
	var seqCounter int64
	rawInsert := func(typ, a, b, layer string) {
		t.Helper()
		seqCounter++
		id := seqCounter
		now := time.Now().UnixNano()
		rec := candRec{
			EntityType: typ, EntityAID: a, EntityBID: b,
			Layer: layer, Status: "pending",
			CreatedAt: now, UpdatedAt: now,
		}
		data, err := json.Marshal(rec)
		require.NoError(t, err)
		idHex := fmt.Sprintf("%016x", id)
		require.NoError(t, store.db.Set(dedupRecKey(id), data, pebble.Sync))
		require.NoError(t, store.db.Set(dedupPairKey(typ, a, b), []byte(idHex), pebble.Sync))
		// Keep the sequence counter consistent.
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(id))
		require.NoError(t, store.db.Set([]byte(dedupSeqKey), buf[:], pebble.Sync))
	}

	// Case A: non-canonical row with no canonical sibling — should be
	// swapped in place.
	rawInsert("book", "zebra", "apple", "embedding")

	// Case B: canonical row already exists alongside a non-canonical
	// duplicate — the non-canonical duplicate should be deleted.
	rawInsert("book", "hello", "world", "embedding") // canonical (h < w)
	rawInsert("book", "world", "hello", "exact")     // non-canonical duplicate

	// Case C: pair already in canonical form — untouched.
	rawInsert("book", "aaa", "bbb", "embedding")

	rewritten, deleted, err := store.CanonicalizeCandidates()
	require.NoError(t, err)
	assert.Equal(t, 1, rewritten, "case A should be swapped in place")
	assert.Equal(t, 1, deleted, "case B's non-canonical duplicate should be deleted")

	// Verify final state: 3 rows, all in canonical order.
	got, total, err := store.ListCandidates(CandidateFilter{EntityType: "book", Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	require.Len(t, got, 3)
	for _, c := range got {
		assert.LessOrEqual(t, c.EntityAID, c.EntityBID,
			"all rows should have entity_a_id <= entity_b_id after canonicalize, got (%s, %s)",
			c.EntityAID, c.EntityBID)
	}
}

func TestListCandidatesForEntity(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// book "b1" is paired with b2 and b3; b2 and b3 also have a pair with each other.
	require.NoError(t, store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Similarity: floatPtr(0.95), Status: "pending"}))
	require.NoError(t, store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b3", Layer: "embedding", Similarity: floatPtr(0.80), Status: "dismissed"}))
	require.NoError(t, store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b2", EntityBID: "b3", Layer: "embedding", Similarity: floatPtr(0.70), Status: "pending"}))

	// b1 appears in 2 pairs; filtered by status it's 1.
	all, err := store.ListCandidatesForEntity("book", "b1", "")
	require.NoError(t, err)
	assert.Len(t, all, 2, "b1 is in 2 pairs")

	pending, err := store.ListCandidatesForEntity("book", "b1", "pending")
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "pending", pending[0].Status)

	// b2 appears in 2 pairs (b1-b2 and b2-b3).
	b2All, err := store.ListCandidatesForEntity("book", "b2", "")
	require.NoError(t, err)
	assert.Len(t, b2All, 2)

	// Entity with no pairs returns empty slice, not error.
	none, err := store.ListCandidatesForEntity("book", "bX", "")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestBackfillEntityIndex(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Write two candidates via the normal path (entity index is written automatically).
	require.NoError(t, store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending"}))
	require.NoError(t, store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "embedding", Status: "pending"}))

	// BackfillEntityIndex is idempotent — running twice should not error or duplicate.
	n1, err := store.BackfillEntityIndex()
	require.NoError(t, err)
	assert.Equal(t, 2, n1)

	n2, err := store.BackfillEntityIndex()
	require.NoError(t, err)
	assert.Equal(t, 2, n2)

	// Entity index lookups should still return correct results after backfill.
	got, err := store.ListCandidatesForEntity("book", "b1", "")
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestDeleteCandidate_CleansEntityIndex(t *testing.T) {
	store := newTestEmbeddingStore(t)

	require.NoError(t, store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending"}))

	before, err := store.ListCandidatesForEntity("book", "b1", "")
	require.NoError(t, err)
	require.Len(t, before, 1)

	require.NoError(t, store.DeleteCandidate(before[0].ID))

	after, err := store.ListCandidatesForEntity("book", "b1", "")
	require.NoError(t, err)
	assert.Empty(t, after, "entity index entries should be removed on delete")
}
