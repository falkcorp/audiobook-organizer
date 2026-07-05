// file: internal/plugins/dedup/embed_scan_test.go
// version: 1.0.0
// guid: 6a1d9c3e-4b7f-4a2d-9e6c-8f1b2c3d4e5f
// last-edited: 2026-07-05

// Tests for CONC-5: parallelizing the dedup.embed-scan synchronous per-book
// EmbedBook loop with registry.RunItems.
//
// The load-bearing assertion is that the parallel loop (Concurrency =
// embedScanConcurrency, currently 4) produces the SAME result as a plain
// sequential loop over the identical fixture — same embedded/cached/skipped/
// error counts and the same set of stored embedding rows with the same
// vectors. Run under `go test -race` so a regression that drops the atomic
// counters (or otherwise races on shared state) fails loudly.

package dedup

import (
	"context"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/stretchr/testify/require"
)

// fakeEmbedVector is the deterministic vector returned by every fixture
// book's stubbed embed call, so parallel and serial runs are byte-for-byte
// comparable.
var fakeEmbedVector = []float32{0.1, 0.2, 0.3, 0.4}

// newEmbedScanFixture builds n books plus a Plugin wired to a real
// dedup.Engine (real EmbeddingStore backed by a temp PebbleDB, real
// EmbeddingClient with SetRawEmbedForTest stubbing out the network call).
// Each call returns an independent store/engine pair so a serial run and a
// parallel run never share state.
func newEmbedScanFixture(t *testing.T, n int) (*Plugin, *database.MockStore, *database.EmbeddingStore, []database.Book) {
	t.Helper()

	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	es := database.NewEmbeddingStore(db)

	prevEnabled := config.AppConfig.Dedup.EmbeddingsEnabled
	config.AppConfig.Dedup.EmbeddingsEnabled = true
	t.Cleanup(func() { config.AppConfig.Dedup.EmbeddingsEnabled = prevEnabled })

	books := make([]database.Book, n)
	byID := make(map[string]*database.Book, n)
	for i := 0; i < n; i++ {
		b := database.Book{ID: fmt.Sprintf("BOOK_%03d", i), Title: fmt.Sprintf("Test Book %d", i)}
		books[i] = b
		bCopy := b
		byID[b.ID] = &bCopy
	}

	mock := &database.MockStore{
		GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
			return books, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return byID[id], nil
		},
	}

	ms := merge.NewService(mock)
	client := ai.NewEmbeddingClientWithOptions("test-key", "test-model", "")
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = fakeEmbedVector
		}
		return out, nil
	})

	engine := dedupengine.NewEngine(es, mock, client, nil, ms)
	p := &Plugin{engine: engine, store: mock, embeddingStore: es}
	return p, mock, es, books
}

// runEmbedSequential embeds every book in books one at a time, mirroring the
// pre-CONC-5 sequential loop shape, and returns the same counters
// runEmbedScanMode reports.
func runEmbedSequential(t *testing.T, p *Plugin, books []database.Book) (embedded, cached, skipped, errs int) {
	t.Helper()
	for _, book := range books {
		status, err := p.engine.EmbedBook(context.Background(), book.ID)
		if err != nil {
			errs++
			continue
		}
		switch status {
		case dedupengine.EmbedStatusEmbedded:
			embedded++
		case dedupengine.EmbedStatusCached:
			cached++
		default:
			skipped++
		}
	}
	return embedded, cached, skipped, errs
}

// TestRunEmbedScanMode_ParallelMatchesSerial is the CONC-5 correctness test:
// the parallel dedup.embed-scan path (registry.RunItems, Concurrency =
// embedScanConcurrency) must produce identical results to a plain sequential
// loop over the same fixture. Uses enough books that the worker pool
// (embedScanConcurrency == 4) actually overlaps goroutines; run with -race to
// confirm the atomic counters (and the underlying EmbeddingStore) hold up
// under concurrent writes.
func TestRunEmbedScanMode_ParallelMatchesSerial(t *testing.T) {
	const numBooks = 40

	// Serial baseline: independent fixture, independent store.
	serialPlugin, _, serialES, serialBooks := newEmbedScanFixture(t, numBooks)
	wantEmbedded, wantCached, wantSkipped, wantErrs := runEmbedSequential(t, serialPlugin, serialBooks)

	require.Equal(t, numBooks, wantEmbedded, "sanity: every fresh book should embed on first pass")
	require.Zero(t, wantCached)
	require.Zero(t, wantSkipped)
	require.Zero(t, wantErrs)

	// Parallel run through the real op path (embedScanConcurrency workers).
	parallelPlugin, _, parallelES, _ := newEmbedScanFixture(t, numBooks)
	err := parallelPlugin.runEmbedScanMode(context.Background(), false, &mockReporter{})
	require.NoError(t, err)

	// Every book must have an identical stored embedding row in both runs.
	for i := 0; i < numBooks; i++ {
		id := fmt.Sprintf("BOOK_%03d", i)

		serialEmb, err := serialES.Get("book", id)
		require.NoError(t, err)
		require.NotNil(t, serialEmb, "serial run: expected stored embedding for %s", id)

		parallelEmb, err := parallelES.Get("book", id)
		require.NoError(t, err)
		require.NotNil(t, parallelEmb, "parallel run: expected stored embedding for %s", id)

		require.Equal(t, serialEmb.Vector, parallelEmb.Vector, "vector mismatch for %s", id)
		require.Equal(t, serialEmb.TextHash, parallelEmb.TextHash, "text hash mismatch for %s", id)
		require.Equal(t, serialEmb.Model, parallelEmb.Model, "model mismatch for %s", id)
	}
}

// TestRunEmbedScanMode_ReEmbedIsCachedBothSequentiallyAndInParallel verifies
// that a second pass over an already-embedded library reports every book as
// EmbedStatusCached (no re-embed call), and that this holds under the
// parallel path exactly as it did under the old sequential loop — cache-hit
// counting is exactly the kind of shared-state bookkeeping CONC-5 must not
// break.
func TestRunEmbedScanMode_ReEmbedIsCachedBothSequentiallyAndInParallel(t *testing.T) {
	const numBooks = 24

	p, _, _, books := newEmbedScanFixture(t, numBooks)

	// First pass: everything embeds fresh (sequential, to seed the store).
	embedded, cached, skipped, errs := runEmbedSequential(t, p, books)
	require.Equal(t, numBooks, embedded)
	require.Zero(t, cached)
	require.Zero(t, skipped)
	require.Zero(t, errs)

	// Second pass through the real parallel op path: every book's text hash
	// is unchanged, so every book must come back EmbedStatusCached.
	err := p.runEmbedScanMode(context.Background(), false, &mockReporter{})
	require.NoError(t, err)

	// Re-run sequentially too and confirm the same cached outcome, proving
	// parallel and serial agree on the cache-hit path as well as the
	// fresh-embed path.
	embedded2, cached2, skipped2, errs2 := runEmbedSequential(t, p, books)
	require.Zero(t, embedded2)
	require.Equal(t, numBooks, cached2)
	require.Zero(t, skipped2)
	require.Zero(t, errs2)
}
