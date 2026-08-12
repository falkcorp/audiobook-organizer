// file: internal/database/embedding_store_chaos_test.go
// version: 2.1.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-08-12
//
// Chaos tests for the EmbeddingStore under shutdown conditions.
// Validates graceful behavior when the store is closed during or
// after active use. Backlog 4.6.

package database

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOwnedEmbeddingStore opens a standalone PebbleDB in dir and returns an
// EmbeddingStore that owns it. Close() will actually shut down the DB,
// allowing chaos tests to verify post-close behaviour.
func newOwnedEmbeddingStore(t *testing.T, dir string) *EmbeddingStore {
	t.Helper()
	db, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	return &EmbeddingStore{db: db, owned: true}
}

// waitForWorkers blocks until wg is done, or fails the test after a deadline.
//
// The chaos tests below deliberately race Close against in-flight operations.
// When that deadlocked (three writers stuck forever on Pebble's
// commitPipeline.prepare), a bare wg.Wait() produced NO test failure — it hung
// until the CI job was killed, which surfaced as "Go Tests cancelled" with no
// test named and no stack. One occurrence burned the whole job cap; see
// todo.d/20260812-embeddingstore-close-deadlock.md.
//
// recover() cannot help here: a deadlock is not a panic. A bounded wait is what
// turns a silent 30-minute hang into a 30-second named failure, so a regression
// is reported instead of merely being expensive.
func waitForWorkers(t *testing.T, wg *sync.WaitGroup, within time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("workers still running %v after Close returned: operations "+
			"raced Close and deadlocked. EmbeddingStore.closeMu is supposed to "+
			"make this impossible — Close must not start until every in-flight "+
			"operation has released its read lock.", within)
	}
}

// makeVector creates a random float32 vector of the given dimension.
func makeVector(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rand.Float32()
	}
	return v
}

// makeEmbedding creates a test Embedding with a random vector.
func makeEmbedding(entityType, entityID string) Embedding {
	return Embedding{
		EntityType: entityType,
		EntityID:   entityID,
		TextHash:   "hash-" + entityID,
		Vector:     makeVector(256),
		Model:      "test-model",
	}
}

// TestChaos_DoubleClose verifies that calling Close twice does not panic.
func TestChaos_DoubleClose(t *testing.T) {
	store := newOwnedEmbeddingStore(t, t.TempDir())

	// First close should succeed.
	require.NoError(t, store.Close())

	// Second close: owned=true but db is already closed, should not panic.
	_ = store.Close()
}

// TestChaos_OperationsAfterClose verifies that all major operations
// return errors (rather than panic) after the store is closed.
func TestChaos_OperationsAfterClose(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Seed some data before closing.
	require.NoError(t, store.Upsert(makeEmbedding("book", "b1")))

	// Close the store.
	require.NoError(t, store.Close())

	t.Run("Upsert", func(t *testing.T) {
		err := store.Upsert(makeEmbedding("book", "b2"))
		assert.Error(t, err, "Upsert after Close should fail")
	})

	t.Run("Get", func(t *testing.T) {
		_, err := store.Get("book", "b1")
		assert.Error(t, err, "Get after Close should fail")
	})

	t.Run("Delete", func(t *testing.T) {
		err := store.Delete("book", "b1")
		assert.Error(t, err, "Delete after Close should fail")
	})

	t.Run("FindSimilar", func(t *testing.T) {
		_, err := store.FindSimilar("book", makeVector(256), 0.5, 10)
		assert.Error(t, err, "FindSimilar after Close should fail")
	})

	t.Run("ListByType", func(t *testing.T) {
		_, err := store.ListByType("book")
		assert.Error(t, err, "ListByType after Close should fail")
	})

	t.Run("CountByType", func(t *testing.T) {
		_, err := store.CountByType("book")
		assert.Error(t, err, "CountByType after Close should fail")
	})

	t.Run("UpsertCandidate", func(t *testing.T) {
		err := store.UpsertCandidate(DedupCandidate{
			EntityType: "book",
			EntityAID:  "b1",
			EntityBID:  "b2",
			Layer:      "embedding",
			Status:     "pending",
		})
		assert.Error(t, err, "UpsertCandidate after Close should fail")
	})

	t.Run("ListCandidates", func(t *testing.T) {
		_, _, err := store.ListCandidates(CandidateFilter{})
		assert.Error(t, err, "ListCandidates after Close should fail")
	})
}

// TestChaos_ConcurrentWritesDuringClose simulates a shutdown scenario
// where multiple goroutines are writing embeddings while Close is called.
// PebbleDB may panic on concurrent use during close; goroutines recover so
// the test process remains alive.
func TestChaos_ConcurrentWritesDuringClose(t *testing.T) {
	store := newOwnedEmbeddingStore(t, t.TempDir())

	const writers = 10
	const writesPerWorker = 50

	var wg sync.WaitGroup

	for i := range writers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() { recover() }() // PebbleDB panics on close-during-write are acceptable
			for j := range writesPerWorker {
				id := fmt.Sprintf("w%d-e%d", workerID, j)
				_ = store.Upsert(makeEmbedding("book", id))
			}
		}(i)
	}

	time.Sleep(5 * time.Millisecond)
	_ = store.Close()

	waitForWorkers(t, &wg, 30*time.Second)
}

// TestChaos_ConcurrentReadsDuringClose simulates readers active when
// the store is shut down.
func TestChaos_ConcurrentReadsDuringClose(t *testing.T) {
	store := newOwnedEmbeddingStore(t, t.TempDir())

	// Seed data.
	for i := range 20 {
		require.NoError(t, store.Upsert(makeEmbedding("book", fmt.Sprintf("b%d", i))))
	}

	const readers = 10
	const readsPerWorker = 50

	var wg sync.WaitGroup

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }() // PebbleDB panics on close-during-read are acceptable
			for range readsPerWorker {
				_, _ = store.ListByType("book")
				_, _ = store.FindSimilar("book", makeVector(256), 0.5, 5)
			}
		}()
	}

	time.Sleep(2 * time.Millisecond)
	_ = store.Close()

	waitForWorkers(t, &wg, 30*time.Second)
}

// TestChaos_MixedReadWriteDuringClose simulates a realistic shutdown
// where both reads and writes are in flight.
func TestChaos_MixedReadWriteDuringClose(t *testing.T) {
	store := newOwnedEmbeddingStore(t, t.TempDir())

	// Seed some data.
	for i := range 10 {
		require.NoError(t, store.Upsert(makeEmbedding("book", fmt.Sprintf("seed%d", i))))
	}

	var wg sync.WaitGroup

	// Writers.
	for i := range 5 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer func() { recover() }() // panics during close are acceptable
			for j := range 30 {
				_ = store.Upsert(makeEmbedding("book", fmt.Sprintf("w%d-%d", id, j)))
			}
		}(i)
	}

	// Readers.
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			for range 30 {
				_, _ = store.Get("book", "seed0")
				_, _ = store.CountByType("book")
			}
		}()
	}

	// Candidate writers.
	for i := range 3 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer func() { recover() }()
			for j := range 20 {
				_ = store.UpsertCandidate(DedupCandidate{
					EntityType: "book",
					EntityAID:  fmt.Sprintf("a%d", j),
					EntityBID:  fmt.Sprintf("b%d", j),
					Layer:      "embedding",
					Status:     "pending",
				})
				_ = id
			}
		}(i)
	}

	time.Sleep(3 * time.Millisecond)
	_ = store.Close()

	waitForWorkers(t, &wg, 30*time.Second)
}

// TestChaos_DataDurabilityAfterGracefulClose verifies that data written
// before a graceful Close survives a re-open.
func TestChaos_DataDurabilityAfterGracefulClose(t *testing.T) {
	dir := t.TempDir()

	// Phase 1: write data and close.
	store1 := newOwnedEmbeddingStore(t, dir)

	for i := range 100 {
		require.NoError(t, store1.Upsert(makeEmbedding("book", fmt.Sprintf("b%d", i))))
	}
	require.NoError(t, store1.Close())

	// Phase 2: re-open and verify.
	store2 := newOwnedEmbeddingStore(t, dir)
	defer store2.Close()

	count, err := store2.CountByType("book")
	require.NoError(t, err)
	assert.Equal(t, 100, count, "all 100 embeddings should survive close+reopen")

	// Verify a specific embedding is readable.
	emb, err := store2.Get("book", "b42")
	require.NoError(t, err)
	assert.NotNil(t, emb, "embedding b42 should be retrievable after reopen")
	assert.Equal(t, "hash-b42", emb.TextHash)
	assert.Len(t, emb.Vector, 256)
}

// TestChaos_WALCleanupOnClose verifies that after close the DB can be
// reopened and all data is intact (PebbleDB equivalent of WAL checkpoint).
func TestChaos_WALCleanupOnClose(t *testing.T) {
	dir := t.TempDir()

	store := newOwnedEmbeddingStore(t, dir)

	// Write enough data to exercise compaction paths.
	for i := range 200 {
		require.NoError(t, store.Upsert(makeEmbedding("book", fmt.Sprintf("b%d", i))))
	}

	require.NoError(t, store.Close())

	// Re-open and verify all data persisted.
	store2 := newOwnedEmbeddingStore(t, dir)
	defer store2.Close()

	count, err := store2.CountByType("book")
	require.NoError(t, err)
	assert.Equal(t, 200, count, "all data should persist through WAL checkpoint")
}
