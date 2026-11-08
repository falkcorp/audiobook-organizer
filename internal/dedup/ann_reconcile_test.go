// file: internal/dedup/ann_reconcile_test.go
// version: 1.0.0
// guid: b41e9a6b-d62b-4ab5-97f2-4498e245117b
// last-edited: 2026-11-08

package dedup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// Review B2: PostInit reconciles a stale HNSW snapshot instead of rebuilding
// it, and rebuilds only when an indexed vector changed.

const annTestDim = 8

// Batman storylines stand in for book IDs.
var annStorylines = []string{
	"year-one", "the-long-halloween", "dark-victory", "the-killing-joke",
	"hush", "knightfall", "no-mans-land", "the-court-of-owls",
	"a-death-in-the-family", "a-lonely-place-of-dying", "the-dark-knight-returns", "arkham-asylum",
	"gothic", "venom", "the-cult", "son-of-the-demon",
	"batman-rip", "war-games", "cataclysm", "contagion",
}

// syncBuffer is an io.Writer safe for the reconcile goroutine to log into
// while the test reads it after the goroutine is joined.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLogs routes the default slog logger (which internal/logger writes
// through) into a buffer for the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// annFixture is a dedup engine over a real EmbeddingStore, a book table the
// mock serves from a map, and an exported HNSW snapshot of the starting rows.
type annFixture struct {
	engine *Engine
	es     *database.EmbeddingStore
	books  map[string]*database.Book
	vecs   map[string][]float32
	rng    *rand.Rand
	dir    string
	// lookupErr, when set for an ID, is what GetBookByID returns for it.
	lookupErr map[string]error
}

func newANNFixture(t *testing.T, snapshotIDs []string) *annFixture {
	t.Helper()
	engine, mock, es := setupTestEngine(t)
	engine.embedClient = ai.NewEmbeddingClientWithOptions("k", "bge-m3", "")
	f := &annFixture{
		engine: engine, es: es,
		books: map[string]*database.Book{}, vecs: map[string][]float32{},
		rng: rand.New(rand.NewSource(5)), dir: t.TempDir(), lookupErr: map[string]error{},
	}
	// Read-only once PostInit starts: the reconcile classifies rows on a
	// worker pool, so this closure must not write shared state.
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if err := f.lookupErr[id]; err != nil {
			return nil, err
		}
		return f.books[id], nil
	}

	src := database.NewHNSWEmbeddingStore(annTestDim)
	for _, id := range snapshotIDs {
		f.putRow(t, id)
		if err := src.Upsert(context.Background(), "book", id, f.vecs[id], bookANNMeta(f.books[id])); err != nil {
			t.Fatal(err)
		}
	}
	src.SetComplete(true)
	if err := src.ExportWithTruth(f.dir, f.es.CountByType); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *annFixture) vec() []float32 {
	v := make([]float32, annTestDim)
	for i := range v {
		v[i] = f.rng.Float32() + 0.01
	}
	return v
}

// putRow gives id a primary book and a fresh current-model embedding row.
func (f *annFixture) putRow(t *testing.T, id string) {
	t.Helper()
	primary := true
	f.books[id] = &database.Book{ID: id, Title: id, IsPrimaryVersion: &primary}
	f.vecs[id] = f.vec()
	if err := f.es.Upsert(database.Embedding{EntityType: "book", EntityID: id, Vector: f.vecs[id], Model: "bge-m3"}); err != nil {
		t.Fatal(err)
	}
}

// deleteRow removes id's book and embedding row.
func (f *annFixture) deleteRow(t *testing.T, id string) {
	t.Helper()
	delete(f.books, id)
	if err := f.es.Delete("book", id); err != nil {
		t.Fatal(err)
	}
}

// boot imports the snapshot the way NewServer does, then runs PostInit and
// joins its background work.
func (f *annFixture) boot(t *testing.T) *database.HNSWEmbeddingStore {
	t.Helper()
	ctx := context.Background()
	store := database.NewHNSWEmbeddingStore(annTestDim)
	if err := store.ImportWithStalenessCheck(f.dir, f.es.CountByType); err != nil {
		t.Fatal(err)
	}
	c := serviceregistry.NewContainer().Override("chromemstore", store)
	if err := c.Build(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.engine.Stop(context.Background()) })
	if err := f.engine.PostInit(ctx, c); err != nil {
		t.Fatal(err)
	}
	f.engine.bgWg.Wait()
	return store
}

func nearest(t *testing.T, s *database.HNSWEmbeddingStore, q []float32) []string {
	t.Helper()
	res, err := s.FindSimilar(context.Background(), "book", q, 5, map[string]string{"is_primary_version": "true"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(res))
	for i, r := range res {
		ids[i] = r.EntityID
	}
	return ids
}

// tombstonesOnDisk exports s and counts the tombstoned keys in its metadata.
// A full rebuild leaves none, so a nonzero count proves the reconcile ran.
func tombstonesOnDisk(t *testing.T, s *database.HNSWEmbeddingStore) int {
	t.Helper()
	dir := t.TempDir()
	if err := s.Export(dir); err != nil {
		t.Fatalf("export the reconciled store: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "hnsw-book.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "__hnsw_tombstone")
}

func TestPostInit_ReconcilesStaleSnapshot(t *testing.T) {
	logs := captureLogs(t)
	f := newANNFixture(t, annStorylines) // 20 books in the snapshot
	// The crash window: two books embedded, one deleted, one merged into
	// another version (its embedding row stays; the book is non-primary).
	f.putRow(t, "zero-year")
	f.putRow(t, "endgame")
	f.deleteRow(t, "hush")
	notPrimary := false
	f.books["venom"].IsPrimaryVersion = &notPrimary

	store := f.boot(t)

	if store.NeedsReconcile() {
		t.Fatal("the stale snapshot is still pending after PostInit")
	}
	for _, id := range []string{"zero-year", "endgame"} {
		if got := nearest(t, store, f.vecs[id]); len(got) == 0 || got[0] != id {
			t.Errorf("replayed key %s: nearest = %v", id, got)
		}
	}
	for _, id := range []string{"hush", "venom"} {
		if got := nearest(t, store, f.vecs[id]); slices.Contains(got, id) {
			t.Errorf("%s should be tombstoned, FindSimilar returned it: %v", id, got)
		}
	}
	if n, _ := store.CountByType(context.Background(), "book"); n != 20 {
		t.Errorf("CountByType(book) = %d, want 20 (22 nodes, 2 tombstones)", n)
	}
	if got := tombstonesOnDisk(t, store); got != 2 {
		t.Errorf("tombstones in the exported metadata = %d, want 2 (0 means it was rebuilt, not reconciled)", got)
	}
	if out := logs.String(); !strings.Contains(out, "vector ANN snapshot reconciled against Pebble") {
		t.Errorf("no reconcile summary logged:\n%s", out)
	}
}

func TestPostInit_ChangedVectorFallsBackToRebuild(t *testing.T) {
	logs := captureLogs(t)
	f := newANNFixture(t, annStorylines[:4])
	// Re-embedded during the crash window, plus one new row so the count moves.
	f.putRow(t, "the-killing-joke")
	f.putRow(t, "zero-year")

	store := f.boot(t)

	if store.NeedsReconcile() {
		t.Fatal("still pending after the rebuild")
	}
	if got := nearest(t, store, f.vecs["the-killing-joke"]); len(got) == 0 || got[0] != "the-killing-joke" {
		t.Errorf("the rebuilt graph lacks the new vector: nearest = %v", got)
	}
	if n, _ := store.CountByType(context.Background(), "book"); n != 5 {
		t.Errorf("CountByType(book) = %d, want 5", n)
	}
	if got := tombstonesOnDisk(t, store); got != 0 {
		t.Errorf("a rebuilt graph has no tombstones, found %d", got)
	}
	out := logs.String()
	if !strings.Contains(out, "cannot be reconciled in place") || !strings.Contains(out, "book/the-killing-joke") {
		t.Errorf("the rebuild must be logged with the key that forced it:\n%s", out)
	}
}

func TestPostInit_ReconcileLookupErrorStaysIncomplete(t *testing.T) {
	logs := captureLogs(t)
	f := newANNFixture(t, annStorylines[:4])
	f.putRow(t, "zero-year")
	f.lookupErr["year-one"] = errors.New("pebble: injected fault")

	store := f.boot(t)

	if store.NeedsReconcile() {
		t.Fatal("a finished reconcile is no longer pending")
	}
	if got := nearest(t, store, f.vecs["year-one"]); len(got) == 0 || got[0] != "year-one" {
		t.Errorf("a key whose lookup failed must stay in the graph: nearest = %v", got)
	}
	if got := nearest(t, store, f.vecs["zero-year"]); len(got) == 0 || got[0] != "zero-year" {
		t.Errorf("the missing key is still replayed: nearest = %v", got)
	}
	if err := store.Export(t.TempDir()); !errors.Is(err, database.ErrHNSWIncomplete) {
		t.Errorf("a reconcile with lookup errors must not be exported: Export = %v", err)
	}
	if out := logs.String(); !strings.Contains(out, "1 book lookup error(s)") {
		t.Errorf("the incomplete reason is not logged:\n%s", out)
	}
}

func TestPostInit_LazyModeDiscardsStaleSnapshot(t *testing.T) {
	prev := config.AppConfig.Dedup.ChromemLazy
	config.AppConfig.Dedup.ChromemLazy = true
	t.Cleanup(func() { config.AppConfig.Dedup.ChromemLazy = prev })
	f := newANNFixture(t, annStorylines[:4])
	f.putRow(t, "zero-year")

	store := f.boot(t)

	if store.NeedsReconcile() {
		t.Error("lazy mode leaves nothing pending")
	}
	if n, _ := store.CountByType(context.Background(), "book"); n != 0 {
		t.Errorf("lazy mode discards a stale snapshot as before: CountByType(book) = %d", n)
	}
}
