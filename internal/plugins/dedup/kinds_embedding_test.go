// file: internal/plugins/dedup/kinds_embedding_test.go
// version: 1.1.2
// guid: 985da553-5e06-479c-8367-c003bdb6d793
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// embeddingSpy is the store, writer and engine a test hands the embedding kinds.
// It implements every method of embeddingKindStore, embeddingKindWriter and
// embeddingKindEngine itself (the compiler checks that where embeddingFakeDeps
// returns it), so a method added to any of them breaks this file until the spy
// answers it. Only writes are recorded in calls (reads change nothing).
type embeddingSpy struct {
	mu      sync.Mutex
	writers []tasks.Writer
	built   int // NewWriter factory calls (a Preview run makes none)
	calls   []string

	// embeddings and authorEmbeddings are what ListByType answers per type; live
	// are the book ids GetBookByID resolves (every other id is a deleted book).
	embeddings       []database.Embedding
	authorEmbeddings []database.Embedding
	live             map[string]bool
	lookupErr        map[string]error
	deleteErr        map[string]error

	authors    []database.Author
	authorsErr error

	// books is the library GetAllBooksCore pages; stored the embedding rows Get
	// answers, keyed "<type>/<id>".
	books    []database.BookCore
	booksErr error
	stored   map[string]*database.Embedding

	examples    []database.LabeledExample
	examplesErr error

	candidates    []database.DedupCandidate
	candidatesErr error
	files         map[string][]database.BookFile
	upsertErr     map[int64]error

	settings    map[string]string
	scanRows    [][2][]byte
	reencodeErr error

	// engine behaviour
	embedErr      map[string]error
	embedBooksErr error
	asyncCount    int
	asyncErr      error
	scoreErr      error
	scoreNone     bool
}

func (s *embeddingSpy) rec(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

// --- store reads ---

func (s *embeddingSpy) ListByType(entityType string) ([]database.Embedding, error) {
	if entityType == "author" {
		return append([]database.Embedding(nil), s.authorEmbeddings...), nil
	}
	return append([]database.Embedding(nil), s.embeddings...), nil
}

func (s *embeddingSpy) GetBookByID(id string) (*database.Book, error) {
	if err := s.lookupErr[id]; err != nil {
		return nil, err
	}
	if s.live[id] {
		return &database.Book{ID: id, Title: id, FilePath: "/audio/" + id + ".m4b"}, nil
	}
	return nil, nil
}

func (s *embeddingSpy) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return append([]database.BookFile(nil), s.files[bookID]...), nil
}

func (s *embeddingSpy) GetAllAuthors() ([]database.Author, error) {
	return append([]database.Author(nil), s.authors...), s.authorsErr
}

func (s *embeddingSpy) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if s.booksErr != nil {
		return nil, s.booksErr
	}
	if offset >= len(s.books) {
		return nil, nil
	}
	end := len(s.books)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return append([]database.BookCore(nil), s.books[offset:end]...), nil
}

func (s *embeddingSpy) Get(entityType, entityID string) (*database.Embedding, error) {
	e := s.stored[entityType+"/"+entityID]
	if e == nil {
		return nil, nil
	}
	c := *e
	return &c, nil
}

func (s *embeddingSpy) ListLabeledExamples(f database.LabeledExampleFilter) ([]database.LabeledExample, error) {
	if s.examplesErr != nil {
		return nil, s.examplesErr
	}
	var out []database.LabeledExample
	for _, ex := range s.examples {
		if f.Label == "" || ex.Label == f.Label {
			out = append(out, ex)
		}
	}
	return out, nil
}

func (s *embeddingSpy) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	if s.candidatesErr != nil {
		return nil, 0, s.candidatesErr
	}
	return append([]database.DedupCandidate(nil), s.candidates...), len(s.candidates), nil
}

func (s *embeddingSpy) GetSetting(key string) (*database.Setting, error) {
	if v, ok := s.settings[key]; ok {
		return &database.Setting{Key: key, Value: v}, nil
	}
	return nil, nil
}

func (s *embeddingSpy) ScanEmbeddingRows(fn func(key, value []byte) error) error {
	for _, r := range s.scanRows {
		if err := fn(r[0], r[1]); err != nil {
			return err
		}
	}
	return nil
}

// --- writer ---

func (s *embeddingSpy) Delete(entityType, entityID string) error {
	s.rec("Delete " + entityType + " " + entityID)
	return s.deleteErr[entityID]
}

func (s *embeddingSpy) UpsertLabeledExample(ex database.LabeledExample) error {
	s.rec("UpsertLabeledExample " + strconv.FormatInt(ex.CandidateID, 10) + " " + ex.Label)
	return s.upsertErr[ex.CandidateID]
}

func (s *embeddingSpy) ReclassifyCandidate(id int64, fromStatus, toStatus string) error {
	s.rec("ReclassifyCandidate " + strconv.FormatInt(id, 10) + " " + fromStatus + "->" + toStatus)
	return nil
}

func (s *embeddingSpy) UpdateCandidateScores(updates []database.CandidateScoreUpdate) (int, []int64, error) {
	s.rec("UpdateCandidateScores " + strconv.Itoa(len(updates)))
	return len(updates), nil, nil
}

func (s *embeddingSpy) SyncCandidateWrites() error {
	s.rec("SyncCandidateWrites")
	return nil
}

func (s *embeddingSpy) ReencodeRows(chunk []reencodeTarget) error {
	s.rec("ReencodeRows " + strconv.Itoa(len(chunk)))
	return s.reencodeErr
}

func (s *embeddingSpy) SetSetting(key, value, typ string, isSecret bool) error {
	s.rec("SetSetting " + key + "=" + value)
	return nil
}

// --- engine ---

func (s *embeddingSpy) EmbedBook(ctx context.Context, bookID string) (dedupengine.EmbedStatus, error) {
	s.rec("EmbedBook " + bookID)
	if err := s.embedErr[bookID]; err != nil {
		return dedupengine.EmbedStatusSkippedEmptyTitle, err
	}
	return dedupengine.EmbedStatusEmbedded, nil
}

func (s *embeddingSpy) EmbedBooksAsync(ctx context.Context) (string, int, error) {
	s.rec("EmbedBooksAsync")
	return "batch-dark-knight", s.asyncCount, s.asyncErr
}

func (s *embeddingSpy) EmbedConcurrency(fallback int) int { return fallback }

func (s *embeddingSpy) EmbedBooks(ctx context.Context, bookIDs []string) (map[string]dedupengine.EmbedStatus, error) {
	s.rec("EmbedBooks " + strings.Join(bookIDs, ","))
	if s.embedBooksErr != nil {
		return nil, s.embedBooksErr
	}
	out := make(map[string]dedupengine.EmbedStatus, len(bookIDs))
	for _, id := range bookIDs {
		out[id] = dedupengine.EmbedStatusEmbedded
	}
	return out, nil
}

func (s *embeddingSpy) ScorePairsForBook(ctx context.Context, aID string, inputs []dedupengine.RescorePairInput) ([]dedupengine.RescorePairResult, error) {
	if s.scoreErr != nil {
		return nil, s.scoreErr
	}
	out := make([]dedupengine.RescorePairResult, len(inputs))
	for i, in := range inputs {
		out[i] = dedupengine.RescorePairResult{OtherID: in.OtherID}
		if s.scoreNone {
			continue
		}
		out[i].NumSignals = 1
		out[i].Score = &unified.UnifiedDedupScore{
			Pair:    [2]string{aID, in.OtherID},
			Score:   4.0,
			Formula: "noisy_or_v1",
			Signals: []unified.Signal{{Kind: unified.SigDuration, Raw: 1.0, Evidence: "duration match"}},
		}
	}
	return out, nil
}

// --- factories ---

func (s *embeddingSpy) factory(w tasks.Writer) embeddingKindWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	s.built++
	return s
}

func (s *embeddingSpy) engineFactory(w tasks.Writer) embeddingKindEngine {
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

// writersBuilt is how many times the NewWriter factory ran.
func (s *embeddingSpy) writersBuilt() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.built
}

func (s *embeddingSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// embeddingVec is a stored book embedding of the given model (Batman comic titles
// as ids, rule 15).
func embeddingVec(id, model string, v ...float32) *database.Embedding {
	return &database.Embedding{EntityType: "book", EntityID: id, Model: model, Vector: v}
}

// reencodeRowFixture is one raw emb:v: row in the legacy float32 layout.
func reencodeRowFixture(id string, v ...float32) [2][]byte {
	val, _ := json.Marshal(struct {
		V []byte `json:"v"`
	}{V: rawV0Blob(v)})
	return [2][]byte{[]byte("emb:v:book:" + id), val}
}

// newEmbeddingSpy seeds a small library of Batman comic titles: four book
// embeddings (two live books, two deleted), three author embeddings (one live
// author), a three-book library with one stale-model embedding, labeled pairs,
// pending candidates and legacy rows. Every kind of the group finds work in it.
func newEmbeddingSpy() (*embeddingSpy, EmbeddingKindDeps) {
	s := &embeddingSpy{
		embeddings: []database.Embedding{
			{EntityType: "book", EntityID: "year-one", Model: "bge-m3"},
			{EntityType: "book", EntityID: "long-halloween", Model: "old-model"},
			{EntityType: "book", EntityID: "dark-victory", Model: "old-model"},
			{EntityType: "book", EntityID: "hush", Model: "bge-m3"},
		},
		authorEmbeddings: []database.Embedding{
			{EntityType: "author", EntityID: "1", Model: "bge-m3"},
			{EntityType: "author", EntityID: "2", Model: "bge-m3"},
			{EntityType: "author", EntityID: "3", Model: "old-model"},
		},
		live:      map[string]bool{"year-one": true, "long-halloween": true, "joker-stub": true, "batman-full": true},
		lookupErr: map[string]error{},
		deleteErr: map[string]error{},
		authors:   []database.Author{{ID: 1, Name: "Selina Kyle"}},
		books: []database.BookCore{
			{ID: "year-one", Title: "Batman Year One"},
			{ID: "long-halloween", Title: "The Long Halloween"},
			{ID: "hush", Title: "Batman Hush"},
		},
		stored: map[string]*database.Embedding{
			"book/year-one":       embeddingVec("year-one", "bge-m3", 1, 0),
			"book/long-halloween": embeddingVec("long-halloween", "old-model", 0, 1),
			"book/dark-victory":   embeddingVec("dark-victory", "bge-m3", 1, 0),
		},
		examples: []database.LabeledExample{
			// scored: same model, same dimension
			{CandidateID: 1, EntityAID: "year-one", EntityBID: "dark-victory", Label: "true_dup"},
			// skipped: long-halloween is stored under another model
			{CandidateID: 2, EntityAID: "year-one", EntityBID: "long-halloween", Label: "not_dup"},
			// skipped: hush has no stored embedding
			{CandidateID: 3, EntityAID: "dark-victory", EntityBID: "hush", Label: "not_dup"},
		},
		candidates: []database.DedupCandidate{
			{ID: 7, EntityType: "book", EntityAID: "joker-stub", EntityBID: "batman-full", Layer: "exact", Status: "pending"},
		},
		files: map[string][]database.BookFile{
			"joker-stub":  {{BookID: "joker-stub", FilePath: "/audio/joker-stub.m4b", FileSize: 32}},
			"batman-full": {{BookID: "batman-full", FilePath: "/audio/batman-full.m4b", FileSize: 5 << 20, Duration: database.Secs(3600)}},
		},
		upsertErr: map[int64]error{},
		settings:  map[string]string{},
		scanRows: [][2][]byte{
			reencodeRowFixture("year-one", 0.1, 0.2, 0.3, 0.4),
			reencodeRowFixture("hush", 0.4, 0.3, 0.2, 0.1),
		},
		embedErr:   map[string]error{},
		asyncCount: 3,
	}
	return s, EmbeddingKindDeps{
		Store:          s,
		NewWriter:      s.factory,
		NewEngine:      s.engineFactory,
		EmbeddingModel: func() string { return "bge-m3" },
	}
}

func embeddingFakeDeps(t *testing.T) EmbeddingKindDeps {
	t.Helper()
	_, d := newEmbeddingSpy()
	return d
}

// useOpenAIEmbeddingBackend points the process config at an OpenAI embedding
// backend (the only one dedup.embed-async runs under) until the test ends.
func useOpenAIEmbeddingBackend(t *testing.T) {
	t.Helper()
	prevKey, prevURL, prevOn := config.AppConfig.OpenAIAPIKey, config.AppConfig.Embedding.BaseURL, config.AppConfig.Embedding.Enabled
	t.Cleanup(func() {
		config.AppConfig.OpenAIAPIKey, config.AppConfig.Embedding.BaseURL, config.AppConfig.Embedding.Enabled = prevKey, prevURL, prevOn
	})
	config.AppConfig.OpenAIAPIKey = "sk-test-key"
	config.AppConfig.Embedding.BaseURL = ""
	config.AppConfig.Embedding.Enabled = true
}

func embeddingRows() []kindRow {
	embedScanPar := "fixed(4): network-bound against rate-limited embedding endpoints; sized from the routed pool (Engine.EmbedConcurrency), default 4"
	return []kindRow{
		{
			HeadID: "dedup.embed-scan", Title: "Embed all books",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 120 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResAuthors, ops.ResSeries},
			Par: []string{embedScanPar}, ParGot: []tasks.Parallelism{embedScanParallelism(embedScanConcurrency)},
			HeadKey: "dedup.embed-scan",
		},
		{
			HeadID: "dedup.embed-async", Title: "Embed books async (batch API) [deprecated — use embed-scan with async:true]",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 10 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResAuthors, ops.ResSeries},
			Par: []string{embedScanPar}, ParGot: []tasks.Parallelism{embedScanParallelism(embedScanConcurrency)},
			HeadKey: "dedup.embed-async",
		},
		{
			HeadID: "dedup.emb-reencode", Title: "Re-encode embeddings to float16+zstd (T021)",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 30 * time.Minute,
			Writes:  []ops.Resource{ops.ResEmbeddings, ops.ResSettings},
			HeadKey: "dedup.emb-reencode",
		},
		{
			HeadID: "dedup.reembed-embeddings", Title: "Re-embed corpus (embedding model change)",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 12 * time.Hour,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResAuthors, ops.ResSeries},
			Par: []string{"cpu"}, ParGot: []tasks.Parallelism{tasks.CPU()},
			HeadKey: "dedup.reembed-embeddings",
		},
		{
			HeadID: "dedup.cleanup-orphan-embeddings", Title: "Clean up orphaned book embeddings",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 30 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks},
			Par: []string{"cpu"}, ParGot: []tasks.Parallelism{tasks.CPU()},
			HeadKey: "dedup.cleanup-orphan-embeddings",
		},
		{
			HeadID: "dedup.cleanup-orphan-author-embeddings", Title: "Clean up orphaned author embeddings",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 10 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResAuthors},
			HeadKey: "dedup.cleanup-orphan-author-embeddings",
		},
		{
			HeadID: "dedup.calibrate-embedding-thresholds", Title: "Calibrate embedding thresholds (report only)",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 30 * time.Minute,
			// No Par: HEAD scored the pairs in one plain loop, which stays one (R4).
			ReadOnly: true, Reads: []ops.Resource{ops.ResBooks, ops.ResEmbeddings},
			HeadKey: "dedup.calibrate-embedding-thresholds",
		},
		{
			HeadID: "dedup.breakdown-backfill", Title: "Backfill ScoreBreakdowns onto pre-T015 pending candidates",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResAuthors, ops.ResBookFiles},
			Par: []string{"cpu"}, ParGot: []tasks.Parallelism{breakdownBackfillParallelism()},
			HeadKey: "dedup.breakdown-backfill",
		},
		{
			HeadID: "dedup.dataset-backfill", Title: "Backfill dedup tuning dataset",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResBookFiles},
			Par: []string{"cpu"}, ParGot: []tasks.Parallelism{datasetBackfillParallelism()},
			HeadKey: "dedup.dataset-backfill",
		},
	}
}

// embeddingWriteProbes: one probe per kind that declares writes (calibrate is
// ReadOnly and so has none). Each gets its own spy so its calls are its own.
func embeddingWriteProbes(t *testing.T) []writeProbe {
	t.Helper()
	useOpenAIEmbeddingBackend(t)
	var probes []writeProbe
	add := func(build func(p *Plugin, d EmbeddingKindDeps) tasks.Definition) {
		spy, d := newEmbeddingSpy()
		def := build(&Plugin{}, d)
		probes = append(probes, writeProbe{Kind: def.Kind(), Def: def, Writers: spy.seenWriters, Calls: spy.recorded})
	}
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition { return p.embedScanNative(d, nil) })
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition { return p.embedAsyncNative(d, nil) })
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition { return p.embReencodeNative(d, nil) })
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition { return p.reembedEmbeddingsNative(d, nil) })
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition { return p.cleanupOrphanEmbeddingsNative(d, nil) })
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition {
		return p.cleanupOrphanAuthorEmbeddingsNative(d, nil)
	})
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition { return p.breakdownBackfillNative(d, nil) })
	add(func(p *Plugin, d EmbeddingKindDeps) tasks.Definition { return p.datasetBackfillNative(d, nil) })
	return probes
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

// --- shared helpers for the group tests below ---

const embeddingModeKeysRefusal = "the run's mode comes from the start request"

// embeddingRun runs def on a fresh host and returns the host and the run's error.
func embeddingRun(t *testing.T, def tasks.Definition, mode tasks.Mode, params string) (*kindHost, error) {
	t.Helper()
	return runKind(t, def, mode, &sentinelWriter{}, params)
}

// embeddingWorkerCounts runs def in mode and returns every worker count a pool of
// the run declared.
func embeddingWorkerCounts(t *testing.T, def tasks.Definition, mode tasks.Mode, params string) []int {
	t.Helper()
	h := newKindHost(def, mode, &sentinelWriter{})
	var declared []int
	var mu sync.Mutex
	h.capObserver = func(n int) {
		mu.Lock()
		declared = append(declared, n)
		mu.Unlock()
	}
	require.NoError(t, runKindOn(t, def, h, params))
	return declared
}

func sortedCalls(c []string) []string {
	out := append([]string(nil), c...)
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// --- dedup.embed-scan ---

// A Live run embeds every book through the run's engine, fanned out over HEAD's
// pool (EmbedConcurrency's answer, 4), and reports what it counted.
func TestDedupEmbedding_EmbedScanLiveEmbedsEveryBookThroughTheEngine(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.embedErr["hush"] = errors.New("backend hiccup")
	def := (&Plugin{}).embedScanNative(d, nil)
	w := &sentinelWriter{}

	h := newKindHost(def, tasks.ModeLive, w)
	var declared []int
	h.capObserver = func(n int) { declared = append(declared, n) }
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []string{"EmbedBook hush", "EmbedBook long-halloween", "EmbedBook year-one"}, sortedCalls(spy.recorded()))
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Equal(t, []int{embedScanConcurrency}, declared)
	require.Contains(t, h.lastNote(), "Embedding complete — 2 new, 0 cached, 0 skipped, 1 errors (of 3 books)")
}

// wideEmbedEngine is a routed pool wider than tasks.Fixed's ceiling whose
// EmbedBook blocks until every book is in flight at once.
type wideEmbedEngine struct {
	width   int
	arrived atomic.Int64
	allIn   chan struct{}
}

func (e *wideEmbedEngine) EmbedBook(context.Context, string) (dedupengine.EmbedStatus, error) {
	if e.arrived.Add(1) == int64(e.width) {
		close(e.allIn)
	}
	select {
	case <-e.allIn:
		return dedupengine.EmbedStatusEmbedded, nil
	case <-time.After(10 * time.Second):
		return dedupengine.EmbedStatusSkippedEmptyTitle, fmt.Errorf("only %d books in flight, want %d", e.arrived.Load(), e.width)
	}
}

func (e *wideEmbedEngine) EmbedBooksAsync(context.Context) (string, int, error) { return "", 0, nil }
func (e *wideEmbedEngine) EmbedConcurrency(int) int                             { return e.width }

type wideEmbedStore struct{ books []database.BookCore }

// GetAllBooksCore(0, 0) is "every book", the only call the embed core makes.
func (s wideEmbedStore) GetAllBooksCore(int, int) ([]database.BookCore, error) { return s.books, nil }

// HEAD applied no upper bound to its routed embed pool; tasks.Fixed clamps at 64,
// so HEAD's shell must still run every worker the engine asks for (n1).
func TestDedupEmbedding_EmbedScanHeadShellKeepsAPoolWiderThanTheFixedCeiling(t *testing.T) {
	width := tasks.MaxFixed + 16
	books := make([]database.BookCore, width)
	for i := range books {
		books[i] = database.BookCore{ID: "dark-knight-" + strconv.Itoa(i), Title: "The Dark Knight Returns"}
	}
	eng := &wideEmbedEngine{width: width, allIn: make(chan struct{})}
	res, err := embedScanCore(context.Background(), embedScanEnv{
		kindEnv: headEnv(&mockReporter{}, true),
		Store:   wideEmbedStore{books: books},
		Engine:  eng,
	})
	require.NoError(t, err)
	require.Equal(t, width, res.Embedded, "every book was in flight at once")
	require.Zero(t, res.Errors)
	require.Equal(t, tasks.MaxFixed, embedScanParallelism(width).Workers(), "the native pool keeps the kernel's ceiling")
}

// async=true under an OpenAI backend submits one batch instead of embedding.
func TestDedupEmbedding_EmbedScanAsyncSubmitsOneBatch(t *testing.T) {
	useOpenAIEmbeddingBackend(t)
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embedScanNative(d, nil)
	h, err := embeddingRun(t, def, tasks.ModeLive, `{"async":true}`)
	require.NoError(t, err)
	require.Equal(t, []string{"EmbedBooksAsync"}, spy.recorded())
	require.Contains(t, h.lastNote(), "Submitted 3 books to OpenAI Batch API (batch_id=batch-dark-knight)")
}

// HEAD has no preview for embed-scan, so a Preview run is refused before any
// engine or writer is built.
func TestDedupEmbedding_EmbedScanPreviewIsRefusedAndBuildsNothing(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embedScanNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.ErrorContains(t, err, "preview not supported")
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
}

func TestDedupEmbedding_EmbedScanRefusesModeKeys(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embedScanNative(d, nil)
	for _, k := range []string{`{"apply":true}`, `{"dry_run":false}`, `{"dryRun":true}`} {
		_, err := embeddingRun(t, def, tasks.ModeLive, k)
		require.ErrorContains(t, err, embeddingModeKeysRefusal, k)
	}
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_EmbedScanErrorPaths(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.booksErr = errors.New("pebble gone")
	def := (&Plugin{}).embedScanNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "load books: pebble gone")
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_EmbedScanRefusesUnwiredDeps(t *testing.T) {
	_, d := newEmbeddingSpy()
	def := (&Plugin{}).embedScanNative(EmbeddingKindDeps{NewEngine: d.NewEngine}, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.Store is not set")
	def = (&Plugin{}).embedScanNative(EmbeddingKindDeps{Store: d.Store}, nil)
	_, err = embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.NewEngine is not set")
	def = (&Plugin{}).embedScanNative(EmbeddingKindDeps{Store: d.Store, NewEngine: func(tasks.Writer) embeddingKindEngine { return nil }}, nil)
	_, err = embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "the engine factory returned no engine")
}

// The v2 shell keeps its nil-engine refusal: a plugin with no engine hands the
// core a nil interface, not a typed nil.
func TestDedupEmbedding_EmbedScanHeadShellKeepsItsNilEngineRefusal(t *testing.T) {
	err := (&Plugin{}).runEmbedScan(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available (embedding may be disabled or API key not configured)")
}

// --- dedup.embed-async ---

func TestDedupEmbedding_EmbedAsyncLiveSubmitsABatchUnderAnOpenAIBackend(t *testing.T) {
	useOpenAIEmbeddingBackend(t)
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embedAsyncNative(d, nil)
	w := &sentinelWriter{}
	h, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"EmbedBooksAsync"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "batch_id=batch-dark-knight")
}

// Under any backend but OpenAI the deprecated op does nothing, as HEAD's.
func TestDedupEmbedding_EmbedAsyncSkipsWithoutAnOpenAIBackend(t *testing.T) {
	useOpenAIEmbeddingBackend(t)
	config.AppConfig.OpenAIAPIKey = ""
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embedAsyncNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_EmbedAsyncPreviewIsRefusedAndBuildsNothing(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embedAsyncNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.ErrorContains(t, err, "preview not supported")
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
}

func TestDedupEmbedding_EmbedAsyncRefusesModeKeys(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embedAsyncNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"apply":true}`)
	require.ErrorContains(t, err, embeddingModeKeysRefusal)
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_EmbedAsyncSubmitFailureIsTheRunsError(t *testing.T) {
	useOpenAIEmbeddingBackend(t)
	spy, d := newEmbeddingSpy()
	spy.asyncErr = errors.New("quota exhausted")
	def := (&Plugin{}).embedAsyncNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "submit embedding batch: quota exhausted")
}

func TestDedupEmbedding_EmbedAsyncRefusesUnwiredDeps(t *testing.T) {
	_, d := newEmbeddingSpy()
	def := (&Plugin{}).embedAsyncNative(EmbeddingKindDeps{NewEngine: d.NewEngine}, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.Store is not set")
	def = (&Plugin{}).embedAsyncNative(EmbeddingKindDeps{Store: d.Store}, nil)
	_, err = embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.NewEngine is not set")
}

// --- dedup.emb-reencode ---

// Preview is HEAD's dry run: it scans and reports, writes nothing, builds no
// writer.
func TestDedupEmbedding_EmbReencodePreviewReportsAndBuildsNoWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embReencodeNative(d, nil)
	h, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Zero(t, spy.writersBuilt())
	require.Empty(t, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Dry-run")
}

func TestDedupEmbedding_EmbReencodeLiveRewritesLegacyRowsAndSetsTheFlag(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embReencodeNative(d, nil)
	w := &sentinelWriter{}
	_, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"ReencodeRows 2", "SetSetting emb_f16_v1_done=true"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
}

// A Live run that finds the done flag set does nothing (HEAD's double-run guard).
func TestDedupEmbedding_EmbReencodeLiveWithTheFlagSetDoesNothing(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.settings["emb_f16_v1_done"] = "true"
	def := (&Plugin{}).embReencodeNative(d, nil)
	h, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Contains(t, h.lastNote(), "Already completed")
}

func TestDedupEmbedding_EmbReencodeRefusesModeKeys(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).embReencodeNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"apply":true}`)
	require.ErrorContains(t, err, embeddingModeKeysRefusal)
	require.Empty(t, spy.recorded())
}

// A failed batch commit is the run's error and the done flag is not set.
func TestDedupEmbedding_EmbReencodeBatchFailureIsTheRunsErrorAndLeavesTheFlagUnset(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.reencodeErr = errors.New("disk full")
	def := (&Plugin{}).embReencodeNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "emb-reencode: batch write: disk full")
	require.Equal(t, []string{"ReencodeRows 2"}, spy.recorded())
}

func TestDedupEmbedding_EmbReencodeRefusesUnwiredDeps(t *testing.T) {
	spy, _ := newEmbeddingSpy()
	def := (&Plugin{}).embReencodeNative(EmbeddingKindDeps{}, nil)
	_, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.ErrorContains(t, err, "Embedding.Store is not set")
	def = (&Plugin{}).embReencodeNative(EmbeddingKindDeps{Store: spy}, nil)
	_, err = embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.NewWriter is not set")
	// a Preview run needs no writer factory
	_, err = embeddingRun(t, def, tasks.ModePreview, "")
	require.NoError(t, err)
}

// --- dedup.reembed-embeddings ---

// Preview is HEAD's dry run: it classifies the library (a CPU-wide pass) and
// builds neither a writer nor an engine.
func TestDedupEmbedding_ReembedPreviewClassifiesAndBuildsNothing(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).reembedEmbeddingsNative(d, nil)
	require.Equal(t, []int{tasks.CPU().Workers()}, embeddingWorkerCounts(t, def, tasks.ModePreview, ""))
	h, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.Contains(t, h.lastNote(), `Dry-run — 2 books would be re-embedded with "bge-m3" (1 already current)`)
}

func TestDedupEmbedding_ReembedLiveDeletesStaleRowsThenEmbedsThroughTheEngine(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).reembedEmbeddingsNative(d, nil)
	w := &sentinelWriter{}
	h, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Delete book long-halloween", "Delete book hush", "EmbedBooks long-halloween,hush"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w, w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), `Complete — 2 embedded, 0 skipped of 2 candidates (target model "bge-m3")`)
}

// batch_size is the one param the native run keeps: a batch of one embeds twice.
func TestDedupEmbedding_ReembedBatchSizeSplitsTheEmbedCalls(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).reembedEmbeddingsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"batch_size":1}`)
	require.NoError(t, err)
	require.Equal(t, []string{
		"Delete book long-halloween", "EmbedBooks long-halloween",
		"Delete book hush", "EmbedBooks hush",
	}, spy.recorded())
}

func TestDedupEmbedding_ReembedRefusesModeKeys(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).reembedEmbeddingsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"apply":true,"batch_size":5}`)
	require.ErrorContains(t, err, embeddingModeKeysRefusal)
	require.Empty(t, spy.recorded())
}

// A whole-batch failure is the run's error, after the stale rows were dropped.
func TestDedupEmbedding_ReembedBatchFailureIsTheRunsError(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.embedBooksErr = errors.New("ollama unreachable")
	def := (&Plugin{}).reembedEmbeddingsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "embed batch at offset 0 (2 books): ollama unreachable")
}

func TestDedupEmbedding_ReembedRefusesAMissingTargetModel(t *testing.T) {
	_, d := newEmbeddingSpy()
	d.EmbeddingModel = func() string { return "" }
	def := (&Plugin{}).reembedEmbeddingsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.ErrorContains(t, err, "no embedding client configured")
}

func TestDedupEmbedding_ReembedRefusesUnwiredDeps(t *testing.T) {
	_, d := newEmbeddingSpy()
	cases := map[string]EmbeddingKindDeps{
		"Embedding.Store is not set":          {NewWriter: d.NewWriter, NewEngine: d.NewEngine, EmbeddingModel: d.EmbeddingModel},
		"Embedding.EmbeddingModel is not set": {Store: d.Store, NewWriter: d.NewWriter, NewEngine: d.NewEngine},
		"Embedding.NewWriter is not set":      {Store: d.Store, NewEngine: d.NewEngine, EmbeddingModel: d.EmbeddingModel},
		"Embedding.NewEngine is not set":      {Store: d.Store, NewWriter: d.NewWriter, EmbeddingModel: d.EmbeddingModel},
	}
	for want, deps := range cases {
		_, err := embeddingRun(t, (&Plugin{}).reembedEmbeddingsNative(deps, nil), tasks.ModeLive, "")
		require.ErrorContains(t, err, want)
	}
}

func TestDedupEmbedding_ReembedHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runReembedEmbeddings(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available (embeddings disabled?)")
}

// --- dedup.cleanup-orphan-author-embeddings ---

func TestDedupEmbedding_CleanupOrphanAuthorPreviewDeletesNothingAndBuildsNoWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).cleanupOrphanAuthorEmbeddingsNative(d, nil)
	h, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Zero(t, spy.writersBuilt())
	require.Contains(t, h.lastNote(), "Dry-run")
	require.Contains(t, h.lastNote(), "2 orphaned")
}

func TestDedupEmbedding_CleanupOrphanAuthorLiveDeletesOnlyOrphansThroughTheWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.deleteErr["3"] = errors.New("disk full")
	def := (&Plugin{}).cleanupOrphanAuthorEmbeddingsNative(d, nil)
	w := &sentinelWriter{}
	h, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Delete author 2", "Delete author 3"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "deleted 1/2 orphaned")
}

func TestDedupEmbedding_CleanupOrphanAuthorRefusesModeKeys(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).cleanupOrphanAuthorEmbeddingsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"apply":false}`)
	require.ErrorContains(t, err, embeddingModeKeysRefusal)
	require.Empty(t, spy.recorded())
}

// An author enumeration failure must stop the run before any delete: with no live
// set every author embedding would look orphaned.
func TestDedupEmbedding_CleanupOrphanAuthorEnumerationFailureDeletesNothing(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.authorsErr = errors.New("pebble gone")
	def := (&Plugin{}).cleanupOrphanAuthorEmbeddingsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "get all authors: pebble gone")
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_CleanupOrphanAuthorRefusesUnwiredDeps(t *testing.T) {
	spy, _ := newEmbeddingSpy()
	_, err := embeddingRun(t, (&Plugin{}).cleanupOrphanAuthorEmbeddingsNative(EmbeddingKindDeps{}, nil), tasks.ModePreview, "")
	require.ErrorContains(t, err, "Embedding.Store is not set")
	_, err = embeddingRun(t, (&Plugin{}).cleanupOrphanAuthorEmbeddingsNative(EmbeddingKindDeps{Store: spy}, nil), tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.NewWriter is not set")
}

func TestDedupEmbedding_CleanupOrphanAuthorHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runCleanupOrphanAuthorEmbeddings(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}

// --- dedup.calibrate-embedding-thresholds ---

// The report is read-only: it is handed no writer, scores the labeled pairs over
// the CPU pool, and skips the pairs it cannot compare.
func TestDedupEmbedding_CalibrateReportsWithoutAWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).calibrateEmbeddingThresholdsNative(d, nil)
	// The scoring pass is HEAD's plain loop: it starts no pool (R4).
	require.Empty(t, embeddingWorkerCounts(t, def, tasks.ModeLive, ""))
	h, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Calibration report complete")
}

func TestDedupEmbedding_CalibratePreviewIsRefused(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).calibrateEmbeddingThresholdsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.ErrorContains(t, err, "preview not supported")
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_CalibrateRefusesModeKeys(t *testing.T) {
	_, d := newEmbeddingSpy()
	def := (&Plugin{}).calibrateEmbeddingThresholdsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"apply":true}`)
	require.ErrorContains(t, err, embeddingModeKeysRefusal)
}

func TestDedupEmbedding_CalibrateErrorPaths(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.examplesErr = errors.New("pebble gone")
	def := (&Plugin{}).calibrateEmbeddingThresholdsNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "list true_dup examples: pebble gone")

	_, d = newEmbeddingSpy()
	d.EmbeddingModel = func() string { return "" }
	_, err = embeddingRun(t, (&Plugin{}).calibrateEmbeddingThresholdsNative(d, nil), tasks.ModeLive, "")
	require.ErrorContains(t, err, "could not resolve a target embedding model")
}

func TestDedupEmbedding_CalibrateRefusesUnwiredDeps(t *testing.T) {
	_, err := embeddingRun(t, (&Plugin{}).calibrateEmbeddingThresholdsNative(EmbeddingKindDeps{}, nil), tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.Store is not set")
	// no model param and no wired embed client: HEAD's refusal
	_, d := newEmbeddingSpy()
	d.EmbeddingModel = nil
	_, err = embeddingRun(t, (&Plugin{}).calibrateEmbeddingThresholdsNative(d, nil), tasks.ModeLive, "")
	require.ErrorContains(t, err, "model param empty and dedup engine not available")
	// ...but an explicit model needs none
	_, err = embeddingRun(t, (&Plugin{}).calibrateEmbeddingThresholdsNative(d, nil), tasks.ModeLive, `{"model":"bge-m3"}`)
	require.NoError(t, err)
}

// frameReporter is a reporter that records every progress frame it is handed.
type frameReporter struct {
	mockReporter
	mu     sync.Mutex
	frames []string
}

func (r *frameReporter) UpdateProgress(cur, total int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, fmt.Sprintf("%d/%d %s", cur, total, msg))
	return nil
}

// calibrate-embedding-thresholds ran its scoring pass as a plain loop on one core
// at HEAD, so the HEAD shell reports exactly the four phase frames and nothing
// between them: no per-pair frame, no pool. A parallelism or progress change is a
// separate PR (05-tasks 5T.8.2, rule R4), so this pins HEAD's output.
func TestDedupEmbedding_CalibrateHeadShellReportsOnlyItsPhaseFrames(t *testing.T) {
	spy, _ := newEmbeddingSpy()
	for i := range 40 {
		a, b := fmt.Sprintf("issue-%03d", i), fmt.Sprintf("annual-%03d", i)
		spy.stored["book/"+a] = embeddingVec(a, "bge-m3", 1, float32(i%7), 0.5)
		spy.stored["book/"+b] = embeddingVec(b, "bge-m3", float32(i%5), 1, 0.25)
		spy.examples = append(spy.examples, database.LabeledExample{CandidateID: int64(i), EntityAID: a, EntityBID: b, Label: "true_dup"})
	}
	rep := &frameReporter{}
	_, err := calibrateEmbeddingThresholdsCore(context.Background(), calibrateEmbeddingThresholdsEnv{
		kindEnv: headEnv(rep, false),
		Params:  calibrateEmbeddingThresholdsParams{Model: "bge-m3"},
		Store:   spy,
	})
	require.NoError(t, err)
	require.Len(t, rep.frames, 4, "one frame per phase: %v", rep.frames)
	require.Equal(t, "0/3 Loading labeled examples…", rep.frames[0])
	require.Equal(t, fmt.Sprintf("1/3 Scoring %d labeled pairs…", len(spy.examples)), rep.frames[1])
	require.Equal(t, "2/3 Sweeping candidate thresholds…", rep.frames[2])
	require.True(t, strings.HasPrefix(rep.frames[3], "3/3 Calibration report complete"), rep.frames[3])
}

func TestDedupEmbedding_CalibrateHeadShellKeepsItsNilCheck(t *testing.T) {
	err := (&Plugin{}).runCalibrateEmbeddingThresholds(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}

// --- dedup.breakdown-backfill ---

// Preview is HEAD's dry run: the scoring pass runs, nothing is written, and no
// writer is built (the engine the scorer needs is built over the Preview run's
// own writer, which only records intent).
func TestDedupEmbedding_BreakdownPreviewScoresButWritesNothingAndBuildsNoWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).breakdownBackfillNative(d, nil)
	require.Equal(t, []int{tasks.CPU().Workers()}, embeddingWorkerCounts(t, def, tasks.ModePreview, ""))
	h, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Zero(t, spy.writersBuilt())
	require.Contains(t, h.lastNote(), "Dry-run — ")
}

func TestDedupEmbedding_BreakdownLiveWritesTheScoresThenSyncs(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).breakdownBackfillNative(d, nil)
	w := &sentinelWriter{}
	h, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"UpdateCandidateScores 1", "SyncCandidateWrites"}, spy.recorded())
	for _, got := range spy.seenWriters() {
		require.Same(t, w, got)
	}
	require.Contains(t, h.lastNote(), "Complete — ")
	require.Contains(t, h.lastNote(), "backfilled=1")
}

// A scorer failure is counted, not fatal, and nothing is persisted for the group.
func TestDedupEmbedding_BreakdownScorerFailureIsCountedNotFatal(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.scoreErr = errors.New("scorer down")
	def := (&Plugin{}).breakdownBackfillNative(d, nil)
	h, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Contains(t, h.lastNote(), "score_errs=1")
}

func TestDedupEmbedding_BreakdownRefusesModeKeys(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).breakdownBackfillNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"apply":true}`)
	require.ErrorContains(t, err, embeddingModeKeysRefusal)
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_BreakdownListFailureIsTheRunsError(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.candidatesErr = errors.New("pebble gone")
	def := (&Plugin{}).breakdownBackfillNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "list pending candidates: pebble gone")
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_BreakdownRefusesUnwiredDeps(t *testing.T) {
	_, d := newEmbeddingSpy()
	cases := map[string]EmbeddingKindDeps{
		"Embedding.Store is not set":     {NewWriter: d.NewWriter, NewEngine: d.NewEngine},
		"Embedding.NewEngine is not set": {Store: d.Store, NewWriter: d.NewWriter},
		"Embedding.NewWriter is not set": {Store: d.Store, NewEngine: d.NewEngine},
	}
	for want, deps := range cases {
		_, err := embeddingRun(t, (&Plugin{}).breakdownBackfillNative(deps, nil), tasks.ModeLive, "")
		require.ErrorContains(t, err, want)
	}
}

func TestDedupEmbedding_BreakdownHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runBreakdownBackfill(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- dedup.dataset-backfill ---

func TestDedupEmbedding_DatasetPreviewClassifiesButWritesNothingAndBuildsNoWriter(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).datasetBackfillNative(d, nil)
	require.Equal(t, []int{tasks.CPU().Workers()}, embeddingWorkerCounts(t, def, tasks.ModePreview, ""))
	h, err := embeddingRun(t, def, tasks.ModePreview, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Zero(t, spy.writersBuilt())
	require.Contains(t, h.lastNote(), "Dry-run complete — 1 not_dup, 0 true_dup of 1 examined")
}

// A stub-side pair is rule-labeled not_dup, written, and its candidate dismissed.
func TestDedupEmbedding_DatasetLiveLabelsThenDismissesTheStubPair(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).datasetBackfillNative(d, nil)
	w := &sentinelWriter{}
	h, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"UpsertLabeledExample 7 not_dup", "ReclassifyCandidate 7 pending->dismissed"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Complete — 1/1 labeled, 1 suppressed")
}

// A candidate whose label write failed stays pending, so it is retried next run.
func TestDedupEmbedding_DatasetFailedUpsertLeavesTheCandidatePending(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.upsertErr[7] = errors.New("disk full")
	def := (&Plugin{}).datasetBackfillNative(d, nil)
	h, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.NoError(t, err)
	require.Equal(t, []string{"UpsertLabeledExample 7 not_dup"}, spy.recorded())
	require.Contains(t, h.lastNote(), "upsert_errs=1")
}

func TestDedupEmbedding_DatasetRefusesModeKeys(t *testing.T) {
	spy, d := newEmbeddingSpy()
	def := (&Plugin{}).datasetBackfillNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, `{"apply":true}`)
	require.ErrorContains(t, err, embeddingModeKeysRefusal)
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_DatasetListFailureIsTheRunsError(t *testing.T) {
	spy, d := newEmbeddingSpy()
	spy.candidatesErr = errors.New("pebble gone")
	def := (&Plugin{}).datasetBackfillNative(d, nil)
	_, err := embeddingRun(t, def, tasks.ModeLive, "")
	require.ErrorContains(t, err, "list candidates: pebble gone")
	require.Empty(t, spy.recorded())
}

func TestDedupEmbedding_DatasetRefusesUnwiredDeps(t *testing.T) {
	spy, _ := newEmbeddingSpy()
	_, err := embeddingRun(t, (&Plugin{}).datasetBackfillNative(EmbeddingKindDeps{}, nil), tasks.ModePreview, "")
	require.ErrorContains(t, err, "Embedding.Store is not set")
	_, err = embeddingRun(t, (&Plugin{}).datasetBackfillNative(EmbeddingKindDeps{Store: spy}, nil), tasks.ModeLive, "")
	require.ErrorContains(t, err, "Embedding.NewWriter is not set")
}

func TestDedupEmbedding_DatasetHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runDatasetBackfill(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}
