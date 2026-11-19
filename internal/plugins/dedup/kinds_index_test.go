// file: internal/plugins/dedup/kinds_index_test.go
// version: 1.1.3
// guid: 37db46af-5d7a-4d7a-9acd-1cd87ae9dd07
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// indexSpy is the store and writer a test hands the index kinds. It implements
// every method of indexKindStore and indexKindWriter itself (the compiler checks
// that where indexFakeDeps returns it), so a method added to either interface
// breaks this file until the spy answers it. Fixture ids are Batman comic titles.
type indexSpy struct {
	mu      sync.Mutex
	writers []tasks.Writer
	calls   []string

	books      []database.BookCore
	candidates []database.DedupCandidate
	files      []database.BookFileCore
	windowIDs  []string
	hasWindow  map[string]bool
	settings   map[string]string

	sweepDry, sweepLive database.SweepBookFileSegDropResult

	booksErr, candidatesErr, sweepErr, membersErr error
	writeErr                                      map[string]error
}

func (s *indexSpy) rec(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

// --- reads ---

func (s *indexSpy) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	if s.booksErr != nil {
		return nil, s.booksErr
	}
	if offset >= len(s.books) {
		return nil, nil
	}
	end := min(offset+limit, len(s.books))
	return append([]database.BookCore(nil), s.books[offset:end]...), nil
}
func (s *indexSpy) IsISBNIndexBuilt() bool { return false }
func (s *indexSpy) ListCandidates(database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	if s.candidatesErr != nil {
		return nil, 0, s.candidatesErr
	}
	return append([]database.DedupCandidate(nil), s.candidates...), len(s.candidates), nil
}
func (s *indexSpy) SweepBookFileSegDropDry(context.Context, int, func(rewrite, total int)) (database.SweepBookFileSegDropResult, error) {
	if s.sweepErr != nil {
		return database.SweepBookFileSegDropResult{}, s.sweepErr
	}
	return s.sweepDry, nil
}
func (s *indexSpy) GetSetting(key string) (*database.Setting, error) {
	if v, ok := s.settings[key]; ok {
		return &database.Setting{Key: key, Value: v}, nil
	}
	return nil, nil
}
func (s *indexSpy) GetAllBookFilesCore() ([]database.BookFileCore, error) { return s.files, nil }
func (s *indexSpy) GetBookByID(id string) (*database.Book, error)         { return nil, nil }
func (s *indexSpy) ListWindowIndexFiles() ([]string, error)               { return s.windowIDs, nil }
func (s *indexSpy) WindowIndexMembers(id string) ([]fingerprint.WindowBand, bool, error) {
	if s.membersErr != nil {
		return nil, false, s.membersErr
	}
	return nil, s.hasWindow[id], nil
}

// --- writes ---

func (s *indexSpy) WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error {
	s.rec(fmt.Sprintf("WriteISBN %s %s %s %s", bookID, isbn10, isbn13, asin))
	return s.writeErr[bookID]
}
func (s *indexSpy) SetISBNIndexBuilt() error { s.rec("SetISBNIndexBuilt"); return nil }
func (s *indexSpy) WriteCandidateStatusIndexRow(id int64, status string) error {
	s.rec(fmt.Sprintf("WriteStatus %d %s", id, status))
	return s.writeErr[fmt.Sprint(id)]
}
func (s *indexSpy) SetCandidateStatusIndexBuilt() error {
	s.rec("SetCandidateStatusIndexBuilt")
	return nil
}
func (s *indexSpy) SweepBookFileSegDrop(context.Context, int, func(rewrite, total int)) (database.SweepBookFileSegDropResult, error) {
	s.rec("Sweep")
	if s.sweepErr != nil {
		return database.SweepBookFileSegDropResult{}, s.sweepErr
	}
	return s.sweepLive, nil
}
func (s *indexSpy) SetSetting(key, value, dataType string, internal bool) error {
	s.rec("SetSetting " + key + "=" + value)
	return nil
}
func (s *indexSpy) ReclassifyCandidate(id int64, from, to string) error {
	s.rec(fmt.Sprintf("Reclassify %d %s %s", id, from, to))
	return nil
}
func (s *indexSpy) PutWindowIndex(id string) (int, error) {
	s.rec("PutWindow " + id)
	return 24, s.writeErr[id]
}
func (s *indexSpy) SetWindowIndexBuilt() error { s.rec("SetWindowIndexBuilt"); return nil }

func (s *indexSpy) factory(w tasks.Writer) indexKindWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *indexSpy) seenWriters() []tasks.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tasks.Writer(nil), s.writers...)
}

// recorded is the writes in a stable order (the pools run in any order).
func (s *indexSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.calls...)
	sort.Strings(out)
	return out
}

func strp(s string) *string { return &s }

func newIndexSpy() (*indexSpy, IndexKindDeps) {
	one := 1.0
	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := &indexSpy{
		books: []database.BookCore{
			{ID: "year-one", ISBN10: strp("0930289560"), ISBN13: strp("9780930289560"), FilePath: "/lib/year-one.m4b"},
			{ID: "long-halloween", ASIN: strp("B000HALLOW"), FilePath: "/lib/long-halloween.m4b"},
			{ID: "hush", FilePath: "/lib/hush.m4b"},
			{ID: "dark-victory", FilePath: "/lib/hush.m4b"},
		},
		candidates: []database.DedupCandidate{
			{ID: 1, EntityAID: "year-one", EntityBID: "long-halloween", Layer: "exact", Similarity: &one, Status: "pending", CreatedAt: before},
			{ID: 2, EntityAID: "hush", EntityBID: "dark-victory", Layer: "exact", Similarity: &one, Status: "pending", CreatedAt: before},
			{ID: 3, EntityAID: "year-one", EntityBID: "hush", Layer: "embedding", Status: ""},
		},
		windowIDs: []string{"f-one", "f-two", "f-three"},
		hasWindow: map[string]bool{"f-one": true},
		settings:  map[string]string{},
		sweepDry:  database.SweepBookFileSegDropResult{Total: 10, Rewrite: 4, Skipped: 6},
		sweepLive: database.SweepBookFileSegDropResult{Total: 10, Rewrite: 4, Skipped: 6},
		writeErr:  map[string]error{},
	}
	return s, IndexKindDeps{Store: s, NewWriter: s.factory}
}

func indexFakeDeps(t *testing.T) IndexKindDeps {
	t.Helper()
	_, d := newIndexSpy()
	return d
}

func indexRows() []kindRow {
	return []kindRow{
		{
			HeadID: "dedup.build-isbn-index", Title: "Build ISBN/ASIN secondary index",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityHigh, Timeout: 60 * time.Minute,
			Writes:  []ops.Resource{ops.ResBooks, ops.ResSettings},
			HeadKey: "dedup.build-isbn-index",
		},
		{
			HeadID: "dedup.build-candidate-status-index", Title: "Build candidate status secondary index",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings},
			Par:    []string{"cpu"}, ParGot: []tasks.Parallelism{tasks.CPU()},
			HeadKey: "dedup.build-candidate-status-index",
		},
		{
			HeadID: "dedup.bookfile-seg-drop", Title: "Drop AcoustID segment fields from BookFile values",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 60 * time.Minute,
			Writes:  []ops.Resource{ops.ResBookFiles, ops.ResSettings},
			HeadKey: "dedup.bookfile-seg-drop",
		},
		{
			HeadID: "dedup.purge-legacy-fp-candidates", Title: "Purge legacy fingerprint candidates",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityHigh, Timeout: 30 * time.Minute, NoWarmup: true,
			Writes:  []ops.Resource{ops.ResEmbeddings, ops.ResSettings},
			Reads:   []ops.Resource{ops.ResBooks, ops.ResBookFiles},
			HeadKey: "dedup.purge-legacy-fp-candidates",
		},
		{
			HeadID: "dedup.window-index-build", Title: "Build window-print index",
			HeadResume: sdk.ResumeRestart, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 180 * time.Minute,
			Writes: []ops.Resource{ops.ResFingerprintIndex, ops.ResSettings},
			Par: []string{fmt.Sprintf("fixed(%d): one worker per CPU and never fewer than two, never the sequential default RunItems falls back to",
				min(max(runtime.NumCPU(), 2), tasks.MaxFixed))},
			ParGot:  []tasks.Parallelism{windowIndexBuildParallelism()},
			HeadKey: "dedup.window-index",
		},
	}
}

// indexNative lists the five native definitions over d.
func indexNative(d IndexKindDeps) map[string]tasks.Definition {
	p := &Plugin{}
	return map[string]tasks.Definition{
		"isbn":   p.buildISBNIndexNative(d, nil),
		"status": p.buildCandidateStatusIndexNative(d, nil),
		"seg":    p.bookfileSegDropNative(d, nil),
		"purge":  p.purgeLegacyFPNative(d, nil),
		"window": p.windowIndexBuildNative(d, nil),
	}
}

func indexWriteProbes(t *testing.T) []writeProbe {
	t.Helper()
	var out []writeProbe
	for _, name := range []string{"isbn", "status", "seg", "purge", "window"} {
		spy, d := newIndexSpy()
		def := indexNative(d)[name]
		out = append(out, writeProbe{Kind: def.Kind(), Def: def, Writers: spy.seenWriters, Calls: spy.recorded})
	}
	return out
}

// --- group tests ---

func TestDedupIndex_ListsFiveKinds(t *testing.T) {
	_, d := newIndexSpy()
	require.Len(t, indexKinds(&Plugin{}, d, nil), 5)
}

// --- build-isbn-index ---

func TestDedupIndex_ISBNPreviewWritesNothingAndBuildsNoWriter(t *testing.T) {
	spy, d := newIndexSpy()
	h, err := runKind(t, indexNative(d)["isbn"], tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Dry-run complete — 3 index rows would be written")
}

func TestDedupIndex_ISBNLiveWritesRowsAndTheFlagThroughTheWriter(t *testing.T) {
	spy, d := newIndexSpy()
	w := &sentinelWriter{}
	_, err := runKind(t, indexNative(d)["isbn"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{
		"SetISBNIndexBuilt",
		"WriteISBN long-halloween   B000HALLOW",
		"WriteISBN year-one 0930289560 9780930289560 ",
	}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
}

func TestDedupIndex_ISBNFailedWriteLeavesTheFlagUnset(t *testing.T) {
	spy, d := newIndexSpy()
	spy.writeErr["year-one"] = errors.New("disk full")
	h, err := runKind(t, indexNative(d)["isbn"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.NotContains(t, spy.recorded(), "SetISBNIndexBuilt")
	require.Contains(t, h.lastNote(), "1/2 books indexed (1 failed)")
}

func TestDedupIndex_ISBNScanErrorFailsTheRun(t *testing.T) {
	spy, d := newIndexSpy()
	spy.booksErr = errors.New("pebble hiccup")
	_, err := runKind(t, indexNative(d)["isbn"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "get all books at offset 0")
}

func TestDedupIndex_ISBNRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newIndexSpy()
	_, err := runKind(t, indexNative(d)["isbn"], tasks.ModeLive, &sentinelWriter{}, `{"apply":true}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	_, err = runKind(t, indexNative(IndexKindDeps{Store: spy})["isbn"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.NewWriter is not set")
	_, err = runKind(t, indexNative(IndexKindDeps{})["isbn"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.Store is not set")
}

func TestDedupIndex_ISBNHeadShellKeepsItsNilCheck(t *testing.T) {
	err := (&Plugin{}).runBuildISBNIndex(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "main store not available")
}

// --- build-candidate-status-index ---

func TestDedupIndex_StatusRefusesPreview(t *testing.T) {
	spy, d := newIndexSpy()
	_, err := runKind(t, indexNative(d)["status"], tasks.ModePreview, &sentinelWriter{}, "")
	require.Error(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
}

func TestDedupIndex_StatusLiveIndexesNonEmptyStatusesAndSetsTheFlag(t *testing.T) {
	spy, d := newIndexSpy()
	w := &sentinelWriter{}
	h, err := runKind(t, indexNative(d)["status"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{
		"SetCandidateStatusIndexBuilt",
		"WriteStatus 1 pending",
		"WriteStatus 2 pending",
	}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "2 written, 1 skipped (empty status), 0 errors (of 3 candidates)")
}

func TestDedupIndex_StatusWriteErrorIsCountedNotFatal(t *testing.T) {
	spy, d := newIndexSpy()
	spy.writeErr["2"] = errors.New("disk full")
	h, err := runKind(t, indexNative(d)["status"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Contains(t, h.lastNote(), "1 written, 1 skipped (empty status), 1 errors")
}

func TestDedupIndex_StatusListErrorFailsTheRun(t *testing.T) {
	spy, d := newIndexSpy()
	spy.candidatesErr = errors.New("pebble hiccup")
	_, err := runKind(t, indexNative(d)["status"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "list candidates")
}

func TestDedupIndex_StatusRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newIndexSpy()
	_, err := runKind(t, indexNative(d)["status"], tasks.ModeLive, &sentinelWriter{}, `{"dryRun":true}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	_, err = runKind(t, indexNative(IndexKindDeps{Store: spy})["status"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.NewWriter is not set")
	_, err = runKind(t, indexNative(IndexKindDeps{})["status"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.Store is not set")
}

func TestDedupIndex_StatusUsesTheCPUPool(t *testing.T) {
	_, d := newIndexSpy()
	def := indexNative(d)["status"]
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	var declared []int
	h.capObserver = func(n int) { declared = append(declared, n) }
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []int{tasks.CPU().Workers()}, declared)
}

func TestDedupIndex_StatusHeadShellKeepsItsNilCheck(t *testing.T) {
	err := (&Plugin{}).runBuildCandidateStatusIndex(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}

// --- bookfile-seg-drop ---

func TestDedupIndex_SegPreviewSweepsDryAndBuildsNoWriter(t *testing.T) {
	spy, d := newIndexSpy()
	h, err := runKind(t, indexNative(d)["seg"], tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Dry-run complete — 4 of 10 rows would be rewritten")
}

func TestDedupIndex_SegLiveSweepsAndSetsTheFlag(t *testing.T) {
	spy, d := newIndexSpy()
	w := &sentinelWriter{}
	h, err := runKind(t, indexNative(d)["seg"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"SetSetting " + bookfileSegDropDoneFlag + "=true", "Sweep"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Complete — 4 rows rewritten")
}

func TestDedupIndex_SegConflictsLeaveTheFlagUnset(t *testing.T) {
	spy, d := newIndexSpy()
	spy.sweepLive.Conflicts = 2
	_, err := runKind(t, indexNative(d)["seg"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Sweep"}, spy.recorded())
}

func TestDedupIndex_SegSkipsWhenAlreadyDone(t *testing.T) {
	spy, d := newIndexSpy()
	spy.settings[bookfileSegDropDoneFlag] = "true"
	h, err := runKind(t, indexNative(d)["seg"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Contains(t, h.lastNote(), "Already completed")
}

func TestDedupIndex_SegSweepErrorFailsTheRun(t *testing.T) {
	spy, d := newIndexSpy()
	spy.sweepErr = errors.New("pebble hiccup")
	_, err := runKind(t, indexNative(d)["seg"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "bookfile-seg-drop: sweep")
}

func TestDedupIndex_SegRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newIndexSpy()
	_, err := runKind(t, indexNative(d)["seg"], tasks.ModeLive, &sentinelWriter{}, `{"apply":true}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	_, err = runKind(t, indexNative(IndexKindDeps{Store: spy})["seg"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.NewWriter is not set")
	_, err = runKind(t, indexNative(IndexKindDeps{})["seg"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.Store is not set")
}

// --- purge-legacy-fp-candidates ---

func TestDedupIndex_PurgePreviewMarksNothingAndBuildsNoWriter(t *testing.T) {
	spy, d := newIndexSpy()
	h, err := runKind(t, indexNative(d)["purge"], tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Dry-run complete — 1 candidates would be marked stale-fp")
}

func TestDedupIndex_PurgeLiveMarksOnlyTheNonSamePathStaleRows(t *testing.T) {
	spy, d := newIndexSpy()
	w := &sentinelWriter{}
	_, err := runKind(t, indexNative(d)["purge"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Reclassify 1 pending stale-fp", "SetSetting " + purgeLegacyFPDoneFlag + "=true"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
}

func TestDedupIndex_PurgeKeepsPostCutoverRows(t *testing.T) {
	spy, d := newIndexSpy()
	_, err := runKind(t, indexNative(d)["purge"], tasks.ModeLive, &sentinelWriter{}, `{"cutover_date":"2025-01-01T00:00:00Z"}`)
	require.NoError(t, err)
	require.Equal(t, []string{"SetSetting " + purgeLegacyFPDoneFlag + "=true"}, spy.recorded())
}

func TestDedupIndex_PurgeRefusesModeKeysBadCutoverAndUnwiredDeps(t *testing.T) {
	spy, d := newIndexSpy()
	_, err := runKind(t, indexNative(d)["purge"], tasks.ModeLive, &sentinelWriter{}, `{"apply":true}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	_, err = runKind(t, indexNative(d)["purge"], tasks.ModeLive, &sentinelWriter{}, `{"cutover_date":"yesterday"}`)
	require.ErrorContains(t, err, "invalid cutover_date")
	_, err = runKind(t, indexNative(IndexKindDeps{Store: spy})["purge"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.NewWriter is not set")
	_, err = runKind(t, indexNative(IndexKindDeps{})["purge"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.Store is not set")
}

func TestDedupIndex_PurgeListErrorFailsTheRun(t *testing.T) {
	spy, d := newIndexSpy()
	spy.candidatesErr = errors.New("pebble hiccup")
	_, err := runKind(t, indexNative(d)["purge"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "list candidates")
}

func TestDedupIndex_PurgeHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runPurgeLegacyFP(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}

// --- window-index-build ---

func TestDedupIndex_WindowPreviewCountsAndWritesNothing(t *testing.T) {
	spy, d := newIndexSpy()
	h, err := runKind(t, indexNative(d)["window"], tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.True(t, strings.HasPrefix(h.lastNote(), "Preview: 3 file(s) with stored windows, 1 already indexed, 2 to index."), h.lastNote())
}

func TestDedupIndex_WindowLiveIndexesPendingFilesAndSetsTheFlag(t *testing.T) {
	spy, d := newIndexSpy()
	w := &sentinelWriter{}
	h, err := runKind(t, indexNative(d)["window"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"PutWindow f-three", "PutWindow f-two", "SetWindowIndexBuilt"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Window index built: 2 file(s) indexed now (48 band keys), 1 already indexed, of 3.")
}

func TestDedupIndex_WindowFailedFileLeavesTheFlagUnsetAndFailsTheRun(t *testing.T) {
	spy, d := newIndexSpy()
	spy.writeErr["f-two"] = errors.New("synthetic index write failure")
	_, err := runKind(t, indexNative(d)["window"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "index NOT marked built")
	require.NotContains(t, spy.recorded(), "SetWindowIndexBuilt")
}

func TestDedupIndex_WindowRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newIndexSpy()
	_, err := runKind(t, indexNative(d)["window"], tasks.ModeLive, &sentinelWriter{}, `{"dry_run":false}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	_, err = runKind(t, indexNative(IndexKindDeps{Store: spy})["window"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.NewWriter is not set")
	_, err = runKind(t, indexNative(IndexKindDeps{})["window"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Index.Store is not set")
}

// HEAD passed max(NumCPU, 2) workers (never one), which is not tasks.CPU()'s
// max(NumCPU, 1): the native pool declares HEAD's count, not CPU.
func TestDedupIndex_WindowUsesHEADsWorkerCount(t *testing.T) {
	_, d := newIndexSpy()
	def := indexNative(d)["window"]
	h := newKindHost(def, tasks.ModePreview, &sentinelWriter{})
	var declared []int
	h.capObserver = func(n int) { declared = append(declared, n) }
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []int{min(max(runtime.NumCPU(), 2), tasks.MaxFixed)}, declared)
	require.False(t, windowIndexBuildParallelism().UsesCPUBudget(), "HEAD's pool took no CPU slot")
}

func TestDedupIndex_WindowKindIsTheHeadIDWithTheBooksPrefix(t *testing.T) {
	require.Equal(t, tasks.Kind("books."+windowIndexBuildOpID), windowIndexBuildKind)
}
