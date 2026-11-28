// file: internal/plugins/dedup/deps_books_test.go
// version: 0.1.0
// guid: 2f8d4b61-a7c3-4e90-9d15-6b0c3e7a1f48
// last-edited: 2026-11-28

package dedup

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// booksEngine is the dedup engine of these tests: it records the calls the
// builder's wiring makes on it and the writer each NewEngine call was handed.
type booksEngine struct {
	mu        sync.Mutex
	rescores  []bool
	reloads   int
	writers   []tasks.Writer
	scoreCfg  unified.ScoreConfig
	modelName string
}

func (e *booksEngine) AutoResolveCertain(context.Context, bool, int, int) (dedupengine.AutoResolveResult, error) {
	return dedupengine.AutoResolveResult{}, nil
}
func (e *booksEngine) BookSignatureScan(context.Context, func(done, total int)) error { return nil }
func (e *booksEngine) DrainStaleCandidates(context.Context, string, bool, dedupengine.DrainStaleProgressFunc) (*dedupengine.DrainStaleResult, error) {
	return &dedupengine.DrainStaleResult{}, nil
}
func (e *booksEngine) FullScan(context.Context, func(phase string, done, total int)) error {
	return nil
}
func (e *booksEngine) PurgeStaleCandidates(context.Context) (int, error) { return 0, nil }
func (e *booksEngine) Rescore(_ context.Context, apply bool) (dedupengine.RescoreResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rescores = append(e.rescores, apply)
	return dedupengine.RescoreResult{}, nil
}
func (e *booksEngine) EmbedBooksAsync(context.Context) (string, int, error) { return "", 0, nil }
func (e *booksEngine) EmbedBooks(context.Context, []string) (map[string]dedupengine.EmbedStatus, error) {
	return nil, nil
}
func (e *booksEngine) EmbedBook(context.Context, string) (dedupengine.EmbedStatus, error) {
	return 0, nil
}
func (e *booksEngine) EmbedConcurrency(fallback int) int { return fallback }
func (e *booksEngine) EmbedReadiness() error             { return nil }
func (e *booksEngine) ScorePairsForBook(context.Context, string, []dedupengine.RescorePairInput) ([]dedupengine.RescorePairResult, error) {
	return nil, nil
}
func (e *booksEngine) LLMReviewConfigured() bool { return true }
func (e *booksEngine) RunLLMReviewWithHooks(context.Context, dedupengine.LLMReviewHooks) error {
	return nil
}
func (e *booksEngine) ScoreConfig() unified.ScoreConfig { return e.scoreCfg }
func (e *booksEngine) EmbeddingModel() string           { return e.modelName }
func (e *booksEngine) ReloadScoreConfig(context.Context, unified.ScoreConfig) (dedupengine.RescoreResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reloads++
	return dedupengine.RescoreResult{}, nil
}

func (e *booksEngine) newEngine(w tasks.Writer) WriteEngine {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writers = append(e.writers, w)
	return e
}

type booksFix struct {
	store *database.PebbleStore
	emb   *database.EmbeddingStore
	eng   *booksEngine
	in    BooksTaskInputs
	sched []tasks.Kind
}

func newBooksFix(t *testing.T) *booksFix {
	t.Helper()
	store := newPebbleForISBNIndexTest(t)
	f := &booksFix{
		store: store,
		emb:   database.NewEmbeddingStore(store.DB()),
		eng:   &booksEngine{scoreCfg: unified.DefaultScoreConfig(), modelName: "bge-m3"},
	}
	f.in = BooksTaskInputs{
		Source: store, Indexes: store, Embeddings: f.emb,
		NewEngine: f.eng.newEngine, Engine: f.eng,
		Schedules: func(k tasks.Kind) *tasks.Schedule { f.sched = append(f.sched, k); return nil },
	}
	return f
}

func (f *booksFix) deps(t *testing.T) KindDeps {
	t.Helper()
	d, err := newBooksKindDeps(f.in)
	require.NoError(t, err)
	return d
}

// gateRec is a tasks.Writer that records the stamp of every gated write. record
// only: fn is not run unless run is set, so a stamp test touches no store.
type gateRec struct {
	refuse error
	run    bool
	mu     sync.Mutex
	got    []tasks.Write
}

func (g *gateRec) Do(ctx context.Context, w tasks.Write, fn func(context.Context) ([]tasks.Change, error)) error {
	g.mu.Lock()
	g.got = append(g.got, w)
	g.mu.Unlock()
	if g.refuse != nil {
		return g.refuse
	}
	if g.run {
		_, err := fn(ctx)
		return err
	}
	return nil
}
func (g *gateRec) MarkRowApplied(context.Context, string) {}
func (g *gateRec) RowApplied(context.Context, string) (bool, error) {
	return false, nil
}

// bwCalls is one call of every method of booksWriter, by Go method name.
func bwCalls(w *booksWriter) map[string]func() error {
	ctx := context.Background()
	cfg := unified.DefaultScoreConfig()
	return map[string]func() error{
		"SetSetting": func() error { return w.SetSetting("dedup.test.key", "v", "string", false) },
		"ModifyBook": func() error {
			_, err := w.ModifyBook("bruce", func(*database.Book) error { return nil })
			return err
		},
		"EnsureSinglePrimary": func() error { return w.EnsureSinglePrimary(ctx, "group-wayne") },
		"Delete":              func() error { return w.Delete("book", "bruce") },
		"ReclassifyCandidate": func() error { return w.ReclassifyCandidate(1, "pending", "dismissed") },
		"UpdateCandidateScores": func() error {
			_, _, err := w.UpdateCandidateScores(nil)
			return err
		},
		"SyncCandidateWrites": func() error { return w.SyncCandidateWrites() },
		"UpsertLabeledExample": func() error {
			return w.UpsertLabeledExample(database.LabeledExample{CandidateID: 1, Label: "true_dup"})
		},
		"DeleteLabeledExamplesBySource": func() error {
			_, err := w.DeleteLabeledExamplesBySource("rule")
			return err
		},
		"ReencodeRows":                 func() error { return w.ReencodeRows(nil) },
		"SetCandidateStatusIndexBuilt": func() error { return w.SetCandidateStatusIndexBuilt() },
		"WriteCandidateStatusIndexRow": func() error { return w.WriteCandidateStatusIndexRow(1, "pending") },
		"SetISBNIndexBuilt":            func() error { return w.SetISBNIndexBuilt() },
		"WriteISBNIndexForBook":        func() error { return w.WriteISBNIndexForBook("bruce", "", "", "B0TEST") },
		"SetWindowIndexBuilt":          func() error { return w.SetWindowIndexBuilt() },
		"PutWindowIndex": func() error {
			_, err := w.PutWindowIndex("file-wayne")
			return err
		},
		"SetLSHIndexBuilt": func() error { return w.SetLSHIndexBuilt() },
		"PutLSHEntries": func() error {
			return w.PutLSHEntries("file-wayne", "bruce", []fingerprint.Subprint{{}}, []byte{1})
		},
		"SweepBookFileSegDrop": func() error {
			_, err := w.SweepBookFileSegDrop(ctx, 10, nil)
			return err
		},
		"PersistBandThresholds": func() error { return w.PersistBandThresholds(ctx, cfg) },
		"PersistedScoreConfig": func() error {
			_, err := w.PersistedScoreConfig()
			return err
		},
		"ReloadScoreConfig": func() error {
			_, err := w.ReloadScoreConfig(ctx, cfg)
			return err
		},
	}
}

// bsCalls is one call of every method of booksSplitWriter.
func bsCalls(w *booksSplitWriter) map[string]func() error {
	return map[string]func() error{
		"SaveAll": func() error { return w.SaveAll(nil) },
		"Delete":  func() error { return w.Delete("gotham") },
		"MergeSplitBookClusterRechecked": func() error {
			_, err := w.MergeSplitBookClusterRechecked([]string{"bruce"}, "bruce", "Year One", "op")
			return err
		},
	}
}

// gateLabel is the gate row name of a Go method where it differs.
var gateLabel = map[string]string{"Delete": "DeleteEmbedding"}
var splitGateLabel = map[string]string{
	"SaveAll": "SaveSplitBookCandidates", "Delete": "DeleteSplitBookCandidate",
	"MergeSplitBookClusterRechecked": "MergeSplitBookClusterRechecked",
}

func ifaceMethods(types ...reflect.Type) []string {
	set := map[string]bool{}
	for _, ty := range types {
		for i := 0; i < ty.NumMethod(); i++ {
			set[ty.Method(i).Name] = true
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestBooksTaskLists_BuildsEveryKindWithItsSchedule(t *testing.T) {
	f := newBooksFix(t)
	defs, err := BooksTaskLists(f.in)
	require.NoError(t, err)
	require.Len(t, defs, 31)
	seen := map[tasks.Kind]bool{}
	for _, d := range defs {
		require.False(t, seen[d.Kind()], "two definitions of %s", d.Kind())
		seen[d.Kind()] = true
	}
	require.Len(t, f.sched, 31, "every kind takes the schedule hook")
	for _, k := range []tasks.Kind{labelRefinementKind, llmReviewKind} {
		require.True(t, seen[k], "%s", k)
	}
}

func TestBooksTaskLists_RefusesAMissingInputByName(t *testing.T) {
	for _, c := range []struct {
		name, named string
		drop        func(*BooksTaskInputs)
	}{
		{"Source", "Source", func(in *BooksTaskInputs) { in.Source = nil }},
		{"Indexes", "Indexes", func(in *BooksTaskInputs) { in.Indexes = nil }},
		{"Embeddings", "Embeddings", func(in *BooksTaskInputs) { in.Embeddings = nil }},
		{"Engine", "Engine", func(in *BooksTaskInputs) { in.Engine = nil }},
		{"Schedules", "Schedules", func(in *BooksTaskInputs) { in.Schedules = nil }},
		// a typed nil in a non-nil interface is as missing as a nil one
		{"Source typed nil", "Source", func(in *BooksTaskInputs) { in.Source = (*database.PebbleStore)(nil) }},
		{"Indexes typed nil", "Indexes", func(in *BooksTaskInputs) { in.Indexes = (*database.PebbleStore)(nil) }},
		{"Engine typed nil", "Engine", func(in *BooksTaskInputs) { in.Engine = (*booksEngine)(nil) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newBooksFix(t)
			c.drop(&f.in)
			defs, err := BooksTaskLists(f.in)
			require.Error(t, err)
			require.Nil(t, defs)
			require.Contains(t, err.Error(), "missing input(s): ["+c.named+"]")
		})
	}
}

// Every method of every writer interface has a call here and a row, and the
// split writer's likewise: a method added to an interface fails here first.
func TestBooksWriter_CoversEveryMethodOfTheInterfaces(t *testing.T) {
	f := newBooksFix(t)
	d := f.deps(t)
	w := d.Index.NewWriter(nil).(*booksWriter)
	want := ifaceMethods(
		reflect.TypeFor[scoringKindWriter](), reflect.TypeFor[embeddingKindWriter](),
		reflect.TypeFor[indexKindWriter](), reflect.TypeFor[labelKindWriter](),
		reflect.TypeFor[lshIndexBuildWriter]())
	require.Equal(t, want, keys(bwCalls(w)), "bwCalls must call exactly the methods of the writer interfaces")
	require.Equal(t, ifaceMethods(reflect.TypeFor[splitBookKindWriter]()), keys(bsCalls(&booksSplitWriter{})))
	rows, err := booksKindRows()
	require.NoError(t, err)
	for _, m := range want {
		_, ok := rows[gateLabel2(m)]
		require.True(t, ok, "%s has no row: the gate would refuse it", m)
	}
	for _, m := range keys(bsCalls(&booksSplitWriter{})) {
		_, ok := rows[splitGateLabel[m]]
		require.True(t, ok, "split %s has no row", m)
	}
}

func gateLabel2(m string) string {
	if l, ok := gateLabel[m]; ok {
		return l
	}
	return m
}

// Each method is ONE gate call stamped with its whole row; the one empty-row
// method (a config read) makes none.
func TestBooksWriter_EveryCallIsOneStampedWholeRow(t *testing.T) {
	rows, err := booksKindRows()
	require.NoError(t, err)
	check := func(t *testing.T, rec *gateRec, label string) {
		stamps := rec.got
		if len(rows[label]) == 0 {
			require.Empty(t, stamps, "%s has an empty row and stays outside the fence", label)
			return
		}
		require.Len(t, stamps, 1, "%s: gated calls", label)
		var want, got []string
		for _, r := range rows[label] {
			want = append(want, string(r))
		}
		for _, r := range stamps[0].Resources {
			got = append(got, r.Kind)
		}
		require.ElementsMatch(t, want, got, "the stamp is the method's whole row")
		require.Equal(t, "books."+label, stamps[0].Name)
	}
	f := newBooksFix(t)
	for m := range bwCalls(&booksWriter{}) {
		t.Run(m, func(t *testing.T) {
			rec := &gateRec{}
			w := f.deps(t).Index.NewWriter(rec).(*booksWriter)
			_ = bwCalls(w)[m]()
			check(t, rec, gateLabel2(m))
		})
	}
	for m := range bsCalls(&booksSplitWriter{}) {
		t.Run("split "+m, func(t *testing.T) {
			rec := &gateRec{}
			w := f.deps(t).Scoring.NewSplitWriter(rec).(*booksSplitWriter)
			_ = bsCalls(w)[m]()
			check(t, rec, splitGateLabel[m])
		})
	}
}

// A refused write (a preview, a fence, outside the effects) and a run with no
// writer at all do nothing: the stores are as they were and the engine's re-band
// did not run.
func TestBooksWriter_RefusalDoesNothing(t *testing.T) {
	for name, mk := range map[string]func() tasks.Writer{
		"preview":   func() tasks.Writer { return &gateRec{refuse: tasks.ErrPreview} },
		"fenced":    func() tasks.Writer { return &gateRec{refuse: tasks.ErrFenced} },
		"outside":   func() tasks.Writer { return &gateRec{refuse: tasks.ErrOutsideEffects} },
		"no writer": func() tasks.Writer { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newBooksFix(t)
			d := f.deps(t)
			w := d.Index.NewWriter(mk()).(*booksWriter)
			for m, call := range bwCalls(w) {
				err := call()
				if m == "PersistedScoreConfig" {
					continue // a read: never refused
				}
				require.Error(t, err, m)
				if name == "no writer" {
					require.ErrorIs(t, err, tasks.ErrOutsideEffects, m)
				}
			}
			s := d.Scoring.NewSplitWriter(mk()).(*booksSplitWriter)
			for m, call := range bsCalls(s) {
				require.Error(t, call(), m)
			}
			require.Zero(t, f.eng.reloads, "the engine's re-band ran after a refusal")
			set, _ := f.store.GetSetting("dedup.test.key") // not-found is an error here
			require.Nil(t, set, "SetSetting wrote after a refusal")
		})
	}
}

// Every kind that writes through the builder's writer is admitted by its own
// declared Effects: no stamp is wider than what the kind declares. The engine
// kinds are not here: the engine writes through its own stores.
func TestBooksWriter_EveryKindIsAdmittedByItsOwnEffects(t *testing.T) {
	type kw struct {
		kind  tasks.Kind
		write []reflect.Type
		split bool
	}
	ty := func(ts ...reflect.Type) []reflect.Type { return ts }
	cases := []kw{
		{drainStaleKind, ty(reflect.TypeFor[drainStaleWriter]()), false},
		{quarantineChapterArtifactsKind, ty(reflect.TypeFor[quarantineChapterArtifactsWriter]()), false},
		{buildISBNIndexKind, ty(reflect.TypeFor[buildISBNIndexWriter]()), false},
		{buildCandidateStatusIndexKind, ty(reflect.TypeFor[buildCandidateStatusIndexWriter]()), false},
		{bookfileSegDropKind, ty(reflect.TypeFor[bookfileSegDropWriter]()), false},
		{purgeLegacyFPKind, ty(reflect.TypeFor[purgeLegacyFPWriter]()), false},
		{windowIndexBuildKind, ty(reflect.TypeFor[windowIndexBuildWriter]()), false},
		{cleanupOrphanEmbeddingsKind, ty(reflect.TypeFor[cleanupOrphanEmbeddingsWriter]()), false},
		{cleanupOrphanAuthorEmbeddingsKind, ty(reflect.TypeFor[cleanupOrphanAuthorEmbeddingsWriter]()), false},
		{reembedEmbeddingsKind, ty(reflect.TypeFor[reembedEmbeddingsWriter]()), false},
		{breakdownBackfillKind, ty(reflect.TypeFor[breakdownBackfillWriter]()), false},
		{datasetBackfillKind, ty(reflect.TypeFor[datasetBackfillWriter]()), false},
		{embReencodeKind, ty(reflect.TypeFor[embReencodeWriter]()), false},
		{mineGoldLabelsKind, ty(reflect.TypeFor[mineGoldLabelsWriter]()), false},
		{rebuildGoldLabelsKind, ty(reflect.TypeFor[rebuildGoldLabelsWriter]()), false},
		{rescoreLabeledExamplesKind, ty(reflect.TypeFor[rescoreLabeledExamplesWriter]()), false},
		{calibrateCompositeKind, ty(reflect.TypeFor[calibrateCompositeWriter]()), false},
		{lshIndexBuildKind, ty(reflect.TypeFor[lshIndexBuildWriter]()), false},
		{splitBookScanKind, ty(reflect.TypeFor[splitBookScanWriter]()), true},
		{splitBookBulkMergeKind, ty(reflect.TypeFor[splitBookBulkMergeWriter]()), true},
	}
	f := newBooksFix(t)
	defs, err := BooksTaskLists(f.in)
	require.NoError(t, err)
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			def := byKind(t, defs, c.kind)
			enforcing := writertest.New(def, tasks.ModeLive)
			var calls map[string]func() error
			d := f.deps(t)
			if c.split {
				calls = bsCalls(d.Scoring.NewSplitWriter(enforcing).(*booksSplitWriter))
			} else {
				calls = bwCalls(d.Index.NewWriter(enforcing).(*booksWriter))
			}
			for _, m := range ifaceMethods(c.write...) {
				require.Contains(t, calls, m)
				_ = calls[m]()
			}
			require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
			if len(ifaceMethods(c.write...)) > 0 {
				require.NotEmpty(t, enforcing.Writes())
			}
		})
	}
}

// build-candidate-status-index run for real: it writes the dedup:s: rows
// (embeddings) and the done flag (a setting: row), so its declared Effects must
// admit both, and the flag is set.
func TestBooksKinds_StatusIndexRunsThroughTheBuiltDeps(t *testing.T) {
	f := newBooksFix(t)
	seedCandidate(t, f.emb, database.DedupCandidate{EntityAID: "bruce", EntityBID: "thomas", Layer: "exact"})
	defs, err := BooksTaskLists(f.in)
	require.NoError(t, err)
	def := byKind(t, defs, buildCandidateStatusIndexKind)
	w := writertest.New(def, tasks.ModeLive)
	_, err = runKind(t, def, tasks.ModeLive, w, "")
	require.NoError(t, err)
	require.NoError(t, w.RequireClean())
	require.True(t, w.Stamped()["books/settings"], "the flag write was not made through the gate: %v", w.Stamped())
	require.True(t, f.emb.IsCandidateStatusIndexBuilt())
}

func TestBooksReads_PassThroughAndWiring(t *testing.T) {
	f := newBooksFix(t)
	d := f.deps(t)

	built, avail := d.Scoring.Store.LSHIndexBuilt()
	require.False(t, built)
	require.True(t, avail)
	require.NoError(t, f.store.SetLSHIndexBuilt())
	built, _ = d.Scoring.Store.LSHIndexBuilt()
	require.True(t, built)

	// the dry sweep reads, never writes
	res, err := d.Index.Store.SweepBookFileSegDropDry(context.Background(), 10, nil)
	require.NoError(t, err)
	require.Zero(t, res.Rewrite)

	require.Equal(t, "bge-m3", d.Embedding.EmbeddingModel())
	_, err = d.Chain.RescorePreview(context.Background())
	require.NoError(t, err)
	require.Equal(t, []bool{false}, f.eng.rescores, "the preview is Rescore(apply=false)")

	// the engine factories hand the engine the run's writer, whatever the group
	w := &sentinelWriter{}
	require.NotNil(t, d.Scoring.NewEngine(w))
	require.NotNil(t, d.Embedding.NewEngine(w))
	require.NotNil(t, d.Labels.NewEngine(w))
	require.Len(t, f.eng.writers, 3)
	for _, got := range f.eng.writers {
		require.Same(t, w, got)
	}
	require.Equal(t, d.Labels.Baseline.ScoreConfig(), f.eng.scoreCfg)
}
