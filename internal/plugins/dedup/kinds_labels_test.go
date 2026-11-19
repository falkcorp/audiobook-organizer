// file: internal/plugins/dedup/kinds_labels_test.go
// version: 1.1.0
// guid: 0aabdd46-6c86-4f1e-a2cd-6dc9ff6f2e64
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/models"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// labelSpy is the store, writer, scorer, baseline config and review engine a
// test hands the labels kinds. It implements every method of labelKindStore and
// labelKindWriter, pairScorer, calibrateCompositeBaseline and llmReviewEngine
// itself (the compiler checks that where labelFakeDeps returns it), so a method
// added to any of those breaks this file until the spy answers it. Fixture ids
// are Batman comic titles.
type labelSpy struct {
	mu      sync.Mutex
	writers []tasks.Writer
	calls   []string

	candidates []database.DedupCandidate
	byID       map[int64]database.DedupCandidate
	books      map[string]*database.Book
	files      map[string][]database.BookFile
	examples   []database.LabeledExample

	listErr, upsertErr, deleteErr, applyErr, scoreErr, runErr error
	llmUnconfigured                                           bool
}

func (s *labelSpy) rec(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

// --- reads ---

func (s *labelSpy) ListCandidates(database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	if s.listErr != nil {
		return nil, 0, s.listErr
	}
	return append([]database.DedupCandidate(nil), s.candidates...), len(s.candidates), nil
}
func (s *labelSpy) GetBookByID(id string) (*database.Book, error) { return s.books[id], nil }
func (s *labelSpy) GetBookFiles(id string) ([]database.BookFile, error) {
	return s.files[id], nil
}
func (s *labelSpy) ListLabeledExamples(f database.LabeledExampleFilter) ([]database.LabeledExample, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []database.LabeledExample
	for _, e := range s.examples {
		if f.Label == "" || e.Label == f.Label {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *labelSpy) GetCandidateByID(id int64) (*database.DedupCandidate, error) {
	if c, ok := s.byID[id]; ok {
		return &c, nil
	}
	return nil, nil
}
func (s *labelSpy) GetLabeledExample(id int64) (*database.LabeledExample, error) {
	for _, e := range s.examples {
		if e.CandidateID == id {
			e := e
			return &e, nil
		}
	}
	return nil, nil
}
func (s *labelSpy) ScoreConfig() unified.ScoreConfig { return unified.DefaultScoreConfig() }

// ScorePairsForBook scores every pair at 91 with one signal, except the
// dark-victory/hush pair, which has none (unscorable).
func (s *labelSpy) ScorePairsForBook(_ context.Context, aID string, in []dedupengine.RescorePairInput) ([]dedupengine.RescorePairResult, error) {
	if s.scoreErr != nil {
		return nil, s.scoreErr
	}
	out := make([]dedupengine.RescorePairResult, len(in))
	for i, p := range in {
		out[i] = dedupengine.RescorePairResult{OtherID: p.OtherID, Score: &unified.UnifiedDedupScore{Score: 91, Band: "HIGH"}, NumSignals: 1}
		if aID == "dark-victory" && p.OtherID == "hush" {
			out[i].NumSignals = 0
		}
	}
	return out, nil
}

// --- writes ---

func (s *labelSpy) UpsertLabeledExample(ex database.LabeledExample) error {
	s.rec(fmt.Sprintf("Upsert %d %s %s", ex.CandidateID, ex.LabelSource, ex.Band))
	return s.upsertErr
}
func (s *labelSpy) DeleteLabeledExamplesBySource(sources ...string) (int, error) {
	s.rec("Delete " + strings.Join(sources, ","))
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	return 3, nil
}
func (s *labelSpy) ApplyConfigUpdates(context.Context, map[string]any) error {
	s.rec("ApplyConfigUpdates")
	return s.applyErr
}
func (s *labelSpy) PersistedScoreConfig() (unified.ScoreConfig, error) {
	s.rec("PersistedScoreConfig")
	return unified.DefaultScoreConfig(), nil
}
func (s *labelSpy) ReloadScoreConfig(context.Context, unified.ScoreConfig) (dedupengine.RescoreResult, error) {
	s.rec("ReloadScoreConfig")
	return dedupengine.RescoreResult{Inspected: 7, Changed: 2}, nil
}

// --- review engine ---

func (s *labelSpy) LLMReviewConfigured() bool { return !s.llmUnconfigured }
func (s *labelSpy) RunLLMReviewWithHooks(_ context.Context, h dedupengine.LLMReviewHooks) error {
	s.rec("RunLLMReview")
	if s.runErr != nil {
		return s.runErr
	}
	if h.Heartbeat != nil {
		h.Heartbeat("listing")
	}
	if h.Progress != nil {
		h.Progress(1, 2, "building LLM review inputs: 1/2 pairs")
		h.Progress(2, 2, "building LLM review inputs: 2/2 pairs")
	}
	return nil
}

func (s *labelSpy) factory(w tasks.Writer) labelKindWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *labelSpy) engineFactory(w tasks.Writer) llmReviewEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *labelSpy) seenWriters() []tasks.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tasks.Writer(nil), s.writers...)
}

// recorded is the writes in a stable order (the pools run in any order).
func (s *labelSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.calls...)
	sort.Strings(out)
	return out
}

func newLabelSpy() (*labelSpy, LabelKindDeps) {
	hash := func(id, h string) []database.BookFile {
		return []database.BookFile{{ID: id + "-f1", BookID: id, FileHash: h}}
	}
	one := 1.0
	c1 := database.DedupCandidate{ID: 1, EntityAID: "year-one", EntityBID: "long-halloween", Layer: "exact", Similarity: &one, Status: "pending"}
	c2 := database.DedupCandidate{ID: 2, EntityAID: "dark-victory", EntityBID: "hush", Layer: "exact", Similarity: &one, Status: "pending"}
	c3 := database.DedupCandidate{ID: 3, EntityAID: "year-one", EntityBID: "ghost", Layer: "exact", Similarity: &one, Status: "pending"}
	s := &labelSpy{
		candidates: []database.DedupCandidate{c1, c2, c3},
		byID:       map[int64]database.DedupCandidate{1: c1, 2: c2, 3: c3},
		books: map[string]*database.Book{
			"year-one": {ID: "year-one"}, "long-halloween": {ID: "long-halloween"},
			"dark-victory": {ID: "dark-victory"}, "hush": {ID: "hush"},
		},
		files: map[string][]database.BookFile{
			"year-one": hash("year-one", "h-shared"), "long-halloween": hash("long-halloween", "h-shared"),
			"dark-victory": hash("dark-victory", "h-dv"), "hush": hash("hush", "h-hush"),
		},
		examples: []database.LabeledExample{
			{CandidateID: 1, EntityAID: "year-one", EntityBID: "long-halloween", Label: "true_dup", LabelSource: "human"},
			{CandidateID: 2, EntityAID: "year-one", EntityBID: "hush", Label: "not_dup", LabelSource: "auto_high_conf"},
			{CandidateID: 3, EntityAID: "dark-victory", EntityBID: "hush", Label: "not_dup", LabelSource: "rule"},
			{CandidateID: 99, EntityAID: "ghost", EntityBID: "hush", Label: "not_dup", LabelSource: "auto_high_conf"},
			{CandidateID: 98, EntityAID: "ghost", EntityBID: "year-one", Label: "unsure", LabelSource: ""},
		},
	}
	return s, LabelKindDeps{Store: s, Scorer: s, Baseline: s, NewWriter: s.factory, NewEngine: s.engineFactory}
}

func labelFakeDeps(t *testing.T) LabelKindDeps {
	t.Helper()
	_, d := newLabelSpy()
	return d
}

func labelRows() []kindRow {
	return []kindRow{
		{
			HeadID: "dedup.mine-gold-labels", Title: "Mine high-confidence dedup labels",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResBookFiles},
			Par: []string{"cpu"}, ParGot: []tasks.Parallelism{tasks.CPU()},
			HeadKey: "dedup.mine-gold-labels",
		},
		{
			HeadID: "dedup.rebuild-gold-labels", Title: "Rebuild mechanically-derived gold labels",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResBookFiles},
			HeadKey: "dedup.rebuild-gold-labels",
		},
		{
			HeadID: "dedup.rescore-labeled-examples", Title: "Rescore labeled examples (populate ScoreBreakdowns for calibration)",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 60 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings},
			Reads:  []ops.Resource{ops.ResBooks, ops.ResBookFiles, ops.ResAuthors, ops.ResIndexes},
			Par:    []string{"cpu"}, ParGot: []tasks.Parallelism{tasks.CPU()},
			HeadKey: "dedup.rescore-labeled-examples",
		},
		{
			HeadID: "dedup.calibrate-composite", Title: "Calibrate composite scorer (report; apply gated)",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityNormal, Timeout: 30 * time.Minute,
			Writes:  []ops.Resource{ops.ResSettings, ops.ResEmbeddings},
			HeadKey: "dedup.calibrate-composite",
		},
		{
			HeadID: "dedup.llm-review", Title: "LLM review of candidates",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityLow, Timeout: 120 * time.Minute,
			Writes: []ops.Resource{ops.ResEmbeddings}, Reads: []ops.Resource{ops.ResBooks, ops.ResAuthors},
			HeadKey: "dedup.llm-review",
		},
	}
}

// labelNative lists the five native definitions over d.
func labelNative(d LabelKindDeps) map[string]tasks.Definition {
	p := &Plugin{}
	return map[string]tasks.Definition{
		"mine":      p.mineGoldLabelsNative(d, nil),
		"rebuild":   p.rebuildGoldLabelsNative(d, nil),
		"rescore":   p.rescoreLabeledExamplesNative(d, nil),
		"calibrate": p.calibrateCompositeNative(d, nil),
		"llm":       p.llmReviewNative(d, nil),
	}
}

func labelWriteProbes(t *testing.T) []writeProbe {
	t.Helper()
	var out []writeProbe
	for _, name := range []string{"mine", "rebuild", "rescore", "calibrate", "llm"} {
		spy, d := newLabelSpy()
		if name == "calibrate" {
			spy.examples = calibrateExamples(30, 30, "separable")
		}
		def := labelNative(d)[name]
		pr := writeProbe{Kind: def.Kind(), Def: def, Writers: spy.seenWriters, Calls: spy.recorded}
		if name == "calibrate" {
			pr.Params = `{"min_scored_pairs":10}`
		}
		out = append(out, pr)
	}
	return out
}

// calibrateExamples is nTrue true_dup and nNot not_dup labeled pairs with one-signal
// breakdowns: "separable" scores true_dup at 99 and not_dup at 80 (both bands
// meet target, so apply fires); "inseparable" scores both at the same value.
func calibrateExamples(nTrue, nNot int, shape string) []database.LabeledExample {
	bd := func(kind unified.SignalKind, conf float64) json.RawMessage {
		b, _ := json.Marshal(models.UnifiedDedupScore{Signals: []models.Signal{{Kind: kind, Confidence: conf, Raw: conf}}})
		return b
	}
	tdKind, tdConf, ndKind, ndConf := unified.SigExactAcoustID, 0.99, unified.SigMetaFuzzy, 0.80
	if shape == "inseparable" {
		tdKind, tdConf, ndKind, ndConf = unified.SigEmbedHigh, 0.92, unified.SigEmbedHigh, 0.92
	}
	var out []database.LabeledExample
	add := func(base, n int, label string, kind unified.SignalKind, conf float64) {
		for i := range n {
			id := base + i
			out = append(out, database.LabeledExample{
				CandidateID: int64(id), EntityAID: fmt.Sprintf("a%06d", id), EntityBID: fmt.Sprintf("b%06d", id),
				Label: label, LabelSource: "rule", ScoreBreakdown: bd(kind, conf),
			})
		}
	}
	add(100000, nTrue, "true_dup", tdKind, tdConf)
	add(200000, nNot, "not_dup", ndKind, ndConf)
	return out
}

func requireModeKeysRefused(t *testing.T, def tasks.Definition, params string) {
	t.Helper()
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, params)
	require.ErrorContains(t, err, "the run's mode comes from the start request")
}

// --- group tests ---

func TestDedupLabels_ListsFiveKinds(t *testing.T) {
	_, d := newLabelSpy()
	require.Len(t, labelKinds(&Plugin{}, d, nil), 5)
}

// --- mine-gold-labels ---

func TestDedupLabels_MinePreviewWritesNothingAndBuildsNoWriter(t *testing.T) {
	spy, d := newLabelSpy()
	h, err := runKind(t, labelNative(d)["mine"], tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.Equal(t, "Dry-run — 1 of 3 candidates are high-confidence true_dup. Pass apply=true to write.", h.lastNote())
}

func TestDedupLabels_MineLiveUpsertsTheMinedLabelThroughTheWriter(t *testing.T) {
	spy, d := newLabelSpy()
	w := &sentinelWriter{}
	h, err := runKind(t, labelNative(d)["mine"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Upsert 1 auto_high_conf "}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Equal(t, "Complete — wrote 1 true_dup labels. examined=3 mined=1 written=1 errs=1 (apply=true)", h.lastNote())
}

func TestDedupLabels_MineUpsertErrorIsCountedNotFatal(t *testing.T) {
	spy, d := newLabelSpy()
	spy.upsertErr = errors.New("disk full")
	h, err := runKind(t, labelNative(d)["mine"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Contains(t, h.lastNote(), "mined=1 written=0 errs=2")
}

func TestDedupLabels_MineListErrorFailsTheRun(t *testing.T) {
	spy, d := newLabelSpy()
	spy.listErr = errors.New("pebble hiccup")
	_, err := runKind(t, labelNative(d)["mine"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "list candidates")
}

func TestDedupLabels_MineRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newLabelSpy()
	requireModeKeysRefused(t, labelNative(d)["mine"], `{"apply":true}`)
	_, err := runKind(t, labelNative(LabelKindDeps{Store: spy})["mine"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.NewWriter is not set")
	_, err = runKind(t, labelNative(LabelKindDeps{})["mine"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.Store is not set")
}

func TestDedupLabels_MineUsesTheCPUPool(t *testing.T) {
	_, d := newLabelSpy()
	def := labelNative(d)["mine"]
	h := newKindHost(def, tasks.ModePreview, &sentinelWriter{})
	var declared []int
	h.capObserver = func(n int) { declared = append(declared, n) }
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []int{tasks.CPU().Workers()}, declared)
}

func TestDedupLabels_MineHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runMineGoldLabels(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}

// --- rebuild-gold-labels ---

func TestDedupLabels_RebuildPreviewReportsTheDiffAndWritesNothing(t *testing.T) {
	spy, d := newLabelSpy()
	h, err := runKind(t, labelNative(d)["rebuild"], tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.True(t, strings.HasPrefix(h.lastNote(), "Dry-run — 1 rule + 0 auto_high_conf would change, 2 would become unlabelable."), h.lastNote())
}

func TestDedupLabels_RebuildLiveWipesTheMechanicalLabelsAndReinsertsTheFreshSet(t *testing.T) {
	spy, d := newLabelSpy()
	w := &sentinelWriter{}
	h, err := runKind(t, labelNative(d)["rebuild"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Delete rule,auto_high_conf", "Upsert 3 rule "}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.True(t, strings.HasPrefix(h.lastNote(), "Complete — deleted 3, wrote 1 fresh (0 errors)."), h.lastNote())
}

func TestDedupLabels_RebuildErrorPaths(t *testing.T) {
	spy, d := newLabelSpy()
	spy.listErr = errors.New("pebble hiccup")
	_, err := runKind(t, labelNative(d)["rebuild"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "list labeled examples")

	spy, d = newLabelSpy()
	spy.deleteErr = errors.New("disk full")
	_, err = runKind(t, labelNative(d)["rebuild"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "delete rule/auto_high_conf examples")
	require.NotContains(t, strings.Join(spy.recorded(), ";"), "Upsert", "a failed wipe must not reinsert")
}

func TestDedupLabels_RebuildRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newLabelSpy()
	requireModeKeysRefused(t, labelNative(d)["rebuild"], `{"dry_run":true}`)
	_, err := runKind(t, labelNative(LabelKindDeps{Store: spy})["rebuild"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.NewWriter is not set")
	_, err = runKind(t, labelNative(LabelKindDeps{})["rebuild"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.Store is not set")
}

func TestDedupLabels_RebuildHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runRebuildGoldLabels(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}

// --- rescore-labeled-examples ---

func TestDedupLabels_RescorePreviewCountsAndWritesNothing(t *testing.T) {
	spy, d := newLabelSpy()
	h, err := runKind(t, labelNative(d)["rescore"], tasks.ModePreview, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.Equal(t, "Dry-run — scored true_dup=1 not_dup=2; zero_signal true_dup=0 not_dup=1; would_write=3 wrote=0 human_preserved=0 upsert_errs=0. Pass apply=true to persist ScoreBreakdowns.", h.lastNote())
}

func TestDedupLabels_RescoreLiveNarrowWritesTheScoredPairsOnly(t *testing.T) {
	spy, d := newLabelSpy()
	w := &sentinelWriter{}
	h, err := runKind(t, labelNative(d)["rescore"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	// The pair under the human label keeps its source; only Band/Score are set.
	require.Equal(t, []string{"Upsert 1 human HIGH", "Upsert 2 auto_high_conf HIGH", "Upsert 99 auto_high_conf HIGH"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Equal(t, "Complete — scored true_dup=1 not_dup=2; zero_signal true_dup=0 not_dup=1; would_write=3 wrote=3 human_preserved=1 upsert_errs=0.", h.lastNote())
}

func TestDedupLabels_RescoreScoreErrorIsCountedNotFatalAndListErrorFails(t *testing.T) {
	spy, d := newLabelSpy()
	spy.scoreErr = errors.New("engine hiccup")
	h, err := runKind(t, labelNative(d)["rescore"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Contains(t, h.lastNote(), "would_write=0 wrote=0")

	spy, d = newLabelSpy()
	spy.listErr = errors.New("pebble hiccup")
	_, err = runKind(t, labelNative(d)["rescore"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "list true_dup examples")
}

func TestDedupLabels_RescoreRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newLabelSpy()
	requireModeKeysRefused(t, labelNative(d)["rescore"], `{"apply":true}`)
	_, err := runKind(t, labelNative(LabelKindDeps{Store: spy, Scorer: spy})["rescore"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.NewWriter is not set")
	_, err = runKind(t, labelNative(LabelKindDeps{Store: spy})["rescore"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.Scorer is not set")
	_, err = runKind(t, labelNative(LabelKindDeps{})["rescore"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.Store is not set")
}

func TestDedupLabels_RescoreUsesTheCPUPool(t *testing.T) {
	_, d := newLabelSpy()
	def := labelNative(d)["rescore"]
	h := newKindHost(def, tasks.ModePreview, &sentinelWriter{})
	var declared []int
	h.capObserver = func(n int) { declared = append(declared, n) }
	require.NoError(t, runKindOn(t, def, h, ""))
	require.Equal(t, []int{tasks.CPU().Workers()}, declared)
}

func TestDedupLabels_RescoreHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runRescoreLabeledExamples(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}

// --- calibrate-composite ---

func TestDedupLabels_CalibratePreviewReportsAndWritesNothing(t *testing.T) {
	spy, d := newLabelSpy()
	spy.examples = calibrateExamples(30, 30, "separable")
	h, err := runKind(t, labelNative(d)["calibrate"], tasks.ModePreview, &sentinelWriter{}, `{"min_scored_pairs":10}`)
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
	require.True(t, strings.HasPrefix(h.lastNote(), "Dry-run — scored_true=30 scored_not=30"), h.lastNote())
}

func TestDedupLabels_CalibrateLiveAppliesBandsThenReloadsThroughTheWriter(t *testing.T) {
	spy, d := newLabelSpy()
	spy.examples = calibrateExamples(30, 30, "separable")
	w := &sentinelWriter{}
	h, err := runKind(t, labelNative(d)["calibrate"], tasks.ModeLive, w, `{"min_scored_pairs":10}`)
	require.NoError(t, err)
	require.Equal(t, []string{"ApplyConfigUpdates", "PersistedScoreConfig", "ReloadScoreConfig"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Contains(t, h.lastNote(), "Applied band thresholds")
	require.Contains(t, h.lastNote(), "inspected=7 changed=2")
}

func TestDedupLabels_CalibrateTargetNotMetRefusesTheApply(t *testing.T) {
	spy, d := newLabelSpy()
	spy.examples = calibrateExamples(30, 30, "inseparable")
	h, err := runKind(t, labelNative(d)["calibrate"], tasks.ModeLive, &sentinelWriter{}, `{"min_scored_pairs":10}`)
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Contains(t, h.lastNote(), "Apply requested but target(s) not met — refused, nothing written.")
}

func TestDedupLabels_CalibrateInsufficientCoverageRecommendsNothing(t *testing.T) {
	spy, d := newLabelSpy()
	spy.examples = calibrateExamples(3, 3, "separable")
	h, err := runKind(t, labelNative(d)["calibrate"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Contains(t, h.lastNote(), "insufficient-coverage — scored true_dup=3 not_dup=3 < min 500")
}

func TestDedupLabels_CalibrateErrorPaths(t *testing.T) {
	spy, d := newLabelSpy()
	spy.listErr = errors.New("pebble hiccup")
	_, err := runKind(t, labelNative(d)["calibrate"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "list true_dup examples")

	spy, d = newLabelSpy()
	spy.examples = calibrateExamples(30, 30, "separable")
	spy.applyErr = errors.New("config locked")
	_, err = runKind(t, labelNative(d)["calibrate"], tasks.ModeLive, &sentinelWriter{}, `{"min_scored_pairs":10}`)
	require.ErrorContains(t, err, "persist band thresholds")
	require.NotContains(t, spy.recorded(), "ReloadScoreConfig", "a failed persist must not reload the engine")
}

func TestDedupLabels_CalibrateRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	spy, d := newLabelSpy()
	requireModeKeysRefused(t, labelNative(d)["calibrate"], `{"apply":true}`)
	_, err := runKind(t, labelNative(LabelKindDeps{Store: spy, Baseline: spy})["calibrate"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.NewWriter is not set")
	_, err = runKind(t, labelNative(LabelKindDeps{Store: spy})["calibrate"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.Baseline is not set")
	_, err = runKind(t, labelNative(LabelKindDeps{})["calibrate"], tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.Store is not set")
}

func TestDedupLabels_CalibrateHeadShellKeepsItsNilChecks(t *testing.T) {
	err := (&Plugin{}).runCalibrateComposite(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "embedding store not available")
}

// --- llm-review ---

func TestDedupLabels_LLMReviewRefusesPreview(t *testing.T) {
	spy, d := newLabelSpy()
	_, err := runKind(t, labelNative(d)["llm"], tasks.ModePreview, &sentinelWriter{}, "")
	require.Error(t, err)
	require.Empty(t, spy.recorded())
	require.Empty(t, spy.seenWriters())
}

func TestDedupLabels_LLMReviewLiveRunsTheEngineBuiltOverTheRunsWriter(t *testing.T) {
	spy, d := newLabelSpy()
	w := &sentinelWriter{}
	h, err := runKind(t, labelNative(d)["llm"], tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.Equal(t, []string{"RunLLMReview"}, spy.recorded())
	require.Equal(t, []tasks.Writer{w}, spy.seenWriters())
	require.Equal(t, "LLM review complete (100%)", h.lastNote())
}

func TestDedupLabels_LLMReviewSkipsWhenTheParserIsNotConfigured(t *testing.T) {
	spy, d := newLabelSpy()
	spy.llmUnconfigured = true
	h, err := runKind(t, labelNative(d)["llm"], tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Empty(t, spy.recorded())
	require.Equal(t, "LLM review skipped: LLM parser not configured", h.lastNote())
}

func TestDedupLabels_LLMReviewEngineErrorIsWrapped(t *testing.T) {
	spy, d := newLabelSpy()
	spy.runErr = errors.New("provider down")
	_, err := runKind(t, labelNative(d)["llm"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "LLM review: provider down")
}

func TestDedupLabels_LLMReviewRefusesModeKeysAndUnwiredDeps(t *testing.T) {
	_, d := newLabelSpy()
	requireModeKeysRefused(t, labelNative(d)["llm"], `{"apply":true}`)
	_, err := runKind(t, labelNative(LabelKindDeps{})["llm"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Labels.NewEngine is not set")
	nilEngine := LabelKindDeps{NewEngine: func(tasks.Writer) llmReviewEngine { return nil }}
	_, err = runKind(t, labelNative(nilEngine)["llm"], tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "the engine factory returned no engine")
}

func TestDedupLabels_LLMReviewHeadShellKeepsItsNilCheck(t *testing.T) {
	err := (&Plugin{}).runLLMReview(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}
