// file: internal/database/hnsw_reconcile_test.go
// version: 1.0.2
// guid: 7ffad9f5-a4f3-49fd-b4ee-3f8b2d043e26
// last-edited: 2026-11-08

package database_test

import (
	"context"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Review B2: a stale snapshot is reconciled in place, not rebuilt.

// primaryMeta is the metadata the dedup mirror gives a primary book.
func primaryMeta() map[string]string { return map[string]string{"is_primary_version": "true"} }

// staleFixture is an exported snapshot of known books, plus the vectors it holds.
type staleFixture struct {
	dir  string
	vecs map[string][]float32 // book ID -> vector in the snapshot
	rng  *rand.Rand
}

// newStaleFixture exports a complete snapshot of the given books, recorded
// against a Pebble count of 100.
func newStaleFixture(t *testing.T, bookIDs []string) staleFixture {
	t.Helper()
	f := staleFixture{dir: t.TempDir(), vecs: map[string][]float32{}, rng: rand.New(rand.NewSource(99))}
	src := database.NewHNSWEmbeddingStore(exportTestDim)
	for _, id := range bookIDs {
		v := exportTestVec(f.rng)
		f.vecs[id] = v
		require.NoError(t, src.Upsert(context.Background(), "book", id, v, primaryMeta()))
	}
	src.SetComplete(true)
	require.NoError(t, src.ExportWithTruth(f.dir, func(string) (int, error) { return 100, nil }))
	return f
}

// importStale imports f with a Pebble count that moved, so the snapshot is stale.
func importStale(t *testing.T, f staleFixture) *database.HNSWEmbeddingStore {
	t.Helper()
	s := newBoundImportStore(t)
	require.NoError(t, s.ImportWithStalenessCheck(f.dir, func(string) (int, error) { return 101, nil }))
	require.True(t, s.NeedsReconcile(), "a stale snapshot is installed pending a reconcile")
	assert.ErrorIs(t, s.Export(t.TempDir()), database.ErrHNSWIncomplete, "a pending snapshot is not exported")
	return s
}

// indexedRows returns an Index row for every snapshot book, in ID order.
func (f staleFixture) indexedRows() []database.ReconcileRow {
	rows := make([]database.ReconcileRow, 0, len(f.vecs))
	for _, id := range slices.Sorted(maps.Keys(f.vecs)) {
		rows = append(rows, database.ReconcileRow{ID: id, Vector: f.vecs[id], Verdict: database.ReconcileIndex, Meta: primaryMeta()})
	}
	return rows
}

func dropRow(rows []database.ReconcileRow, id string) []database.ReconcileRow {
	return slices.DeleteFunc(rows, func(r database.ReconcileRow) bool { return r.ID == id })
}

func setVerdict(rows []database.ReconcileRow, id string, v database.ReconcileVerdict) {
	for i := range rows {
		if rows[i].ID == id {
			rows[i].Verdict = v
		}
	}
}

// foundIDs returns the IDs FindSimilar returns for query.
func foundIDs(t *testing.T, s *database.HNSWEmbeddingStore, query []float32, filter map[string]string) []string {
	t.Helper()
	res, err := s.FindSimilar(context.Background(), "book", query, 20, filter)
	require.NoError(t, err)
	ids := make([]string, len(res))
	for i, r := range res {
		ids[i] = r.EntityID
	}
	return ids
}

func TestReconcile_MissingKeysReplayed(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs[:4])
	s := importStale(t, f)

	rows := f.indexedRows()
	added := map[string][]float32{}
	for _, id := range []string{"tim-drake", "damian-wayne"} {
		v := exportTestVec(f.rng)
		added[id] = v
		rows = append(rows, database.ReconcileRow{ID: id, Vector: v, Verdict: database.ReconcileIndex, Meta: primaryMeta()})
	}

	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.Empty(t, report.RebuildReason)
	assert.True(t, report.Complete)
	assert.Equal(t, database.ReconcileCounts{Rows: 6, Kept: 4, Added: 2}, report.ByType["book"])
	assert.False(t, s.NeedsReconcile())

	n, err := s.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	for id, v := range added {
		got := foundIDs(t, s, v, primaryMeta())
		require.NotEmpty(t, got)
		assert.Equal(t, id, got[0], "the replayed key is its own nearest neighbour")
		m, err := s.Get(ctx, "book", id)
		require.NoError(t, err)
		assert.Equal(t, primaryMeta(), m)
	}

	// Complete again: the reconciled graph is exported, and after a clean stop
	// that snapshot is fresh.
	dir := t.TempDir()
	require.NoError(t, s.ExportWithTruth(dir, func(string) (int, error) { return 101, nil }))
	requireCleanStop(t, s, dir)
	next := newBoundImportStore(t)
	require.NoError(t, next.ImportWithStalenessCheck(dir, func(string) (int, error) { return 101, nil }))
	assert.False(t, next.NeedsReconcile())
	n, err = next.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Equal(t, 6, n)
}

func TestReconcile_DeletedKeysTombstonedAndFiltered(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs) // 12 books; 1 tombstone stays under the 10% rebuild share
	s := importStale(t, f)
	const gone = "harvey-dent"

	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": dropRow(f.indexedRows(), gone)})
	require.NoError(t, err)
	assert.Empty(t, report.RebuildReason)
	assert.True(t, report.Complete)
	assert.Equal(t, database.ReconcileCounts{Rows: 11, Kept: 11, Tombstoned: 1}, report.ByType["book"])

	assertGone := func(t *testing.T, s *database.HNSWEmbeddingStore) {
		t.Helper()
		assert.NotContains(t, foundIDs(t, s, f.vecs[gone], nil), gone, "unfiltered FindSimilar skips a tombstone")
		assert.NotContains(t, foundIDs(t, s, f.vecs[gone], primaryMeta()), gone, "filtered FindSimilar skips a tombstone")
		m, err := s.Get(ctx, "book", gone)
		require.NoError(t, err)
		assert.Nil(t, m, "Get treats a tombstone as absent")
		n, err := s.CountByType(ctx, "book")
		require.NoError(t, err)
		assert.Equal(t, 11, n, "CountByType does not count a tombstone")
	}
	assertGone(t, s)
	assert.Contains(t, foundIDs(t, s, f.vecs["bruce-wayne"], nil), "bruce-wayne")

	// The tombstone survives an export, a clean stop and a fresh import.
	dir := t.TempDir()
	require.NoError(t, s.ExportWithTruth(dir, func(string) (int, error) { return 11, nil }))
	requireCleanStop(t, s, dir)
	next := newBoundImportStore(t)
	require.NoError(t, next.ImportWithStalenessCheck(dir, func(string) (int, error) { return 11, nil }))
	require.False(t, next.NeedsReconcile())
	assertGone(t, next)
}

func TestReconcile_SkipVerdictTombstonesAGraphKey(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs)
	s := importStale(t, f)
	const nonPrimary = "selina-kyle" // e.g. merged into another version during the crash window

	rows := f.indexedRows()
	setVerdict(rows, nonPrimary, database.ReconcileSkip)
	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.True(t, report.Complete)
	assert.Equal(t, database.ReconcileCounts{Rows: 12, Kept: 11, Tombstoned: 1}, report.ByType["book"])
	assert.NotContains(t, foundIDs(t, s, f.vecs[nonPrimary], nil), nonPrimary)
}

func TestReconcile_ChangedVectorAsksForRebuild(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs[:4])
	s := importStale(t, f)
	const reembedded = "selina-kyle"

	rows := f.indexedRows()
	for i := range rows {
		if rows[i].ID == reembedded {
			rows[i].Vector = exportTestVec(f.rng)
		}
	}
	rows = append(rows, database.ReconcileRow{ID: "tim-drake", Vector: exportTestVec(f.rng), Verdict: database.ReconcileIndex, Meta: primaryMeta()})

	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.Contains(t, report.RebuildReason, "different vector")
	assert.Equal(t, []string{"book/" + reembedded}, report.Changed)
	assert.False(t, report.Complete)

	// Nothing was applied: the new key was not added, and the store is still
	// pending and incomplete.
	n, err := s.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.True(t, s.NeedsReconcile())
	assert.ErrorIs(t, s.Export(t.TempDir()), database.ErrHNSWIncomplete)

	// The caller's fallback: drop everything for a hydrate from empty.
	s.DiscardForRebuild()
	assert.False(t, s.NeedsReconcile())
	n, err = s.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.ErrorIs(t, s.Export(t.TempDir()), database.ErrHNSWIncomplete)
}

// A changed vector on a key that should no longer be indexed needs no rebuild:
// the key is tombstoned.
func TestReconcile_ChangedVectorOnSkippedKeyIsTombstoned(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs)
	s := importStale(t, f)

	rows := f.indexedRows()
	for i := range rows {
		if rows[i].ID == "jim-gordon" {
			rows[i].Vector = exportTestVec(f.rng)
			rows[i].Verdict = database.ReconcileSkip
		}
	}
	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.Empty(t, report.RebuildReason)
	assert.Empty(t, report.Changed)
	assert.Equal(t, 1, report.ByType["book"].Tombstoned)
}

// A live write after the import is newer than the Pebble rows the reconcile
// read, so it wins: no Changed, no tombstone.
func TestReconcile_LiveWriteDuringPendingWins(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs[:4])
	s := importStale(t, f)

	liveVec := exportTestVec(f.rng)
	require.NoError(t, s.Upsert(ctx, "book", "tim-drake", liveVec, primaryMeta()))
	lateVec := exportTestVec(f.rng)
	require.NoError(t, s.Upsert(ctx, "book", "lucius-fox", lateVec, primaryMeta())) // not in the rows read

	rows := append(f.indexedRows(), database.ReconcileRow{
		ID: "tim-drake", Vector: exportTestVec(f.rng), Verdict: database.ReconcileIndex, Meta: primaryMeta(),
	})
	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.Empty(t, report.Changed, "a live write is not a changed vector")
	assert.Empty(t, report.RebuildReason)
	assert.True(t, report.Complete)
	assert.Equal(t, database.ReconcileCounts{Rows: 5, Kept: 4, LiveWrites: 1}, report.ByType["book"])

	got := foundIDs(t, s, liveVec, nil)
	require.NotEmpty(t, got)
	assert.Equal(t, "tim-drake", got[0], "the live vector is the one in the graph")
	assert.Contains(t, foundIDs(t, s, lateVec, nil), "lucius-fox", "a key written live after the rows were read is not tombstoned")
}

func TestReconcile_TombstoneRestoredWhenRowReturns(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs)
	s := importStale(t, f)
	const back = "edward-nygma"

	_, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": dropRow(f.indexedRows(), back)})
	require.NoError(t, err)
	require.NotContains(t, foundIDs(t, s, f.vecs[back], nil), back)

	// Export the tombstone, then boot stale again with the row back in Pebble.
	dir := t.TempDir()
	require.NoError(t, s.ExportWithTruth(dir, func(string) (int, error) { return 11, nil }))
	next := newBoundImportStore(t)
	require.NoError(t, next.ImportWithStalenessCheck(dir, func(string) (int, error) { return 12, nil }))
	require.True(t, next.NeedsReconcile())

	report, err := next.Reconcile(ctx, map[string][]database.ReconcileRow{"book": f.indexedRows()})
	require.NoError(t, err)
	assert.True(t, report.Complete)
	assert.Equal(t, database.ReconcileCounts{Rows: 12, Kept: 11, Restored: 1}, report.ByType["book"])
	got := foundIDs(t, next, f.vecs[back], primaryMeta())
	require.NotEmpty(t, got)
	assert.Equal(t, back, got[0])
}

func TestReconcile_MetadataRefreshed(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs[:4])
	s := importStale(t, f)

	rows := f.indexedRows()
	rows[0].Meta = map[string]string{"is_primary_version": "true", "series_id": "7"}
	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.Equal(t, 1, report.ByType["book"].MetaRefreshed)
	m, err := s.Get(ctx, "book", rows[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "7", m["series_id"])
}

func TestReconcile_TooManyTombstonesAsksForRebuild(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs)
	s := importStale(t, f)

	rows := dropRow(dropRow(f.indexedRows(), "harvey-dent"), "jim-gordon") // 2 of 12 > 10%
	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.Contains(t, report.RebuildReason, "tombstones are 2 of the 12 book nodes")
	assert.False(t, report.Complete)
	assert.True(t, s.NeedsReconcile())
}

func TestReconcile_UnknownVerdictKeepsKeyAndStaysIncomplete(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs[:4])
	s := importStale(t, f)
	const unsure = "dick-grayson"

	rows := f.indexedRows()
	setVerdict(rows, unsure, database.ReconcileUnknown)
	report, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": rows})
	require.NoError(t, err)
	assert.Empty(t, report.RebuildReason)
	assert.False(t, report.Complete, "a row the classifier could not decide keeps the store incomplete")
	assert.Equal(t, database.ReconcileCounts{Rows: 4, Kept: 3, Unknown: 1}, report.ByType["book"])
	assert.False(t, s.NeedsReconcile())
	assert.Contains(t, foundIDs(t, s, f.vecs[unsure], nil), unsure, "an undecided graph key is left in place")
	assert.ErrorIs(t, s.Export(t.TempDir()), database.ErrHNSWIncomplete)
}

func TestReconcile_RequiresEveryEntityType(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs[:4])
	// Add an author graph to the snapshot.
	src := database.NewHNSWEmbeddingStore(exportTestDim)
	require.NoError(t, src.Import(f.dir))
	require.NoError(t, src.Upsert(ctx, "author", "alfred-pennyworth", exportTestVec(f.rng), nil))
	require.NoError(t, src.ExportWithTruth(f.dir, func(string) (int, error) { return 100, nil }))
	s := importStale(t, f)

	_, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": f.indexedRows()})
	require.ErrorContains(t, err, `entity type "author"`)
	assert.True(t, s.NeedsReconcile(), "nothing is applied when a type is missing")
}

func TestReconcile_NotPending(t *testing.T) {
	s := newExportTestStore(t, 2)
	_, err := s.Reconcile(context.Background(), map[string][]database.ReconcileRow{})
	assert.ErrorIs(t, err, database.ErrNoReconcilePending)
}

func TestReconcile_CancelledLeavesPending(t *testing.T) {
	f := newStaleFixture(t, batmanIDs[:4])
	s := importStale(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Reconcile(ctx, map[string][]database.ReconcileRow{"book": f.indexedRows()})
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, s.NeedsReconcile())
	assert.ErrorIs(t, s.Export(t.TempDir()), database.ErrHNSWIncomplete)
}

// Reconcile reads the graph's keys from the metadata sidecar, so a stale
// snapshot whose metadata does not match the graph is discarded instead.
func TestImport_StaleSnapshotWithMismatchedMetaIsDiscarded(t *testing.T) {
	ctx := context.Background()
	f := newStaleFixture(t, batmanIDs[:4])
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "hnsw-book.meta.json"), []byte(`{"bruce-wayne":{}}`), 0o644))

	s := newBoundImportStore(t)
	require.NoError(t, s.ImportWithStalenessCheck(f.dir, func(string) (int, error) { return 101, nil }))
	assert.False(t, s.NeedsReconcile())
	n, err := s.CountByType(ctx, "book")
	require.NoError(t, err)
	assert.Zero(t, n, "discarded: the hydrate from Pebble starts from empty")
}

func TestUpsert_RejectsReservedMetadataKey(t *testing.T) {
	s := database.NewHNSWEmbeddingStore(exportTestDim)
	err := s.Upsert(context.Background(), "book", "bruce-wayne", exportTestVec(rand.New(rand.NewSource(1))),
		map[string]string{"__hnsw_tombstone": "1"})
	assert.ErrorContains(t, err, "reserved")
}
