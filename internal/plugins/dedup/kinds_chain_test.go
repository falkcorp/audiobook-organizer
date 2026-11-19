// file: internal/plugins/dedup/kinds_chain_test.go
// version: 1.4.0
// guid: 6d01f54f-d1e9-4cf7-8def-037dd38e3e04
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// chainSpy is the reads, writer and rescore a test hands the chain kinds. It
// implements every method of lshIndexBuildReads and lshIndexBuildWriter itself
// (the compiler checks that where chainFakeDeps returns it), so a method added to
// either breaks this file until the spy answers it.
type chainSpy struct {
	mu      sync.Mutex
	writers []tasks.Writer
	calls   []string
	flagSet bool

	files   []database.BookFileCore
	indexed map[string]bool
	hydrate map[string][]database.BookFile
	putErr  map[string]error

	rescoreCalls int
	rescore      dedupengine.RescoreResult
	rescoreErr   error
}

func (s *chainSpy) rec(c string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
}

func (s *chainSpy) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return append([]database.BookFileCore(nil), s.files...), nil
}
func (s *chainSpy) HasLSHIndex(id string) bool { return s.indexed[id] }
func (s *chainSpy) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.hydrate[bookID], nil
}
func (s *chainSpy) PutLSHEntries(fileID, bookID string, _ []fingerprint.Subprint, _ []byte) error {
	s.rec("PutLSHEntries " + fileID)
	return s.putErr[fileID]
}
func (s *chainSpy) SetLSHIndexBuilt() error {
	s.rec("SetLSHIndexBuilt")
	s.mu.Lock()
	s.flagSet = true
	s.mu.Unlock()
	return nil
}
func (s *chainSpy) factory(w tasks.Writer) lshIndexBuildWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}
func (s *chainSpy) seenWriters() []tasks.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tasks.Writer(nil), s.writers...)
}
func (s *chainSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}
func (s *chainSpy) doRescore(context.Context) (dedupengine.RescoreResult, error) {
	s.mu.Lock()
	s.rescoreCalls++
	s.mu.Unlock()
	return s.rescore, s.rescoreErr
}

// newChainSpy seeds three books (Batman comic titles as ids): two with a print
// and one with none, plus one print already indexed.
func newChainSpy() (*chainSpy, ChainKindDeps) {
	fp := synthRawLSH(42, 57600)
	s := &chainSpy{
		files: []database.BookFileCore{
			{ID: "file-year-one", BookID: "year-one", AcoustIDFingerprintDurationSec: 1800},
			{ID: "file-hush", BookID: "hush", AcoustIDFingerprintDurationSec: 1800},
			{ID: "file-long-halloween", BookID: "long-halloween", AcoustIDFingerprintDurationSec: 1800},
			{ID: "file-dark-victory", BookID: "dark-victory"}, // no print
		},
		indexed: map[string]bool{"file-long-halloween": true},
		hydrate: map[string][]database.BookFile{
			"year-one": {{ID: "file-year-one", BookID: "year-one", AcoustIDFingerprint: fp, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}},
			"hush":     {{ID: "file-hush", BookID: "hush", AcoustIDFingerprint: fp, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}},
		},
		putErr:  map[string]error{},
		rescore: dedupengine.RescoreResult{Inspected: 9, Changed: 2},
	}
	return s, ChainKindDeps{LSHReads: s, NewLSHWriter: s.factory, RescorePreview: s.doRescore}
}

func chainFakeDeps(t *testing.T) ChainKindDeps {
	t.Helper()
	_, d := newChainSpy()
	return d
}

func chainRows() []kindRow {
	return []kindRow{
		{
			HeadID: "dedup.lsh-index-build", Title: "Build LSH fingerprint index",
			HeadResume: sdk.ResumeRequeue, Resume: tasks.ResumeFromZero,
			Priority: tasks.PriorityLow, Timeout: 120 * time.Minute,
			Writes: []ops.Resource{ops.ResFingerprintIndex, ops.ResSettings}, Reads: []ops.Resource{ops.ResBookFiles},
			Par: []string{"cpu"}, ParGot: []tasks.Parallelism{tasks.CPU()},
			HeadKey: "dedup.lsh-index",
		},
		{
			HeadID: "dedup.run-all", Title: "Find all duplicates",
			HeadResume: sdk.ResumeRestart, Resume: tasks.ResumeContinue,
			Priority: tasks.PriorityLow, Timeout: 24 * time.Hour, ReadOnly: true,
			HeadKey: "dedup.run-all",
		},
		{
			HeadID: "dedup.label-refinement", Title: "Label refinement (report only)",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 2 * time.Hour, ReadOnly: true,
			HeadKey: "dedup.label-refinement",
		},
	}
}

func chainWriteProbes(t *testing.T) []writeProbe {
	t.Helper()
	spy, d := newChainSpy()
	def := (&Plugin{}).lshIndexBuildNative(d, nil)
	return []writeProbe{{Kind: def.Kind(), Def: def, Writers: spy.seenWriters, Calls: spy.recorded}}
}

// chainChildWaitChecks is TestDedupKinds_ChildWaitsUseAwait's body: each
// child-waiting definition starts every child with rc.Enqueue and waits for the
// same run with rc.Await, in order, naming a native kind.
func chainChildWaitChecks(t *testing.T) {
	t.Helper()
	setEmbeddings(t, true)
	_, d := newChainSpy()
	p := &Plugin{}

	runAll := p.runAllNative(d, nil)
	h, err := runKind(t, runAll, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	kinds := enqueuedKinds(h)
	require.Equal(t, []tasks.Kind{kindFor("dedup.embed-scan"), kindFor("acoustid.scan"), kindFor("dedup.full-scan"), kindFor("dedup.llm-review")}, kinds)
	require.Equal(t, enqueuedIDs(h), h.awaitedIDs(), "every child is awaited, by the run id Enqueue returned")

	refine := p.labelRefinementNative(d, nil)
	h, err = runKind(t, refine, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []tasks.Kind{kindFor("dedup.rebuild-gold-labels"), kindFor("dedup.calibrate-composite")}, enqueuedKinds(h))
	require.Equal(t, enqueuedIDs(h), h.awaitedIDs())
}

func enqueuedKinds(h *kindHost) []tasks.Kind {
	var out []tasks.Kind
	for _, e := range h.enqueuedRuns() {
		out = append(out, e.Kind)
	}
	return out
}

func enqueuedIDs(h *kindHost) []string {
	var out []string
	for _, e := range h.enqueuedRuns() {
		out = append(out, e.ID)
	}
	return out
}

// setEmbeddings sets the config flag run-all's plan reads, restoring it after.
func setEmbeddings(t *testing.T, on bool) {
	t.Helper()
	old := config.AppConfig.Dedup.EmbeddingsEnabled
	config.AppConfig.Dedup.EmbeddingsEnabled = on
	t.Cleanup(func() { config.AppConfig.Dedup.EmbeddingsEnabled = old })
}

// --- lsh-index-build ---

func TestDedupChain_LSHIndexesUnindexedFilesThroughTheWriter(t *testing.T) {
	spy, d := newChainSpy()
	def := (&Plugin{}).lshIndexBuildNative(d, nil)
	w := &sentinelWriter{}

	h, err := runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	calls := spy.recorded()
	require.ElementsMatch(t, []string{"PutLSHEntries file-year-one", "PutLSHEntries file-hush", "SetLSHIndexBuilt"}, calls)
	require.Equal(t, "SetLSHIndexBuilt", calls[len(calls)-1], "the completion flag is written last")
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())

	// The one book with no print is handed to a rescan run, which is not awaited.
	runs := h.enqueuedRuns()
	require.Len(t, runs, 1)
	require.Equal(t, kindFor("acoustid.fingerprint-rescan"), runs[0].Kind)
	require.JSONEq(t, `{"scope":"books","book_ids":["dark-victory"]}`, string(runs[0].Params))
	require.Empty(t, h.awaitedIDs())
	require.Contains(t, h.lastNote(), "LSH index build complete — 2 indexed, 1 skipped (already indexed), 1 no-fingerprint (1 books queued for rescan, 0 permanently failed), 0 errors (of 4 files)")
}

func TestDedupChain_LSHCountsAWriteErrorAndStillSetsTheFlag(t *testing.T) {
	spy, d := newChainSpy()
	spy.putErr["file-hush"] = errors.New("disk full")
	def := (&Plugin{}).lshIndexBuildNative(d, nil)

	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Contains(t, h.lastNote(), "1 indexed, 1 skipped (already indexed), 1 no-fingerprint (1 books queued for rescan, 0 permanently failed), 1 errors")
	require.True(t, spy.flagSet)
}

func TestDedupChain_LSHPreviewIsRefusedAndBuildsNoWriter(t *testing.T) {
	spy, d := newChainSpy()
	def := (&Plugin{}).lshIndexBuildNative(d, nil)
	_, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "preview not supported; start with mode live")
	require.Empty(t, spy.seenWriters())
	require.Empty(t, spy.recorded())
}

func TestDedupChain_LSHRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newChainSpy()
	def := (&Plugin{}).lshIndexBuildNative(d, nil)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, `{"dry_run":true}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	require.Empty(t, spy.recorded())

	_, err = runKind(t, (&Plugin{}).lshIndexBuildNative(ChainKindDeps{NewLSHWriter: spy.factory}, nil), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Chain.LSHReads is not set")
	_, err = runKind(t, (&Plugin{}).lshIndexBuildNative(ChainKindDeps{LSHReads: spy}, nil), tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Chain.NewLSHWriter is not set")
}

func TestDedupChain_LSHRescanFailureIsLoggedNotFatal(t *testing.T) {
	spy, d := newChainSpy()
	def := (&Plugin{}).lshIndexBuildNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	h.enqueueErr = errors.New("queue closed")
	require.NoError(t, runKindOn(t, def, h, ""))
	require.True(t, spy.flagSet, "a failed rescan enqueue still lets the build finish")
}

func TestDedupChain_LSHFansOutOverTheCPUPool(t *testing.T) {
	_, d := newChainSpy()
	def := (&Plugin{}).lshIndexBuildNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	var declared []int
	h.capObserver = func(n int) { declared = append(declared, n) }
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []int{tasks.CPU().Workers()}, declared)
}

func TestDedupChain_LSHHeadShellNeedsAnLSHStore(t *testing.T) {
	err := (&Plugin{}).runLSHIndexBuild(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "store does not implement LSHIndexStore")
}

// --- run-all ---

func TestDedupChain_RunAllPreviewStartsNoChildAndReturnsTheRescore(t *testing.T) {
	setEmbeddings(t, true)
	spy, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)

	h, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, h.enqueuedRuns(), "a preview starts no child")
	require.Equal(t, 1, spy.rescoreCalls)
	require.Equal(t, "Preview finished: 4 step(s) would start child operations; nothing was changed; 2 of 9 waiting pairs would score differently", h.lastNote())
}

func TestDedupChain_RunAllLiveStartsEveryChildLiveAndEmptyParams(t *testing.T) {
	setEmbeddings(t, true)
	spy, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)

	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	runs := h.enqueuedRuns()
	require.Len(t, runs, 4)
	for _, r := range runs {
		require.Equal(t, tasks.ModeRequestLive, r.Mode, "%s starts Live explicitly", r.Kind)
		require.JSONEq(t, `{}`, string(r.Params), "%s: the mode is not a param", r.Kind)
	}
	require.Equal(t, 1, spy.rescoreCalls, "the rescore preview closes the chain")
	require.Equal(t, "Finished 5 of 5 steps; 2 of 9 waiting pairs would score differently (nothing changed)", h.lastNote())
}

func TestDedupChain_RunAllSkipsEmbeddingsWhenTurnedOff(t *testing.T) {
	setEmbeddings(t, false)
	_, d := newChainSpy()
	h, err := runKind(t, (&Plugin{}).runAllNative(d, nil), tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []tasks.Kind{kindFor("acoustid.scan"), kindFor("dedup.full-scan"), kindFor("dedup.llm-review")}, enqueuedKinds(h))
}

func TestDedupChain_RunAllStopsAtAFailedChild(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	h.onAwait = func(ref tasks.RunRef) (tasks.Outcome, error) {
		if ref.ID == "child-2" {
			return tasks.Outcome{State: tasks.StateFailed, Err: "scanner crashed"}, nil
		}
		return tasks.Outcome{State: tasks.StateSucceeded}, nil
	}
	err := runKindOn(t, def, h, "")
	require.ErrorContains(t, err, `Comparing audio fingerprints (child-2) ended as "failed": scanner crashed`)
	require.Len(t, h.enqueuedRuns(), 2, "no step after the failed one starts")
}

func TestDedupChain_RunAllResumeReattachesToTheRecordedChild(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	require.NoError(t, h.FakeRun.SetCheckpoint("", runAllState{
		Plan:      []string{"embeddings", "acoustic", "find", "ai-review", "rescore-preview"},
		StepIndex: 2, ChildOpID: "child-find",
		Finished: []RunAllStepRecord{{ID: "embeddings"}, {ID: "acoustic"}},
	}))
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []string{"child-find", "child-1"}, h.awaitedIDs(), "the recorded child is awaited first; only llm-review starts new")
	require.Equal(t, []tasks.Kind{kindFor("dedup.llm-review")}, enqueuedKinds(h))
}

func TestDedupChain_RunAllReenqueuesAChildTheKernelNoLongerKnows(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	require.NoError(t, h.FakeRun.SetCheckpoint("", runAllState{
		Plan:      []string{"ai-review"},
		StepIndex: 0, ChildOpID: "child-gone",
	}))
	h.onAwait = func(ref tasks.RunRef) (tasks.Outcome, error) {
		if ref.ID == "child-gone" {
			return tasks.Outcome{}, errors.New("unknown run")
		}
		return tasks.Outcome{State: tasks.StateSucceeded}, nil
	}
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []tasks.Kind{kindFor("dedup.llm-review")}, enqueuedKinds(h))
}

// HEAD's resume restarts a recorded child that a restart interrupted. The
// llm-review child is ResumeDrop, so after a restart the kernel drops it: the
// step starts it again instead of failing the run (review m1).
func TestDedupChain_RunAllRestartsARecordedChildTheKernelDropped(t *testing.T) {
	for _, state := range []tasks.RunState{tasks.StateInterruptedDropped, tasks.StateInterrupted, tasks.StateInterruptedQuiesced} {
		t.Run(string(state), func(t *testing.T) {
			setEmbeddings(t, true)
			_, d := newChainSpy()
			def := (&Plugin{}).runAllNative(d, nil)
			h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
			require.NoError(t, h.FakeRun.SetCheckpoint("", runAllState{
				Plan:      []string{"ai-review"},
				StepIndex: 0, ChildOpID: "child-dropped",
			}))
			h.onAwait = func(ref tasks.RunRef) (tasks.Outcome, error) {
				if ref.ID == "child-dropped" {
					return tasks.Outcome{State: state}, nil
				}
				return tasks.Outcome{State: tasks.StateSucceeded}, nil
			}
			require.NoError(t, runKindOn(t, def, h, ""))
			require.Equal(t, []tasks.Kind{kindFor("dedup.llm-review")}, enqueuedKinds(h), "the step starts again")
			require.Contains(t, h.logged(), "was interrupted by a restart ("+string(state)+"); starting it again")
		})
	}
}

// A recorded child that ENDED badly (failed, canceled, timed out) is not
// restarted: the run stops, as HEAD's does.
func TestDedupChain_RunAllStopsAtARecordedChildThatEndedBadly(t *testing.T) {
	for _, state := range []tasks.RunState{tasks.StateFailed, tasks.StateCanceled, tasks.StateTimedOut} {
		t.Run(string(state), func(t *testing.T) {
			setEmbeddings(t, true)
			_, d := newChainSpy()
			def := (&Plugin{}).runAllNative(d, nil)
			h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
			require.NoError(t, h.FakeRun.SetCheckpoint("", runAllState{
				Plan:      []string{"ai-review"},
				StepIndex: 0, ChildOpID: "child-bad",
			}))
			h.onAwait = func(tasks.RunRef) (tasks.Outcome, error) { return tasks.Outcome{State: state, Err: "boom"}, nil }
			err := runKindOn(t, def, h, "")
			require.ErrorContains(t, err, `(child-bad) ended as "`+string(state)+`": boom`)
			require.Empty(t, enqueuedKinds(h), "nothing starts again")
		})
	}
}

// A child this run started that is interrupted DURING the live wait is not a
// restart (the run would have stopped with it): the step fails.
func TestDedupChain_RunAllFailsAChildInterruptedDuringTheLiveWait(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	h.onAwait = func(tasks.RunRef) (tasks.Outcome, error) {
		return tasks.Outcome{State: tasks.StateInterruptedDropped}, nil
	}
	err := runKindOn(t, def, h, "")
	require.ErrorContains(t, err, `ended as "interrupted_dropped"`)
	require.Len(t, h.enqueuedRuns(), 1, "the failed step is not started a second time")
}

// A child the kernel starts without an id is an error worded for the kernel, and
// no wait follows it. HEAD's shell words the same case for the registry.
func TestDedupChain_ARunWithNoIDIsRefusedInEachShellsWords(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	for name, mk := range map[string]func() tasks.Definition{
		"run-all":          func() tasks.Definition { return (&Plugin{}).runAllNative(d, nil) },
		"label-refinement": func() tasks.Definition { return (&Plugin{}).labelRefinementNative(d, nil) },
	} {
		t.Run("native "+name, func(t *testing.T) {
			def := mk()
			h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
			h.emptyRunID = true
			err := runKindOn(t, def, h, "")
			require.ErrorContains(t, err, "the kernel returned no run id for ")
			require.Empty(t, h.awaitedIDs(), "nothing is awaited")
		})
	}

	p, _, _, _ := newRunAllPlugin(t, true)
	p.registry = noIDRegistry{}
	err := p.runRunAll(context.Background(), liveJSON, &runAllReporter{})
	require.ErrorContains(t, err, "the registry returned no operation id for dedup.embed-scan")
	err = p.runLabelRefinement(context.Background(), nil, &runAllReporter{})
	require.ErrorContains(t, err, "the registry returned no operation id for dedup.rebuild-gold-labels")
}

// noIDRegistry accepts every enqueue and returns no operation id.
type noIDRegistry struct{ *runAllRegistry }

func (noIDRegistry) EnqueueOp(context.Context, string, any, ...sdk.EnqueueOption) (string, error) {
	return "", nil
}

// While a child runs, the parent saves its position at most once a minute
// (runAllCheckpointEvery), not on every row change: a burst of progress adds one
// save (the first, which stamps a non-zero high-water mark) and no more.
func TestDedupChain_RunAllThrottlesTheCheckpointWhileAChildRuns(t *testing.T) {
	saves := func(rows []database.OperationV2Row) int {
		p, _, ops, _ := newRunAllPlugin(t, true)
		ops.byDef["dedup.full-scan"] = rows
		rep := &runAllReporter{}
		params := mustJSON(t, runAllState{DryRun: opmode.Live(), Plan: []string{"find"}})
		require.NoError(t, p.runRunAll(context.Background(), params, rep))
		rep.mu.Lock()
		defer rep.mu.Unlock()
		return len(rep.checkpoints)
	}
	quiet := saves([]database.OperationV2Row{{Status: "completed"}})
	busy := saves([]database.OperationV2Row{
		{Status: "running", ProgressCurrent: 1, ProgressTotal: 100, ProgressMessage: "a"},
		{Status: "running", ProgressCurrent: 2, ProgressTotal: 100, ProgressMessage: "b"},
		{Status: "running", ProgressCurrent: 3, ProgressTotal: 100, ProgressMessage: "c"},
		{Status: "running", ProgressCurrent: 4, ProgressTotal: 100, ProgressMessage: "d"},
		{Status: "running", ProgressCurrent: 5, ProgressTotal: 100, ProgressMessage: "e"},
		{Status: "completed"},
	})
	require.Equal(t, quiet+1, busy, "five row changes cost one extra save, not five")
}

func TestDedupChain_RunAllRefusesACorruptCheckpointPlan(t *testing.T) {
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	require.NoError(t, h.FakeRun.SetCheckpoint("", runAllState{Plan: []string{"embeddings", "teleport"}}))
	err := runKindOn(t, def, h, "")
	require.ErrorContains(t, err, `unknown step "teleport"`)
	require.Empty(t, h.enqueuedRuns())
}

func TestDedupChain_RunAllRefusesModeKeysAndUnwiredRescore(t *testing.T) {
	_, d := newChainSpy()
	_, err := runKind(t, (&Plugin{}).runAllNative(d, nil), tasks.ModeLive, &sentinelWriter{}, `{"dry_run":false}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
	_, err = runKind(t, (&Plugin{}).runAllNative(ChainKindDeps{}, nil), tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Chain.RescorePreview is not set")
}

func TestDedupChain_RunAllCancelEndsAWaitOnAChildWithoutEndingTheChild(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &kindHost{FakeRun: taskstest.NewFakeRun(ctx, def.Kind()), mode: tasks.ModeLive, w: &sentinelWriter{}}
	waiting := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	h.onAwait = func(tasks.RunRef) (tasks.Outcome, error) {
		close(waiting)
		<-release // rc.Await takes no context: only the run's own cancel can end the wait
		return tasks.Outcome{State: tasks.StateSucceeded}, nil
	}
	errc := make(chan error, 1)
	go func() { errc <- runKindOn(t, def, h, "") }()
	<-waiting
	cancel()
	select {
	case err := <-errc:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled run kept waiting on its child")
	}
	require.Len(t, h.enqueuedRuns(), 1, "nothing after the awaited step starts")
}

// --- label-refinement ---

func TestDedupChain_LabelRefinementStartsBothChildrenInPreviewWithEmptyParams(t *testing.T) {
	_, d := newChainSpy()
	def := (&Plugin{}).labelRefinementNative(d, nil)
	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	runs := h.enqueuedRuns()
	require.Len(t, runs, 2)
	for _, r := range runs {
		require.Equal(t, tasks.ModeRequestPreview, r.Mode, "%s: report only, never Live", r.Kind)
		require.JSONEq(t, `{}`, string(r.Params))
	}
	require.Contains(t, h.lastNote(), "Finished (report only, nothing written)")
}

func TestDedupChain_LabelRefinementStopsAtAFailedChild(t *testing.T) {
	_, d := newChainSpy()
	def := (&Plugin{}).labelRefinementNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	h.onAwait = func(tasks.RunRef) (tasks.Outcome, error) {
		return tasks.Outcome{State: tasks.StateTimedOut, Err: "too slow"}, nil
	}
	err := runKindOn(t, def, h, "")
	require.ErrorContains(t, err, `Rebuilding gold labels (child-1) ended as "timed_out": too slow; label refinement stopped`)
	require.Len(t, h.enqueuedRuns(), 1, "calibrate does not start behind a failed rebuild")
}

func TestDedupChain_LabelRefinementRefusesPreviewAndModeKeys(t *testing.T) {
	_, d := newChainSpy()
	def := (&Plugin{}).labelRefinementNative(d, nil)
	h, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "preview not supported; start with mode live")
	require.Empty(t, h.enqueuedRuns())
	_, err = runKind(t, def, tasks.ModeLive, &sentinelWriter{}, `{"apply":true}`)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
}

// The result names both children, as HEAD's did.
func TestDedupChain_LabelRefinementResultNamesTheChildren(t *testing.T) {
	_, d := newChainSpy()
	def := (&Plugin{}).labelRefinementNative(d, nil)
	r, ok := def.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok)
	_, task, _, _ := r.ExecutorRunners()
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	out, err := task.Run(tasks.NewRC(h), nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"dry_run":true,"rebuild_op":"child-1","calibrate_op":"child-2"}`, string(out))
}

// --- exact progress, logs and result shapes (mutation survivors of the first run) ---

// barsOf is the bar frames a chain run is expected to report: Known totals of
// total, with the given done values in order.
func barsOf(total int64, dones ...int64) []barFrame {
	out := make([]barFrame, len(dones))
	for i, d := range dones {
		out[i] = barFrame{Done: d, Total: total, Known: true}
	}
	return out
}

// A Live run-all reports 1000 units a step: the step's start, one unit once its
// child is recorded and awaited, and the step's end; the rescore step has no
// child, so no waiting frame.
func TestDedupChain_RunAllLiveReportsExactProgressAndNotes(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h, out, err := runKindResult(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, barsOf(5000,
		0,                                                                // starting
		0, 1, 1000, 1000, 1001, 2000, 2000, 2001, 3000, 3000, 3001, 4000, // four child steps
		4000, 5000, // the rescore step
		5000, // finished
	), h.bars())
	h.mu.Lock()
	notes := strings.Join(h.notes, "\n")
	h.mu.Unlock()
	for _, want := range []string{"Starting at step 1 of 5", "Step 1 of 5: ", "Step 5 of 5: ", " — done", " — waiting for "} {
		require.Contains(t, notes, want)
	}
	var res RunAllResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.Len(t, res.Steps, 5, "the result lists every finished step")
	require.NotContains(t, h.logged(), "checkpoint failed")
	require.NotContains(t, h.logged(), "Resuming")
}

// A resumed run-all reports from the recorded step, says so, and does not
// announce skipped steps.
func TestDedupChain_RunAllResumeReportsFromTheRecordedStep(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	require.NoError(t, h.FakeRun.SetCheckpoint("", runAllState{
		Plan:      []string{"embeddings", "acoustic", "find", "ai-review", "rescore-preview"},
		StepIndex: 2, ChildOpID: "child-find",
		Finished: []RunAllStepRecord{{ID: "embeddings"}, {ID: "acoustic"}},
	}))
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, barFrame{Done: 2000, Total: 5000, Known: true}, h.bars()[0])
	require.Contains(t, h.notes[0], "Starting at step 3 of 5")
	require.Contains(t, h.logged(), "Resuming at step 3 of 5")
	require.Equal(t, barsOf(5000, 2000, 2000, 3000), h.bars()[:3], "a recorded child that already finished moves the bar a whole step")
}

// A checkpoint with no plan is not a resume: the plan is built fresh and a
// step turned off in Settings is announced.
func TestDedupChain_RunAllEmptyCheckpointIsNotAResume(t *testing.T) {
	setEmbeddings(t, false)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	require.NoError(t, h.FakeRun.SetCheckpoint("", runAllState{}))
	require.NoError(t, runKindOn(t, def, h, ""))
	require.NotContains(t, h.logged(), "Resuming")
	require.Contains(t, h.logged(), "Skipping step")
}

// A failed checkpoint save is logged and the run goes on.
func TestDedupChain_RunAllCheckpointFailureIsLoggedNotFatal(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	h.FakeRun.FailCheckpoints(errors.New("pebble closed"))
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Contains(t, h.logged(), "checkpoint failed")
}

// label-refinement reports 1000 units a step: the step's start, one unit while
// its child runs, then the closing frame.
func TestDedupChain_LabelRefinementReportsExactProgress(t *testing.T) {
	_, d := newChainSpy()
	def := (&Plugin{}).labelRefinementNative(d, nil)
	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, barsOf(2000, 0, 1, 1000, 1001, 2000), h.bars())
}

// The two chain parents declare a 15 minute progress timeout (a child wait with
// no row progress must not trip the watchdog sooner).
func TestDedupChain_ParentsDeclareAFifteenMinuteProgressTimeout(t *testing.T) {
	_, d := newChainSpy()
	p := &Plugin{}
	require.Equal(t, 15*time.Minute, tasks.CommonOf(p.runAllNative(d, nil)).ProgressTimeout)
	require.Equal(t, 15*time.Minute, tasks.CommonOf(p.labelRefinementNative(d, nil)).ProgressTimeout)
}

// A panic in the wait on a child is the wait's error, not a hung run.
func TestDedupChain_AwaitPanicIsReportedAsTheWaitsError(t *testing.T) {
	setEmbeddings(t, true)
	_, d := newChainSpy()
	def := (&Plugin{}).runAllNative(d, nil)
	h := newKindHost(def, tasks.ModeLive, &sentinelWriter{})
	h.onAwait = func(tasks.RunRef) (tasks.Outcome, error) { panic("kernel gone") }
	done := make(chan error, 1)
	go func() { done <- runKindOn(t, def, h, "") }()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "panicked: kernel gone")
	case <-time.After(10 * time.Second):
		t.Fatal("a panic in Await left the run waiting")
	}
}
