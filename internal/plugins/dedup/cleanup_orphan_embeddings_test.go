// file: internal/plugins/dedup/cleanup_orphan_embeddings_test.go
// version: 1.1.0
// guid: a53bc26a-3ecb-4aab-bef8-d6e76c174cd7
// last-edited: 2026-09-11

// Tests for the dedup.cleanup-orphan-embeddings op (retroactive counterpart to
// PR #1802's DeleteBook fix).
//
// These wire an in-memory EmbeddingStore + MockStore (no real Engine needed —
// the op only reads embeddings and looks up books), then exercise the op
// wrapper: dry-run reports correct orphan/live counts without mutating,
// apply deletes only orphaned rows and leaves live-book embeddings untouched
// (including ones with a "wrong" model — out of scope for this op), and a
// second apply run is idempotent (finds nothing left to delete).

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errBarrierTimeout is what a concurrencyBarrier hands back when the required
// number of callers never arrived together — i.e. the code under test ran its
// per-item lookups one at a time.
var errBarrierTimeout = errors.New("barrier timeout: lookups never ran concurrently")

// concurrencyBarrier proves a per-item lookup is fanned out rather than run
// on one core: enter blocks every caller until `want` of them are inside at
// once, then releases them all. A sequential caller can never satisfy that,
// so it trips the timeout instead of hanging — the pre-DA-03 loop FAILS the
// test rather than stalling it — and every later caller fails fast too.
type concurrencyBarrier struct {
	want     int32
	timeout  time.Duration
	inFlight atomic.Int32
	timedOut atomic.Bool
	release  chan struct{}
	once     sync.Once
}

func newConcurrencyBarrier(want int, timeout time.Duration) *concurrencyBarrier {
	return &concurrencyBarrier{want: int32(want), timeout: timeout, release: make(chan struct{})}
}

func (b *concurrencyBarrier) enter() error {
	if b.timedOut.Load() {
		return errBarrierTimeout
	}
	if b.inFlight.Add(1) >= b.want {
		b.once.Do(func() { close(b.release) })
	}
	select {
	case <-b.release:
		return nil
	case <-time.After(b.timeout):
		b.timedOut.Store(true)
		return errBarrierTimeout
	}
}

// requireMultiCore skips a concurrency-shape test on a host where a
// NumCPU-sized pool degenerates to one worker.
func requireMultiCore(t *testing.T) {
	t.Helper()
	if runtime.NumCPU() < 2 {
		t.Skip("needs >= 2 CPUs for a NumCPU-sized worker pool to run concurrently")
	}
}

// cleanupOrphanMockStore returns a MockStore whose GetBookByID resolves only
// the given live book IDs; any other ID returns (nil, nil) — the "book gone"
// signal the op relies on.
func cleanupOrphanMockStore(liveIDs ...string) *database.MockStore {
	books := make(map[string]*database.Book, len(liveIDs))
	for _, id := range liveIDs {
		books[id] = &database.Book{ID: id, Title: "Live Book " + id}
	}
	return &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return books[id], nil
		},
	}
}

// seedEmbedding upserts a book embedding with the given entity ID and model.
func seedEmbedding(t *testing.T, es *database.EmbeddingStore, entityID, model string) {
	t.Helper()
	require.NoError(t, es.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   entityID,
		Model:      model,
		Vector:     []float32{0.1, 0.2, 0.3},
	}))
}

// TestCleanupOrphanEmbeddingsOp_Metadata asserts the OperationDef shape.
func TestCleanupOrphanEmbeddingsOp_Metadata(t *testing.T) {
	p := &Plugin{}
	def := p.cleanupOrphanEmbeddingsDef()
	assert.Equal(t, "dedup.cleanup-orphan-embeddings", def.ID)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryRead)
	assert.Contains(t, def.Capabilities, sdk.CapLibraryWrite)

	var params cleanupOrphanEmbeddingsParams
	require.NoError(t, json.Unmarshal([]byte(`{}`), &params))
	assert.False(t, params.Apply, "apply must default to false")
}

// TestCleanupOrphanEmbeddingsOp_DryRunReportsCountsWithoutMutating asserts
// dry-run correctly classifies orphaned vs. live embeddings and writes
// nothing to the store.
func TestCleanupOrphanEmbeddingsOp_DryRunReportsCountsWithoutMutating(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1", "book-live-2")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-live-2", "text-embedding-3-large") // "wrong" model, but book is live
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-2", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanEmbeddingsParams{Apply: false})
	require.NoError(t, err)
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))

	// Dry-run must not mutate: all 4 embeddings still present.
	all, err := es.ListByType("book")
	require.NoError(t, err)
	assert.Len(t, all, 4, "dry-run must not delete any embedding")
}

// TestCleanupOrphanEmbeddingsOp_ScanClassifiesCorrectly directly exercises the
// scan core to assert exact orphan/live counts and the sample contents.
func TestCleanupOrphanEmbeddingsOp_ScanClassifiesCorrectly(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1", "book-live-2")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-live-2", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-2", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	embeddings, err := es.ListByType("book")
	require.NoError(t, err)

	report, err := p.scanOrphanEmbeddings(context.Background(), &mockReporter{}, embeddings)
	require.NoError(t, err)

	assert.Equal(t, 4, report.Total)
	assert.Equal(t, 2, report.Orphaned)
	assert.Equal(t, 2, report.Live)
	assert.Equal(t, 0, report.LookupErr)
	assert.ElementsMatch(t, []string{"book-gone-1", "book-gone-2"}, report.OrphanIDs)
}

// TestCleanupOrphanEmbeddingsOp_ScanRunsConcurrently is the DA-03 regression
// test: GetBookByID blocks until two lookups are in flight at once, so the
// pre-fix sequential loop times out on its first call (every row then counts
// as a lookup error) while the worker-pool scan releases the barrier. It also
// pins the report shape under concurrency: exact counters, OrphanIDs in
// listing order, and Sample = the first cleanupOrphanSampleLimit orphans.
func TestCleanupOrphanEmbeddingsOp_ScanRunsConcurrently(t *testing.T) {
	requireMultiCore(t)

	const n = 32
	barrier := newConcurrencyBarrier(2, 2*time.Second)
	var calls atomic.Int32
	ms := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			calls.Add(1)
			if err := barrier.enter(); err != nil {
				return nil, err
			}
			if id == "book-live-0" {
				return &database.Book{ID: id, Title: "Live"}, nil
			}
			return nil, nil // orphaned
		},
	}
	p := buildPlugin(t, newTestEmbeddingStorePurge(t), ms)

	embeddings := make([]database.Embedding, 0, n)
	embeddings = append(embeddings, database.Embedding{EntityType: "book", EntityID: "book-live-0", Model: "bge-m3"})
	wantOrphans := make([]string, 0, n-1)
	for i := 1; i < n; i++ {
		id := fmt.Sprintf("book-gone-%02d", i)
		embeddings = append(embeddings, database.Embedding{EntityType: "book", EntityID: id, Model: "text-embedding-3-large"})
		wantOrphans = append(wantOrphans, id)
	}

	report, err := p.scanOrphanEmbeddings(context.Background(), &mockReporter{}, embeddings)
	require.NoError(t, err)

	assert.Equal(t, int32(n), calls.Load(), "every embedding must be looked up exactly once")
	assert.Equal(t, 0, report.LookupErr, "a lookup error here means GetBookByID ran sequentially and tripped the barrier")
	assert.Equal(t, n, report.Total)
	assert.Equal(t, 1, report.Live)
	assert.Equal(t, n-1, report.Orphaned)
	assert.Equal(t, wantOrphans, report.OrphanIDs, "OrphanIDs must keep listing order regardless of worker completion order")

	require.Len(t, report.Sample, cleanupOrphanSampleLimit)
	for i, s := range report.Sample {
		assert.Equal(t, wantOrphans[i], s.EntityID, "Sample must be the first orphans in listing order")
		assert.Equal(t, "text-embedding-3-large", s.Model)
	}
}

// TestCleanupOrphanEmbeddingsOp_ScanHonorsCancellation asserts the parallel
// scan still surfaces the bare context sentinel the sequential loop returned.
func TestCleanupOrphanEmbeddingsOp_ScanHonorsCancellation(t *testing.T) {
	ms := cleanupOrphanMockStore()
	p := buildPlugin(t, newTestEmbeddingStorePurge(t), ms)
	embeddings := []database.Embedding{{EntityType: "book", EntityID: "book-gone-1"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.scanOrphanEmbeddings(ctx, &mockReporter{}, embeddings)
	assert.Equal(t, context.Canceled, err)
}

// TestCleanupOrphanEmbeddingsOp_ApplyDeletesOnlyOrphans asserts apply=true
// deletes only the rows whose book is confirmed gone, leaving live-book
// embeddings — including one with a "wrong" model — untouched.
func TestCleanupOrphanEmbeddingsOp_ApplyDeletesOnlyOrphans(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1", "book-live-2")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-live-2", "text-embedding-3-large") // stale model, but out of scope: book is live
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")
	seedEmbedding(t, es, "book-gone-2", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanEmbeddingsParams{Apply: true})
	require.NoError(t, err)
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))

	remaining, err := es.ListByType("book")
	require.NoError(t, err)
	require.Len(t, remaining, 2, "only the two orphaned rows should be deleted")

	ids := make([]string, 0, len(remaining))
	for _, e := range remaining {
		ids = append(ids, e.EntityID)
	}
	assert.ElementsMatch(t, []string{"book-live-1", "book-live-2"}, ids)

	// The live book with a "wrong" model must be untouched — model-aware
	// re-embed is out of scope for this op.
	live2, err := es.Get("book", "book-live-2")
	require.NoError(t, err)
	require.NotNil(t, live2)
	assert.Equal(t, "text-embedding-3-large", live2.Model)

	// Orphaned rows are gone.
	gone1, err := es.Get("book", "book-gone-1")
	require.NoError(t, err)
	assert.Nil(t, gone1)
	gone2, err := es.Get("book", "book-gone-2")
	require.NoError(t, err)
	assert.Nil(t, gone2)
}

// TestCleanupOrphanEmbeddingsOp_ApplyTwiceIsIdempotent asserts a second apply
// run after a clean pass finds nothing left to delete.
func TestCleanupOrphanEmbeddingsOp_ApplyTwiceIsIdempotent(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := cleanupOrphanMockStore("book-live-1")

	seedEmbedding(t, es, "book-live-1", "bge-m3")
	seedEmbedding(t, es, "book-gone-1", "text-embedding-3-large")

	p := buildPlugin(t, es, ms)
	params, err := json.Marshal(cleanupOrphanEmbeddingsParams{Apply: true})
	require.NoError(t, err)

	// First apply deletes the one orphan.
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))
	remaining, err := es.ListByType("book")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "book-live-1", remaining[0].EntityID)

	// Second apply against the now-clean store must be a no-op — nothing left
	// to delete, and the live embedding is still present.
	require.NoError(t, p.runCleanupOrphanEmbeddings(context.Background(), params, &mockReporter{}))
	remaining, err = es.ListByType("book")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "book-live-1", remaining[0].EntityID)
}
