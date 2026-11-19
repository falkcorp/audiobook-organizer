// file: internal/plugins/dedup/kinds_scoring_test.go
// version: 1.2.0
// guid: a002cdf7-aa0e-4499-b278-7610e743dccf
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// scoringSpy is the engine, store and writer a test hands the scoring kinds. It
// implements every method of scoringEngine, scoringKindStore and
// scoringKindWriter itself (the compiler checks that where scoringFakeDeps
// returns it), so a method added to an interface breaks this file until the spy
// answers it, instead of panicking on a nil embedded interface.
type scoringSpy struct {
	mu      sync.Mutex
	writers []tasks.Writer
	calls   []string
	writes  []string

	purgeDeleted int
	purgeErr     error

	autoRes dedupengine.AutoResolveResult
	autoErr error

	fullScanErr error

	rescoreRes dedupengine.RescoreResult
	rescoreErr error

	sigScanErr error

	drainRes *dedupengine.DrainStaleResult
	drainErr error

	lshBuilt, lshAvail bool
	candidateTotal     int
	candidateErr       error

	settings   map[string]string
	settingErr error

	books    []database.BookCore
	booksErr error
	files    map[string][]database.BookFile
	fileErr  map[string]error

	modifyErr  map[string]error
	primaryErr error
	setErr     error

	// split-book kinds
	splitCands []dedupengine.SplitBookCandidate
	detectErr  error
	saved      [][]dedupengine.SplitBookCandidate
	saveErr    error
	recheck    map[string][]string // first book id of the cluster -> reasons
	recheckErr map[string]error
	mergeRes   map[string]*dedupengine.SplitBookMergeResult // keep id -> result
	mergeErr   map[string]error
	merged     []string // "keep<-opID"
	deleted    []string
	deleteErr  error
}

func (s *scoringSpy) rec(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

// recWrite records a call that is a write (it is also a call).
func (s *scoringSpy) recWrite(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	s.writes = append(s.writes, call)
}

// engine

func (s *scoringSpy) PurgeStaleCandidates(context.Context) (int, error) {
	s.recWrite("PurgeStaleCandidates")
	return s.purgeDeleted, s.purgeErr
}

func (s *scoringSpy) AutoResolveCertain(_ context.Context, apply bool, maxMerges, sampleCap int) (dedupengine.AutoResolveResult, error) {
	call := fmt.Sprintf("AutoResolveCertain apply=%v max=%d cap=%d", apply, maxMerges, sampleCap)
	if apply {
		s.recWrite(call)
	} else {
		s.rec(call)
	}
	r := s.autoRes
	r.DryRun = !apply
	return r, s.autoErr
}

func (s *scoringSpy) FullScan(_ context.Context, progress func(phase string, done, total int)) error {
	s.recWrite("FullScan")
	if progress != nil {
		progress("scan", 1, 2)
		progress("scan", 2, 2)
		progress("score", 2, 2)
	}
	return s.fullScanErr
}

func (s *scoringSpy) Rescore(_ context.Context, apply bool) (dedupengine.RescoreResult, error) {
	call := fmt.Sprintf("Rescore apply=%v", apply)
	if apply {
		s.recWrite(call)
	} else {
		s.rec(call)
	}
	return s.rescoreRes, s.rescoreErr
}

func (s *scoringSpy) BookSignatureScan(_ context.Context, progress func(done, total int)) error {
	s.recWrite("BookSignatureScan")
	if progress != nil {
		progress(1, 2)
		progress(2, 2)
	}
	return s.sigScanErr
}

func (s *scoringSpy) DrainStaleCandidates(_ context.Context, opID string, apply bool, progress dedupengine.DrainStaleProgressFunc) (*dedupengine.DrainStaleResult, error) {
	call := fmt.Sprintf("DrainStaleCandidates id=%s apply=%v", opID, apply)
	if apply {
		s.recWrite(call)
	} else {
		s.rec(call)
	}
	if progress != nil {
		progress(1, 2, "scanned 1 of 2")
	}
	return s.drainRes, s.drainErr
}

// store

func (s *scoringSpy) LSHIndexBuilt() (bool, bool) { return s.lshBuilt, s.lshAvail }

func (s *scoringSpy) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	s.rec("ListCandidates layer=" + f.Layer)
	return nil, s.candidateTotal, s.candidateErr
}

func (s *scoringSpy) GetSetting(key string) (*database.Setting, error) {
	s.rec("GetSetting " + key)
	if s.settingErr != nil {
		return nil, s.settingErr
	}
	if v, ok := s.settings[key]; ok {
		return &database.Setting{Key: key, Value: v}, nil
	}
	return nil, nil
}

func (s *scoringSpy) GetAllBooksCore(int, int) ([]database.BookCore, error) {
	return append([]database.BookCore(nil), s.books...), s.booksErr
}

func (s *scoringSpy) GetBookFiles(id string) ([]database.BookFile, error) {
	if err := s.fileErr[id]; err != nil {
		return nil, err
	}
	return s.files[id], nil
}

// writer

func (s *scoringSpy) SetSetting(key, value, typ string, secret bool) error {
	s.recWrite("SetSetting " + key + "=" + value)
	return s.setErr
}

func (s *scoringSpy) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.recWrite("ModifyBook " + id)
	if err := s.modifyErr[id]; err != nil {
		return nil, err
	}
	b := &database.Book{ID: id}
	if err := fn(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *scoringSpy) EnsureSinglePrimary(_ context.Context, group string) error {
	s.recWrite("EnsureSinglePrimary " + group)
	return s.primaryErr
}

// split-book reads and writes

func (s *scoringSpy) DetectSplitBookCandidates(_ context.Context, progress func(loaded int)) ([]dedupengine.SplitBookCandidate, error) {
	s.rec("DetectSplitBookCandidates")
	if progress != nil {
		progress(1000)
	}
	return s.splitCands, s.detectErr
}

func (s *scoringSpy) RecheckSplitBookMembers(ids []string, _ int) ([]string, error) {
	s.rec("RecheckSplitBookMembers " + ids[0])
	return s.recheck[ids[0]], s.recheckErr[ids[0]]
}

func (s *scoringSpy) SaveAll(c []dedupengine.SplitBookCandidate) error {
	s.recWrite(fmt.Sprintf("SaveAll n=%d", len(c)))
	s.mu.Lock()
	s.saved = append(s.saved, c)
	s.mu.Unlock()
	return s.saveErr
}

func (s *scoringSpy) Delete(id string) error {
	s.recWrite("DeleteCandidate " + id)
	s.mu.Lock()
	s.deleted = append(s.deleted, id)
	s.mu.Unlock()
	return s.deleteErr
}

func (s *scoringSpy) MergeSplitBookClusterRechecked(ids []string, keepID, _, opID string) (*dedupengine.SplitBookMergeResult, error) {
	s.recWrite("MergeSplitBookClusterRechecked " + keepID)
	s.mu.Lock()
	s.merged = append(s.merged, keepID+"<-"+opID)
	s.mu.Unlock()
	if err := s.mergeErr[keepID]; err != nil {
		return nil, err
	}
	if r, ok := s.mergeRes[keepID]; ok {
		return r, nil
	}
	return &dedupengine.SplitBookMergeResult{KeepID: keepID, MergedSrcCount: len(ids) - 1}, nil
}

func (s *scoringSpy) splitWriterFactory(w tasks.Writer) splitBookKindWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *scoringSpy) engineFactory(w tasks.Writer) scoringEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *scoringSpy) writerFactory(w tasks.Writer) scoringKindWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *scoringSpy) seenWriters() []tasks.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tasks.Writer(nil), s.writers...)
}

func (s *scoringSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *scoringSpy) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// newScoringSpy is a spy and the deps that hand it to every scoring kind. The
// fixture is Batman comic titles: five copies of one generic title (the artifact
// shape) and one real book.
func newScoringSpy() (*scoringSpy, ScoringKindDeps) {
	s := &scoringSpy{
		settings:   map[string]string{},
		modifyErr:  map[string]error{},
		fileErr:    map[string]error{},
		files:      map[string][]database.BookFile{},
		recheck:    map[string][]string{},
		recheckErr: map[string]error{},
		mergeRes:   map[string]*dedupengine.SplitBookMergeResult{},
		mergeErr:   map[string]error{},
		drainRes:   &dedupengine.DrainStaleResult{Inspected: 5, WouldPurge: 2, Kept: 3, ReasonCounts: map[string]int{"title_leak": 2}},
	}
	for _, id := range []string{"year-one-1", "year-one-2", "year-one-3", "year-one-4", "year-one-5"} {
		s.books = append(s.books, database.BookCore{ID: id, Title: "Batman: Year One"})
		s.files[id] = []database.BookFile{{ID: id + "-f", AcoustIDFingerprintDurationSec: 30}}
	}
	s.books = append(s.books, database.BookCore{ID: "hush", Title: "Hush"})
	s.files["hush"] = []database.BookFile{{ID: "hush-f", AcoustIDFingerprintDurationSec: 30}}
	return s, ScoringKindDeps{Store: s, NewEngine: s.engineFactory, NewWriter: s.writerFactory,
		SplitReads: s, NewSplitWriter: s.splitWriterFactory}
}

func scoringFakeDeps(t *testing.T) ScoringKindDeps {
	t.Helper()
	_, d := newScoringSpy()
	return d
}

func scoringRows() []kindRow {
	merge := []ops.Resource{
		ops.ResAuthors, ops.ResBooks, ops.ResEmbeddings, ops.ResExternalIDs, ops.ResIndexes,
		ops.ResMetadataCache, ops.ResOperations, ops.ResSyncIdentity, ops.ResUserState,
	}
	return []kindRow{
		{
			HeadID: "dedup.purge-stale", Title: "Cleanup stale dedup candidates",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityHigh, Timeout: 10 * time.Minute, NoWarmup: true,
			Writes: []ops.Resource{ops.ResEmbeddings}, HeadKey: "dedup.purge-stale",
		},
		{
			HeadID: "dedup.auto-resolve", Title: "Auto-resolve CERTAIN dedup candidates (Tier 1)",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityHigh, Timeout: 30 * time.Minute,
			Writes: merge, Reads: []ops.Resource{ops.ResBookFiles}, HeadKey: "dedup.auto-resolve",
		},
		{
			HeadID: "dedup.full-scan", Title: "Full dedup scan",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 120 * time.Minute,
			Writes: merge, Reads: []ops.Resource{ops.ResBookFiles}, HeadKey: "dedup.full-scan",
		},
		{
			HeadID: "dedup.rescore", Title: "Re-band dedup candidates",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityHigh, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, HeadKey: "dedup.full-scan",
		},
		{
			HeadID: "dedup.book-signature-scan", Title: "Book signature scan",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 120 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks},
		},
		{
			HeadID: "dedup.drain-stale", Title: "Drain stale exact candidates",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings, ops.ResOperations, ops.ResSettings},
			Reads:  []ops.Resource{ops.ResAuthors, ops.ResBookFiles, ops.ResBooks}, HeadKey: "dedup.drain-stale",
		},
		{
			HeadID: "dedup.quarantine-chapter-artifacts", Title: "Quarantine chapter-file artifacts",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResAuthors, ops.ResBooks, ops.ResIndexes, ops.ResMetadataCache},
			Reads:  []ops.Resource{ops.ResBookFiles}, HeadKey: "dedup.quarantine-chapter-artifacts",
			Par: []string{"cpu", "cpu"}, ParGot: []tasks.Parallelism{quarantineScanPar(), quarantineApplyPar()},
		},
		{
			HeadID: "dedup.split-book-scan", Title: "Split-book backfill scan",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResSplitBookCandidates},
			Reads:  []ops.Resource{ops.ResAuthors, ops.ResBooks}, HeadKey: "dedup.split-book-scan",
		},
		{
			HeadID: "dedup.split-book-bulk-merge", Title: "Merge reviewed split-book candidates",
			HeadResume: sdk.ResumeAsk, Resume: tasks.ResumeAsk,
			Priority: tasks.PriorityLow, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{
				ops.ResAuthors, ops.ResBookFiles, ops.ResBooks, ops.ResExternalIDs, ops.ResIndexes,
				ops.ResMetadataCache, ops.ResOperations, ops.ResSplitBookCandidates, ops.ResSyncIdentity, ops.ResUserState,
			},
			HeadKey: "dedup.split-book-merge",
		},
	}
}

// scoringProbe is one kind's Live write probe over a fresh spy.
func scoringProbe(t *testing.T, build func(*Plugin, ScoringKindDeps) tasks.Definition, params string, tweak func(*scoringSpy)) writeProbe {
	t.Helper()
	spy, d := newScoringSpy()
	if tweak != nil {
		tweak(spy)
	}
	def := build(&Plugin{}, d)
	return writeProbe{Kind: def.Kind(), Def: def, Params: params, Writers: spy.seenWriters, Calls: spy.written}
}

func scoringWriteProbes(t *testing.T) []writeProbe {
	t.Helper()
	return []writeProbe{
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.purgeStaleNative(d, nil) }, "",
			func(s *scoringSpy) { s.purgeDeleted = 3 }),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.autoResolveNative(d, nil) }, "",
			func(*scoringSpy) {
				config.AppConfig.Dedup.AutoResolveEnabled = true
				t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = false })
			}),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.fullScanNative(d, nil) }, "", nil),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.rescoreNative(d, nil) }, "", nil),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.bookSignatureScanNative(d, nil) }, "", nil),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.drainStaleNative(d, nil) }, "", nil),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.quarantineChapterArtifactsNative(d, nil) }, "", nil),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.splitBookScanNative(d, nil) }, "", nil),
		scoringProbe(t, func(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.splitBookBulkMergeNative(d, nil) }, splitBulkParams, nil),
	}
}

// runKindResult is runKind that also returns the run's result document.
func runKindResult(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (*kindHost, json.RawMessage, error) {
	t.Helper()
	h := newKindHost(d, mode, w)
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok)
	decode, task, _, _ := r.ExecutorRunners()
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	if err != nil {
		return h, nil, err
	}
	out, err := task.Run(tasks.NewRC(h), p)
	return h, out, err
}

// requirePreviewWritesNothing: a Preview run reached no write and was never
// handed the run's writer.
func requirePreviewWritesNothing(t *testing.T, spy *scoringSpy) {
	t.Helper()
	require.Empty(t, spy.written(), "a Preview run writes nothing")
	for _, w := range spy.seenWriters() {
		require.Nil(t, w, "a Preview run is never handed the run's writer")
	}
}

func requireScoringModeKeysRefused(t *testing.T, build func(*Plugin, ScoringKindDeps) tasks.Definition) {
	t.Helper()
	for _, params := range []string{`{"dry_run":true}`, `{"dryRun":false}`, `{"Apply":true}`} {
		spy, d := newScoringSpy()
		_, _, err := runKindResult(t, build(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, params)
		require.ErrorContains(t, err, "the run's mode comes from the start request", params)
		require.Empty(t, spy.recorded(), params)
		require.Empty(t, spy.seenWriters(), params)
	}
}

// --- group tests (not TestDedupKinds_: the brief counts exactly four of those) ---

// A native purge-stale runs the shared core: the engine's count comes back as the
// result, and the progress frames are HEAD's.
func TestDedupScoring_PurgeStaleNativeRunsTheCore(t *testing.T) {
	spy, d := newScoringSpy()
	spy.purgeDeleted = 7
	def := (&Plugin{}).purgeStaleNative(d, nil)

	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"PurgeStaleCandidates"}, spy.recorded())
	last := h.lastNote()
	require.Equal(t, "Cleanup complete — 7 stale candidate(s) removed", last)
}

// HEAD runs purge-stale live only; a native Preview must not delete, so it is
// refused before the engine is built.
func TestDedupScoring_PurgeStalePreviewIsRefusedWithoutBuildingTheEngine(t *testing.T) {
	spy, d := newScoringSpy()
	def := (&Plugin{}).purgeStaleNative(d, nil)

	_, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "preview not supported; start with mode live")
	require.Empty(t, spy.seenWriters(), "a refused Preview builds no engine")
	require.Empty(t, spy.recorded())
}

// The run's mode comes from the start request: a mode key in params is refused,
// not dropped, so a dry-run start can never become a live one.
func TestDedupScoring_PurgeStaleRefusesModeKeysInParams(t *testing.T) {
	for _, params := range []string{`{"dry_run":true}`, `{"dryRun":false}`, `{"Apply":true}`} {
		spy, d := newScoringSpy()
		def := (&Plugin{}).purgeStaleNative(d, nil)
		_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, params)
		require.ErrorContains(t, err, "the run's mode comes from the start request", params)
		require.Empty(t, spy.recorded(), params)
	}
}

// An engine error fails the run with HEAD's wrapping.
func TestDedupScoring_PurgeStaleEngineErrorFailsTheRun(t *testing.T) {
	spy, d := newScoringSpy()
	spy.purgeErr = errors.New("pebble closed")
	def := (&Plugin{}).purgeStaleNative(d, nil)

	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "purge stale candidates: pebble closed")
}

// A deps value with no engine factory is refused up front, not dereferenced.
func TestDedupScoring_PurgeStaleRefusesUnwiredDeps(t *testing.T) {
	def := (&Plugin{}).purgeStaleNative(ScoringKindDeps{}, nil)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewEngine is not set")
}

// The HEAD shell still refuses a plugin with no engine, as before.
func TestDedupScoring_PurgeStaleHeadShellNeedsAnEngine(t *testing.T) {
	err := (&Plugin{}).runPurgeStale(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- auto-resolve ---

func enableAutoResolve(t *testing.T, on bool) {
	t.Helper()
	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = on
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })
}

func autoResolveBuild(p *Plugin, d ScoringKindDeps) tasks.Definition {
	return p.autoResolveNative(d, nil)
}

// A Live run applies: the engine gets apply=true and the params' caps, and the
// final note is HEAD's.
func TestDedupScoring_AutoResolveLiveApplies(t *testing.T) {
	enableAutoResolve(t, true)
	spy, d := newScoringSpy()
	spy.autoRes = dedupengine.AutoResolveResult{Checked: 4, Eligible: 3, Merged: 2, SkippedCap: 1}
	w := &sentinelWriter{}
	h, out, err := runKindResult(t, autoResolveBuild(&Plugin{}, d), tasks.ModeLive, w, `{"max_merges":7,"sample_cap":9}`)
	require.NoError(t, err)
	require.Equal(t, []string{"AutoResolveCertain apply=true max=7 cap=9"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Equal(t, "Auto-resolve complete (APPLY) — checked 4, eligible 3, merged 2, skipped(cap) 1", h.lastNote())
	var res autoResolveResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 2, res.Merged)
	require.Nil(t, res.Report, "an apply run stores no plan")
}

// Preview is the dry run: apply=false, the plan comes back in the result, and no
// writer is built or reached even with the kill switch off.
func TestDedupScoring_AutoResolvePreviewReportsAndWritesNothing(t *testing.T) {
	enableAutoResolve(t, false)
	spy, d := newScoringSpy()
	spy.autoRes = dedupengine.AutoResolveResult{Checked: 2, Eligible: 1}
	h, out, err := runKindResult(t, autoResolveBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"AutoResolveCertain apply=false max=0 cap=0"}, spy.recorded())
	requirePreviewWritesNothing(t, spy)
	require.Equal(t, "Auto-resolve complete (dry-run) — checked 2, eligible 1, merged 0, skipped(cap) 0", h.lastNote())
	var res autoResolveResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.True(t, res.DryRun)
	require.NotNil(t, res.Report)
}

func TestDedupScoring_AutoResolveRefusesModeKeysInParams(t *testing.T) {
	requireScoringModeKeysRefused(t, autoResolveBuild)
}

// An engine error fails the run with HEAD's wrapping; a Live run with the kill
// switch off is refused before the engine is called.
func TestDedupScoring_AutoResolveErrorPaths(t *testing.T) {
	enableAutoResolve(t, true)
	spy, d := newScoringSpy()
	spy.autoErr = errors.New("pebble closed")
	_, _, err := runKindResult(t, autoResolveBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "auto-resolve: pebble closed")

	enableAutoResolve(t, false)
	spy, d = newScoringSpy()
	_, _, err = runKindResult(t, autoResolveBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "requires dedup.auto_resolve_enabled=true")
	require.Empty(t, spy.recorded())
}

func TestDedupScoring_AutoResolveRefusesUnwiredDeps(t *testing.T) {
	_, _, err := runKindResult(t, autoResolveBuild(&Plugin{}, ScoringKindDeps{}), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewEngine is not set")
}

func TestDedupScoring_AutoResolveHeadShellNeedsAnEngine(t *testing.T) {
	err := (&Plugin{}).runAutoResolve(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- full-scan ---

func fullScanBuild(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.fullScanNative(d, nil) }

func TestDedupScoring_FullScanNativeRunsTheCore(t *testing.T) {
	spy, d := newScoringSpy()
	spy.candidateTotal = 12
	h, out, err := runKindResult(t, fullScanBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"PurgeStaleCandidates", "FullScan", "ListCandidates layer="}, spy.recorded())
	require.Contains(t, h.lastNote(), "Dedup scan complete — 12 pending candidates")
	var res fullScanResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 12, res.PendingCandidates)
}

// HEAD has no dry run for the scan: Preview is refused before any dependency is built.
func TestDedupScoring_FullScanPreviewIsRefused(t *testing.T) {
	spy, d := newScoringSpy()
	_, _, err := runKindResult(t, fullScanBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "preview not supported; start with mode live")
	require.Empty(t, spy.seenWriters())
	require.Empty(t, spy.recorded())
}

func TestDedupScoring_FullScanRefusesModeKeysInParams(t *testing.T) {
	requireScoringModeKeysRefused(t, fullScanBuild)
}

// A FullScan error fails the run; a failed purge does not.
func TestDedupScoring_FullScanErrorPaths(t *testing.T) {
	spy, d := newScoringSpy()
	spy.fullScanErr = errors.New("embedding backend down")
	_, _, err := runKindResult(t, fullScanBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "dedup scan: embedding backend down")

	spy, d = newScoringSpy()
	spy.purgeErr = errors.New("purge failed")
	_, _, err = runKindResult(t, fullScanBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err, "HEAD logs a failed purge and scans anyway")
}

func TestDedupScoring_FullScanRefusesUnwiredDeps(t *testing.T) {
	_, d := newScoringSpy()
	noStore := d
	noStore.Store = nil
	_, _, err := runKindResult(t, fullScanBuild(&Plugin{}, noStore), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.Store is not set")
	_, _, err = runKindResult(t, fullScanBuild(&Plugin{}, ScoringKindDeps{Store: d.Store}), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewEngine is not set")
}

func TestDedupScoring_FullScanHeadShellNeedsAnEngine(t *testing.T) {
	err := (&Plugin{}).runFullScan(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- rescore ---

func rescoreBuild(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.rescoreNative(d, nil) }

func TestDedupScoring_RescoreLiveWritesAndReports(t *testing.T) {
	spy, d := newScoringSpy()
	spy.rescoreRes = dedupengine.RescoreResult{Inspected: 10, Changed: 4, Written: 4, Skipped: 1}
	w := &sentinelWriter{}
	h, out, err := runKindResult(t, rescoreBuild(&Plugin{}, d), tasks.ModeLive, w, `{"reason":"ladder changed"}`)
	require.NoError(t, err)
	require.Equal(t, []string{"Rescore apply=true"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Equal(t, "Re-banded 4 of 10 pending candidates (1 skipped: no stored signals)", h.lastNote())
	var res rescoreResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 4, res.Written)
}

func TestDedupScoring_RescorePreviewIsTheDryRun(t *testing.T) {
	spy, d := newScoringSpy()
	_, _, err := runKindResult(t, rescoreBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Rescore apply=false"}, spy.recorded())
	requirePreviewWritesNothing(t, spy)
}

func TestDedupScoring_RescoreRefusesModeKeysInParams(t *testing.T) {
	requireScoringModeKeysRefused(t, rescoreBuild)
}

// A partial write failure fails the run and still returns the counts.
func TestDedupScoring_RescoreErrorPaths(t *testing.T) {
	spy, d := newScoringSpy()
	spy.rescoreRes = dedupengine.RescoreResult{Inspected: 10, Changed: 4, Written: 3, WriteErrors: 1}
	h, _, err := runKindResult(t, rescoreBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "dedup rescore: 1 of 4 changed candidate rows could not be re-banded")
	require.Equal(t, "Re-band incomplete — 1 of 4 changed rows could not be written", h.lastNote())

	spy, d = newScoringSpy()
	spy.rescoreErr = errors.New("pebble closed")
	_, _, err = runKindResult(t, rescoreBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "dedup rescore: pebble closed")
}

func TestDedupScoring_RescoreRefusesUnwiredDeps(t *testing.T) {
	_, _, err := runKindResult(t, rescoreBuild(&Plugin{}, ScoringKindDeps{}), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewEngine is not set")
}

func TestDedupScoring_RescoreHeadShellNeedsAnEngine(t *testing.T) {
	err := (&Plugin{}).runRescore(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- book-signature-scan ---

func bookSigBuild(p *Plugin, d ScoringKindDeps) tasks.Definition {
	return p.bookSignatureScanNative(d, nil)
}

func TestDedupScoring_BookSignatureScanNativeRunsTheCore(t *testing.T) {
	spy, d := newScoringSpy()
	spy.candidateTotal = 3
	h, out, err := runKindResult(t, bookSigBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"BookSignatureScan", "ListCandidates layer=book_signature"}, spy.recorded())
	require.Contains(t, h.lastNote(), "Book signature scan complete — 3 pending candidate(s)")
	var res bookSignatureScanResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 3, res.PendingCandidates)
}

func TestDedupScoring_BookSignatureScanPreviewIsRefused(t *testing.T) {
	spy, d := newScoringSpy()
	_, _, err := runKindResult(t, bookSigBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "preview not supported; start with mode live")
	require.Empty(t, spy.seenWriters())
	require.Empty(t, spy.recorded())
}

func TestDedupScoring_BookSignatureScanRefusesModeKeysInParams(t *testing.T) {
	requireScoringModeKeysRefused(t, bookSigBuild)
}

func TestDedupScoring_BookSignatureScanErrorPath(t *testing.T) {
	spy, d := newScoringSpy()
	spy.sigScanErr = errors.New("no signatures")
	_, _, err := runKindResult(t, bookSigBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "book signature scan: no signatures")
}

func TestDedupScoring_BookSignatureScanRefusesUnwiredDeps(t *testing.T) {
	_, d := newScoringSpy()
	noStore := d
	noStore.Store = nil
	_, _, err := runKindResult(t, bookSigBuild(&Plugin{}, noStore), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.Store is not set")
	_, _, err = runKindResult(t, bookSigBuild(&Plugin{}, ScoringKindDeps{Store: d.Store}), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewEngine is not set")
}

func TestDedupScoring_BookSignatureScanHeadShellNeedsAnEngine(t *testing.T) {
	err := (&Plugin{}).runBookSignatureScan(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- drain-stale ---

func drainBuild(p *Plugin, d ScoringKindDeps) tasks.Definition { return p.drainStaleNative(d, nil) }

// A Live run drains and then sets the versioned done-flag through the writer.
func TestDedupScoring_DrainStaleLiveSetsTheDoneFlag(t *testing.T) {
	spy, d := newScoringSpy()
	w := &sentinelWriter{}
	h, out, err := runKindResult(t, drainBuild(&Plugin{}, d), tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{
		"GetSetting " + drainStaleDoneFlag,
		"DrainStaleCandidates id=" + drainStaleCheckpointID + " apply=true",
		"SetSetting " + drainStaleDoneFlag + "=true",
	}, spy.recorded())
	require.Equal(t, []tasks.Writer{w, w}, spy.seenWriters())
	require.True(t, strings.HasPrefix(h.lastNote(), "Complete — 2 candidate(s) reclassified stale-drain."), h.lastNote())
	var res drainStaleResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.True(t, res.FlagSet)
}

// Preview is the dry run: nothing is written, no flag is read, no writer built.
func TestDedupScoring_DrainStalePreviewWritesNothing(t *testing.T) {
	spy, d := newScoringSpy()
	h, _, err := runKindResult(t, drainBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"DrainStaleCandidates id=" + drainStaleCheckpointID + " apply=false"}, spy.recorded())
	requirePreviewWritesNothing(t, spy)
	require.True(t, strings.HasPrefix(h.lastNote(), "Dry-run — 2 candidate(s) would be reclassified stale-drain."), h.lastNote())
}

func TestDedupScoring_DrainStaleRefusesModeKeysInParams(t *testing.T) {
	requireScoringModeKeysRefused(t, drainBuild)
}

// An engine error fails the run; a set flag skips the engine; read errors keep
// the flag unset.
func TestDedupScoring_DrainStaleErrorAndFlagPaths(t *testing.T) {
	spy, d := newScoringSpy()
	spy.drainErr = errors.New("scan failed")
	spy.drainRes = nil
	_, _, err := runKindResult(t, drainBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "drain-stale: scan failed")

	spy, d = newScoringSpy()
	spy.settings[drainStaleDoneFlag] = "true"
	h, out, err := runKindResult(t, drainBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, "Already completed (flag set); nothing to do.", h.lastNote())
	require.Empty(t, spy.written())
	var res drainStaleResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.True(t, res.AlreadyDone)

	spy, d = newScoringSpy()
	spy.drainRes.LookupErrors = 1
	_, _, err = runKindResult(t, drainBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.written()[1:], "a run with read errors does not set the flag")
}

func TestDedupScoring_DrainStaleRefusesUnwiredDeps(t *testing.T) {
	_, d := newScoringSpy()
	_, _, err := runKindResult(t, drainBuild(&Plugin{}, ScoringKindDeps{}), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewEngine is not set")
	_, _, err = runKindResult(t, drainBuild(&Plugin{}, ScoringKindDeps{NewEngine: d.NewEngine}), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.Store is not set")
	_, _, err = runKindResult(t, drainBuild(&Plugin{}, ScoringKindDeps{NewEngine: d.NewEngine, Store: d.Store}), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewWriter is not set")
}

func TestDedupScoring_DrainStaleHeadShellNeedsAnEngine(t *testing.T) {
	err := (&Plugin{}).runDrainStale(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- quarantine-chapter-artifacts ---

func quarantineBuild(p *Plugin, d ScoringKindDeps) tasks.Definition {
	return p.quarantineChapterArtifactsNative(d, nil)
}

// A Live run soft-deletes the five colliding short books, not the real one.
func TestDedupScoring_QuarantineLiveSoftDeletesArtifacts(t *testing.T) {
	spy, d := newScoringSpy()
	w := &sentinelWriter{}
	h, out, err := runKindResult(t, quarantineBuild(&Plugin{}, d), tasks.ModeLive, w, "")
	require.NoError(t, err)
	got := spy.written()
	require.ElementsMatch(t, []string{
		"ModifyBook year-one-1", "ModifyBook year-one-2", "ModifyBook year-one-3",
		"ModifyBook year-one-4", "ModifyBook year-one-5",
	}, got)
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.True(t, strings.HasPrefix(h.lastNote(), "Soft-deleted 5 chapter-artifact book(s). "), h.lastNote())
	var res quarantineChapterArtifactsResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 5, res.Quarantined)
	require.Equal(t, 5, res.Artifacts)
}

func TestDedupScoring_QuarantinePreviewWritesNothing(t *testing.T) {
	spy, d := newScoringSpy()
	h, out, err := runKindResult(t, quarantineBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, `{"min_title_collisions":3}`)
	require.NoError(t, err)
	requirePreviewWritesNothing(t, spy)
	require.True(t, strings.HasPrefix(h.lastNote(), "Dry-run — 5 chapter-artifact book(s) would be soft-deleted."), h.lastNote())
	var res quarantineChapterArtifactsResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Equal(t, 0, res.Quarantined)
	require.Equal(t, 5, res.Artifacts)
}

func TestDedupScoring_QuarantineRefusesModeKeysInParams(t *testing.T) {
	requireScoringModeKeysRefused(t, quarantineBuild)
}

// A failed write and an unreadable file list fail the run (counted, not logged
// only); a library that cannot be listed fails it up front.
func TestDedupScoring_QuarantineErrorPaths(t *testing.T) {
	spy, d := newScoringSpy()
	spy.modifyErr["year-one-2"] = errors.New("disk full")
	_, out, err := runKindResult(t, quarantineBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorIs(t, err, errQuarantineIncomplete)
	_ = out

	spy, d = newScoringSpy()
	spy.fileErr["year-one-3"] = errors.New("read failed")
	_, _, err = runKindResult(t, quarantineBuild(&Plugin{}, d), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorIs(t, err, errQuarantineIncomplete)

	spy, d = newScoringSpy()
	spy.booksErr = errors.New("store closed")
	_, _, err = runKindResult(t, quarantineBuild(&Plugin{}, d), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "get all books: store closed")
}

func TestDedupScoring_QuarantineRefusesUnwiredDeps(t *testing.T) {
	_, d := newScoringSpy()
	_, _, err := runKindResult(t, quarantineBuild(&Plugin{}, ScoringKindDeps{}), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.Store is not set")
	_, _, err = runKindResult(t, quarantineBuild(&Plugin{}, ScoringKindDeps{Store: d.Store}), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewWriter is not set")
}

func TestDedupScoring_QuarantineHeadShellNeedsAStore(t *testing.T) {
	err := (&Plugin{}).runQuarantineChapterArtifacts(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "main store not available")
}
