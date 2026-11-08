// file: internal/database/hnsw_embedding_store_persist_test.go
// version: 1.1.0
// guid: c3d4e5f6-a7b8-9012-cdef-012345678902
// last-edited: 2026-11-07

package database_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHNSWEmbeddingStore_ExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, store.Upsert(ctx, "book", "b1", []float32{1, 0, 0, 0}, map[string]string{"title": "Dune"}))
	require.NoError(t, store.Upsert(ctx, "book", "b2", []float32{0, 1, 0, 0}, map[string]string{"title": "Foundation"}))

	dir := t.TempDir()
	store.SetComplete(true) // models a fully built graph
	require.NoError(t, store.Export(dir))

	store2 := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, store2.Import(dir))

	meta, err := store2.Get(ctx, "book", "b1")
	require.NoError(t, err)
	assert.Equal(t, "Dune", meta["title"])

	count, err := store2.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestHNSWEmbeddingStore_Import_ErrNoSnapshot(t *testing.T) {
	store := database.NewHNSWEmbeddingStore(4)
	err := store.Import(t.TempDir())
	assert.ErrorIs(t, err, database.ErrNoHNSWSnapshot)
}

// ---- Export gating, dirty tracking and truth ordering ----

const exportTestDim = 8

// batmanIDs are the deterministic entity IDs the export tests use.
var batmanIDs = []string{
	"bruce-wayne", "selina-kyle", "dick-grayson", "barbara-gordon",
	"alfred-pennyworth", "harvey-dent", "jim-gordon", "lucius-fox",
	"tim-drake", "damian-wayne", "oswald-cobblepot", "edward-nygma",
}

// exportTestVec returns a deterministic non-zero vector.
func exportTestVec(rng *rand.Rand) []float32 {
	v := make([]float32, exportTestDim)
	for i := range v {
		v[i] = rng.Float32() + 0.01
	}
	return v
}

// newExportTestStore returns a store holding the first n Batman IDs, NOT yet
// marked complete.
func newExportTestStore(t *testing.T, n int) *database.HNSWEmbeddingStore {
	t.Helper()
	rng := rand.New(rand.NewSource(42))
	s := database.NewHNSWEmbeddingStore(exportTestDim)
	for _, id := range batmanIDs[:n] {
		require.NoError(t, s.Upsert(context.Background(), "book", id, exportTestVec(rng), map[string]string{"k": id}))
	}
	return s
}

// readTruthSidecar parses the truth sidecar the export wrote for "book".
func readTruthSidecar(t *testing.T, dir string) (truthCount, graphCount int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "hnsw-book.truth.json"))
	require.NoError(t, err)
	var st struct {
		TruthCount int `json:"truth_count"`
		GraphCount int `json:"graph_count"`
	}
	require.NoError(t, json.Unmarshal(b, &st))
	return st.TruthCount, st.GraphCount
}

func TestExportIfDirty_SkipsCleanAndExportsAfterChange(t *testing.T) {
	ctx := context.Background()
	s := newExportTestStore(t, 4)
	s.SetComplete(true)
	dir := t.TempDir()
	truth := func(string) (int, error) { return 4, nil }

	at, took := s.LastExport()
	assert.True(t, at.IsZero())
	assert.Zero(t, took)

	res, err := s.ExportIfDirty(dir, truth)
	require.NoError(t, err)
	assert.True(t, res.Exported)
	assert.False(t, res.Clean)
	assert.Positive(t, res.Duration)
	at, took = s.LastExport()
	assert.False(t, at.IsZero())
	assert.Equal(t, res.Duration, took)

	res, err = s.ExportIfDirty(dir, truth)
	require.NoError(t, err)
	assert.True(t, res.Clean, "nothing changed since the last export")
	assert.False(t, res.Exported)

	require.NoError(t, s.Upsert(ctx, "book", "oswald-cobblepot", exportTestVec(rand.New(rand.NewSource(7))), nil))
	res, err = s.ExportIfDirty(dir, truth)
	require.NoError(t, err)
	assert.True(t, res.Exported, "an Upsert makes the store dirty")

	res, err = s.ExportIfDirty(dir, truth)
	require.NoError(t, err)
	assert.True(t, res.Clean)

	require.NoError(t, s.Delete(ctx, "book", "bruce-wayne"))
	res, err = s.ExportIfDirty(dir, truth)
	require.NoError(t, err)
	assert.True(t, res.Exported, "a Delete makes the store dirty")

	res, err = s.ExportIfDirty(dir, truth)
	require.NoError(t, err)
	assert.True(t, res.Clean)

	// A never-mutated empty store is Clean, so it can never be written over a
	// good snapshot.
	empty := database.NewHNSWEmbeddingStore(exportTestDim)
	empty.SetComplete(true)
	emptyDir := t.TempDir()
	res, err = empty.ExportIfDirty(emptyDir, truth)
	require.NoError(t, err)
	assert.True(t, res.Clean)
	entries, err := os.ReadDir(emptyDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestExport_IncompleteGraphRefused(t *testing.T) {
	s := newExportTestStore(t, 3) // SetComplete never called: a half-hydrated graph
	dir := t.TempDir()
	truth := func(string) (int, error) { return 3, nil }

	err := s.ExportWithTruth(dir, truth)
	assert.ErrorIs(t, err, database.ErrHNSWIncomplete)
	err = s.Export(dir)
	assert.ErrorIs(t, err, database.ErrHNSWIncomplete)
	res, err := s.ExportIfDirty(dir, truth)
	assert.ErrorIs(t, err, database.ErrHNSWIncomplete)
	assert.False(t, res.Exported)
	entries, rerr := os.ReadDir(dir)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "a refused export must write nothing")
	at, _ := s.LastExport()
	assert.True(t, at.IsZero())

	s.SetComplete(true)
	require.NoError(t, s.ExportWithTruth(dir, truth))
	_, statErr := os.Stat(filepath.Join(dir, "hnsw-book.bin"))
	assert.NoError(t, statErr)

	// And back to incomplete (a re-hydrate started): refused again.
	s.SetComplete(false)
	require.NoError(t, s.Upsert(context.Background(), "book", "edward-nygma", exportTestVec(rand.New(rand.NewSource(9))), nil))
	_, err = s.ExportIfDirty(dir, truth)
	assert.True(t, errors.Is(err, database.ErrHNSWIncomplete))
}

// TestExport_TruthReadBeforeSerialization pins the ordering that makes a
// snapshot's truth sidecar safe. The invariant: a snapshot the next boot accepts
// as fresh (recorded truth == current Pebble count) must contain every row that
// count includes. That holds only if truth is read BEFORE the graph is
// serialized. If truth were read after, a row that lands between the two would
// be counted by the sidecar but absent from the graph, and the boot would accept
// the incomplete graph.
//
// The truth callback models "a row lands while the export is running": it adds
// the row to the Pebble stand-in AND mirrors it into the store (as the dedup
// mirror does), then reports the Pebble count. Read first, the mirrored node is
// then serialized, so GraphCount includes it; read last, the node is not in the
// graph yet and GraphCount is one short.
func TestExport_TruthReadBeforeSerialization(t *testing.T) {
	ctx := context.Background()

	t.Run("row lands during export: accepted snapshot has the row", func(t *testing.T) {
		s := newExportTestStore(t, 3)
		s.SetComplete(true)
		pebbleRows := 3
		truth := func(string) (int, error) {
			// No store lock is held while truth runs, so a direct Upsert is safe.
			require.NoError(t, s.Upsert(ctx, "book", "oswald-cobblepot", exportTestVec(rand.New(rand.NewSource(7))), nil))
			pebbleRows++
			return pebbleRows, nil
		}
		dir := t.TempDir()
		require.NoError(t, s.ExportWithTruth(dir, truth))

		tc, gc := readTruthSidecar(t, dir)
		assert.Equal(t, 4, tc)
		assert.Equal(t, 4, gc, "the row counted by truth must be in the serialized graph (truth read first)")

		fresh := database.NewHNSWEmbeddingStore(exportTestDim)
		require.NoError(t, fresh.ImportWithStalenessCheck(dir, func(string) (int, error) { return pebbleRows, nil }))
		n, err := fresh.CountByType(ctx, "book")
		require.NoError(t, err)
		assert.Equal(t, pebbleRows, n, "a snapshot accepted as fresh must contain every counted row")
	})

	t.Run("count read before the row lands: snapshot looks stale", func(t *testing.T) {
		s := newExportTestStore(t, 3)
		s.SetComplete(true)
		pebbleRows := 3
		truth := func(string) (int, error) {
			recorded := pebbleRows
			require.NoError(t, s.Upsert(ctx, "book", "oswald-cobblepot", exportTestVec(rand.New(rand.NewSource(7))), nil))
			pebbleRows++
			return recorded, nil
		}
		dir := t.TempDir()
		require.NoError(t, s.ExportWithTruth(dir, truth))

		fresh := database.NewHNSWEmbeddingStore(exportTestDim)
		require.NoError(t, fresh.ImportWithStalenessCheck(dir, func(string) (int, error) { return pebbleRows, nil }))
		n, err := fresh.CountByType(ctx, "book")
		require.NoError(t, err)
		assert.Zero(t, n, "recorded count 3 vs current 4: the snapshot must be discarded as stale")
	})
}

func TestImport_SetsCompleteAndStaleDiscardClears(t *testing.T) {
	ctx := context.Background()
	src := newExportTestStore(t, 4)
	src.SetComplete(true)
	dir := t.TempDir()
	require.NoError(t, src.ExportWithTruth(dir, func(string) (int, error) { return 4, nil }))

	// Plain Import marks the store complete.
	imp := database.NewHNSWEmbeddingStore(exportTestDim)
	assert.ErrorIs(t, imp.Export(t.TempDir()), database.ErrHNSWIncomplete, "a new store starts incomplete")
	require.NoError(t, imp.Import(dir))
	assert.NoError(t, imp.Export(t.TempDir()), "Import marks the store complete")

	// A failed Import (no snapshot) leaves a new store incomplete.
	none := database.NewHNSWEmbeddingStore(exportTestDim)
	assert.ErrorIs(t, none.Import(t.TempDir()), database.ErrNoHNSWSnapshot)
	assert.ErrorIs(t, none.Export(t.TempDir()), database.ErrHNSWIncomplete)

	// Fresh by the staleness check: stays complete.
	fresh := database.NewHNSWEmbeddingStore(exportTestDim)
	require.NoError(t, fresh.ImportWithStalenessCheck(dir, func(string) (int, error) { return 4, nil }))
	n, err := fresh.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.NoError(t, fresh.Export(t.TempDir()))

	// Stale: discarded, empty, incomplete again.
	stale := database.NewHNSWEmbeddingStore(exportTestDim)
	require.NoError(t, stale.ImportWithStalenessCheck(dir, func(string) (int, error) { return 5, nil }))
	n, err = stale.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.ErrorIs(t, stale.Export(t.TempDir()), database.ErrHNSWIncomplete)
	_, err = stale.ExportIfDirty(t.TempDir(), nil)
	assert.ErrorIs(t, err, database.ErrHNSWIncomplete, "a discard bumps the generation, so it is not mistaken for an empty store")

	// Hydration then completes it.
	stale.SetComplete(true)
	assert.NoError(t, stale.Export(t.TempDir()))
}

func TestExportIfDirty_ConcurrentCallersExportOnce(t *testing.T) {
	s := newExportTestStore(t, 10)
	s.SetComplete(true)
	dir := t.TempDir()
	truth := func(string) (int, error) { return 10, nil }

	const callers = 16
	results := make([]database.ExportResult, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.ExportIfDirty(dir, truth)
		}()
	}
	close(start)
	wg.Wait()

	exported, clean := 0, 0
	for i := range callers {
		require.NoError(t, errs[i])
		if results[i].Exported {
			exported++
		}
		if results[i].Clean {
			clean++
		}
	}
	assert.Equal(t, 1, exported, "exactly one caller exports")
	assert.Equal(t, callers-1, clean, "every other caller sees Clean")
}

// TestExportIfDirty_MutationsDuringExport runs Upserts against a loop of exports
// (meaningful under -race): serialization holds the read lock only for the
// in-memory pass, and every snapshot written must be loadable.
func TestExportIfDirty_MutationsDuringExport(t *testing.T) {
	ctx := context.Background()
	s := newExportTestStore(t, 6)
	s.SetComplete(true)
	dir := t.TempDir()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(11))
		for _, id := range batmanIDs[6:] {
			assert.NoError(t, s.Upsert(ctx, "book", id, exportTestVec(rng), nil))
		}
	}()
	for range 5 {
		_, err := s.ExportIfDirty(dir, nil)
		require.NoError(t, err)
	}
	wg.Wait()

	// Either an earlier export already saw every upsert (Clean) or this one
	// writes them; both leave a snapshot holding all of them.
	_, err := s.ExportIfDirty(dir, nil)
	require.NoError(t, err)

	loaded := database.NewHNSWEmbeddingStore(exportTestDim)
	require.NoError(t, loaded.Import(dir))
	n, err := loaded.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Equal(t, len(batmanIDs), n)
}
