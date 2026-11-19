// file: internal/plugins/dedup/kinds_embedding_test.go
// version: 1.0.0
// guid: 985da553-5e06-479c-8367-c003bdb6d793
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// embeddingSpy is the store and writer a test hands the embedding kinds. It
// implements every method of embeddingKindStore and embeddingKindWriter itself
// (the compiler checks that where embeddingFakeDeps returns it), so a method
// added to either interface breaks this file until the spy answers it.
type embeddingSpy struct {
	mu      sync.Mutex
	writers []tasks.Writer
	calls   []string

	// embeddings are the book embeddings ListByType answers; live are the book
	// ids GetBookByID resolves (every other id is a deleted book).
	embeddings []database.Embedding
	live       map[string]bool
	lookupErr  map[string]error
	deleteErr  map[string]error
}

func (s *embeddingSpy) rec(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *embeddingSpy) ListByType(entityType string) ([]database.Embedding, error) {
	return append([]database.Embedding(nil), s.embeddings...), nil
}

func (s *embeddingSpy) GetBookByID(id string) (*database.Book, error) {
	if err := s.lookupErr[id]; err != nil {
		return nil, err
	}
	if s.live[id] {
		return &database.Book{ID: id}, nil
	}
	return nil, nil
}

func (s *embeddingSpy) Delete(entityType, entityID string) error {
	s.rec("Delete " + entityType + " " + entityID)
	return s.deleteErr[entityID]
}

func (s *embeddingSpy) factory(w tasks.Writer) embeddingKindWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *embeddingSpy) seenWriters() []tasks.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tasks.Writer(nil), s.writers...)
}

func (s *embeddingSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// newEmbeddingSpy seeds two live books and two deleted ones (Batman comic
// titles as ids, rule 15).
func newEmbeddingSpy() (*embeddingSpy, EmbeddingKindDeps) {
	s := &embeddingSpy{
		embeddings: []database.Embedding{
			{EntityType: "book", EntityID: "year-one", Model: "bge-m3"},
			{EntityType: "book", EntityID: "long-halloween", Model: "old-model"},
			{EntityType: "book", EntityID: "dark-victory", Model: "old-model"},
			{EntityType: "book", EntityID: "hush", Model: "bge-m3"},
		},
		live:      map[string]bool{"year-one": true, "long-halloween": true},
		lookupErr: map[string]error{},
		deleteErr: map[string]error{},
	}
	return s, EmbeddingKindDeps{Store: s, NewWriter: s.factory}
}

func embeddingFakeDeps(t *testing.T) EmbeddingKindDeps {
	t.Helper()
	_, d := newEmbeddingSpy()
	return d
}

func embeddingRows() []kindRow {
	return []kindRow{
		{
			HeadID: "dedup.cleanup-orphan-embeddings", Title: "Clean up orphaned book embeddings",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 30 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks},
			Par: []string{"cpu"}, ParGot: []tasks.Parallelism{tasks.CPU()},
			HeadKey: "dedup.cleanup-orphan-embeddings",
		},
	}
}

func embeddingWriteProbes(t *testing.T) []writeProbe {
	t.Helper()
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).cleanupOrphanEmbeddingsNative(d, nil)
	return []writeProbe{{Kind: def.Kind(), Def: def, Writers: spy.seenWriters, Calls: spy.recorded}}
}

// --- group tests ---

// A Preview run is HEAD's dry run: the scan reports, nothing is deleted, and no
// writer is even built.
func TestDedupEmbedding_CleanupOrphanPreviewDeletesNothingAndBuildsNoWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).cleanupOrphanEmbeddingsNative(d, nil)

	h, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters(), "a Preview run builds no writer")
	last := h.lastNote()
	require.True(t, strings.HasPrefix(last, "Dry-run — 2 orphaned book embeddings found (2 live, 0 lookup errors, 4 total)"), last)
}

// A Live run deletes exactly the rows the scan proved orphaned, in report order,
// through the run's writer.
func TestDedupEmbedding_CleanupOrphanLiveDeletesOnlyOrphansThroughTheWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).cleanupOrphanEmbeddingsNative(d, nil)
	w := &sentinelWriter{}

	_, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Delete book dark-victory", "Delete book hush"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
}

// A row whose lookup errored is never touched, and a failed delete is counted and
// logged, not fatal (HEAD's behaviour).
func TestDedupEmbedding_CleanupOrphanSkipsLookupErrorsAndCountsDeleteErrors(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.lookupErr["dark-victory"] = errors.New("pebble hiccup")
	spy.deleteErr["hush"] = errors.New("disk full")
	def := (&Plugin{}).cleanupOrphanEmbeddingsNative(d, nil)

	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Delete book hush"}, spy.recorded(), "the lookup-error row is left untouched")
	last := h.lastNote()
	require.Contains(t, last, "deleted 0/1 orphaned embeddings (1 errors)")
}

func TestDedupEmbedding_CleanupOrphanRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).cleanupOrphanEmbeddingsNative(d, nil)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, `{"apply":false}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	require.Empty(t, spy.recorded())

	unwired := (&Plugin{}).cleanupOrphanEmbeddingsNative(EmbeddingKindDeps{Store: spy}, nil)
	_, err = runKind(t, unwired, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Embedding.NewWriter is not set")
	noStore := (&Plugin{}).cleanupOrphanEmbeddingsNative(EmbeddingKindDeps{}, nil)
	_, err = runKind(t, noStore, tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Embedding.Store is not set")
}

// The native scan fans out over the declared pool (CPU), as HEAD's did; the host
// records the worker count the loop declared.
func TestDedupEmbedding_CleanupOrphanScanUsesTheCPUPool(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).cleanupOrphanEmbeddingsNative(d, nil)
	h := newKindHost(def, tasks.ModePreview, &sentinelWriter{})
	var declared []int
	h.capObserver = func(n int) { declared = append(declared, n) }
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []int{tasks.CPU().Workers()}, declared)
	_ = spy
}

// The v2 shell still runs the same core: params, nil checks and results as before.
func TestDedupEmbedding_CleanupOrphanHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runCleanupOrphanEmbeddings(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
	_ = time.Second
}
