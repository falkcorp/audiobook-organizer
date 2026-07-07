// file: internal/database/embedding_store_candidate_durability_test.go
// version: 1.0.0
// guid: 4f1e8a20-9c3b-4d7e-8a51-2b6f0c9d7e34
// last-edited: 2026-07-07

package database

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpsertCandidate_SurvivesGracefulClose locks in the durability contract the
// #19 NoSync change depends on. Dedup-candidate writes use pebble.NoSync so the
// per-write fdatasync no longer floods Pebble's L0 (the write-amplification that
// let dedup.full-scan's score phase trigger a Pebble write-stall and freeze for
// 9+ hours). NoSync trades away durability ONLY on a hard crash (kill -9 / power
// loss): a graceful Close — which is what a `systemctl restart` / SIGTERM does —
// must still flush the WAL to SST so no scored candidate is lost. If this
// regresses (Close stops flushing, or a write path silently drops data), every
// restart would lose recent full-scan results.
func TestUpsertCandidate_SurvivesGracefulClose(t *testing.T) {
	dir := t.TempDir()

	db, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	store := &EmbeddingStore{db: db, owned: true}

	sim := 0.87
	id, isNew, err := store.UpsertCandidateNew(DedupCandidate{
		EntityType: "book",
		EntityAID:  "bA",
		EntityBID:  "bB",
		Layer:      "embedding",
		Similarity: &sim,
		Status:     "pending",
	})
	require.NoError(t, err)
	require.True(t, isNew)

	// Graceful close — Pebble flushes the WAL to SST here. This is exactly the
	// systemctl-restart path the NoSync durability tradeoff relies on.
	require.NoError(t, store.Close())

	// Reopen the SAME directory; the candidate must still be there.
	db2, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	store2 := &EmbeddingStore{db: db2, owned: true}
	t.Cleanup(func() { _ = store2.Close() })

	got, err := store2.GetCandidateByID(id)
	require.NoError(t, err)
	require.NotNil(t, got, "candidate must survive a graceful Close under NoSync")
	assert.Equal(t, "bA", got.EntityAID)
	assert.Equal(t, "bB", got.EntityBID)
	assert.Equal(t, "embedding", got.Layer)
	require.NotNil(t, got.Similarity)
	assert.InDelta(t, 0.87, *got.Similarity, 1e-9)
}

// TestCandidateWritePath_ConcurrentNoRace exercises the dedup-candidate write
// path the way FullScan's score phase does — many workers (sized to
// runtime.NumCPU() via registry.RunItems) calling UpsertCandidate concurrently.
// It mixes distinct pairs (no key overlap) with a single hotly-contended shared
// pair (same read-modify-write) and interleaves DeleteCandidate (which is
// lock-free). Run under `-race`, it guards the store's internal locking against a
// future refactor introducing a data race, and asserts concurrent same-pair
// upserts never duplicate the row (the pair-uniqueness invariant the global
// s.mu protects — unchanged by the NoSync switch, which alters only fsync
// durability, not atomicity or visibility).
func TestCandidateWritePath_ConcurrentNoRace(t *testing.T) {
	store := newTestEmbeddingStore(t)

	const workers = 8
	const perWorker = 50
	var wg sync.WaitGroup

	// Distinct pairs — every worker owns a disjoint key space.
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWorker {
				sim := 0.5
				if err := store.UpsertCandidate(DedupCandidate{
					EntityType: "book",
					EntityAID:  fmt.Sprintf("w%d_a%d", w, i),
					EntityBID:  fmt.Sprintf("w%d_b%d", w, i),
					Layer:      "embedding",
					Similarity: &sim,
					Status:     "pending",
				}); err != nil {
					// t.Errorf is safe from a goroutine; require/FailNow is not.
					t.Errorf("distinct upsert w%d i%d: %v", w, i, err)
				}
			}
		}(w)
	}

	// One hotly-contended shared pair — concurrent same-pair read-modify-write.
	const sharedA, sharedB = "shared_a", "shared_b"
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWorker {
				sim := float64(w*perWorker+i) / 1000.0
				if err := store.UpsertCandidate(DedupCandidate{
					EntityType: "book",
					EntityAID:  sharedA,
					EntityBID:  sharedB,
					Layer:      "embedding",
					Similarity: &sim,
					Status:     "pending",
				}); err != nil {
					t.Errorf("shared upsert w%d i%d: %v", w, i, err)
				}
			}
		}(w)
	}
	wg.Wait()

	// Every distinct pair persisted exactly once.
	for w := range workers {
		for i := range perWorker {
			cands, err := store.ListCandidatesForEntity("book", fmt.Sprintf("w%d_a%d", w, i), "pending")
			require.NoError(t, err)
			require.Len(t, cands, 1, "distinct pair w%d_a%d should exist exactly once", w, i)
		}
	}

	// The shared pair collapsed to exactly one row despite the concurrent storm.
	shared, err := store.ListCandidatesForEntity("book", sharedA, "pending")
	require.NoError(t, err)
	assert.Len(t, shared, 1, "concurrent same-pair upserts must not duplicate the row")
}
