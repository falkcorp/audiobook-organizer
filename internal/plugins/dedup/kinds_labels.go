// file: internal/plugins/dedup/kinds_labels.go
// version: 1.1.0
// guid: 8f265d0b-a0d3-4ccc-8090-632ef8c21b5d
// last-edited: 2026-11-18

package dedup

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The labels group (F5-04): the definitions that build, rescore and calibrate
// the dedup tuning dataset, plus the LLM review pass. (dedup.label-refinement,
// which starts rebuild-gold-labels and calibrate-composite as children, is the
// coordinator's.)

// labelBookReader is the book reads the dataset builders take (their
// dataset.BuilderStore, with the store's own method names).
type labelBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(id string) ([]database.BookFile, error)
}

// labelBookAdapter bridges a labelBookReader onto dataset.BuilderStore: the
// store spells GetBook as GetBookByID.
type labelBookAdapter struct{ r labelBookReader }

func (a labelBookAdapter) GetBook(id string) (*database.Book, error) { return a.r.GetBookByID(id) }
func (a labelBookAdapter) GetBookFiles(id string) ([]database.BookFile, error) {
	return a.r.GetBookFiles(id)
}

// labelBookMemo is labelBookAdapter with a mutex-guarded book-lookup cache
// (CONC-8): the same book appears in many candidate rows and is shared across
// the item pool, so the cache needs its lock. Both the book and any error are
// cached. Only GetBook is memoized; GetBookFiles passes through. (It repeats
// memoizedBuilderAdapter in dataset_backfill.go, which is outside this group's
// files; the two can be unified when that file is converted.)
type labelBookMemo struct {
	inner labelBookAdapter
	mu    sync.Mutex
	cache map[string]labelBookLookup
}

type labelBookLookup struct {
	book *database.Book
	err  error
}

func newLabelBookMemo(r labelBookReader) *labelBookMemo {
	return &labelBookMemo{inner: labelBookAdapter{r: r}, cache: make(map[string]labelBookLookup)}
}

func (m *labelBookMemo) GetBook(id string) (*database.Book, error) {
	m.mu.Lock()
	if r, ok := m.cache[id]; ok {
		m.mu.Unlock()
		return r.book, r.err
	}
	m.mu.Unlock()

	book, err := m.inner.GetBook(id)

	m.mu.Lock()
	m.cache[id] = labelBookLookup{book: book, err: err}
	m.mu.Unlock()
	return book, err
}

func (m *labelBookMemo) GetBookFiles(id string) ([]database.BookFile, error) {
	return m.inner.GetBookFiles(id)
}

// labelKindStore is the reads the labels group's cores take, composed by
// embedding each definition's own narrow interface (the F5-11 precedent).
type labelKindStore interface {
	mineGoldLabelsStore
	rebuildGoldLabelsStore
	rescoreLabeledExamplesStore
	calibrateCompositeStore
}

// labelKindWriter is the writes the labels group's cores make, composed the same way.
type labelKindWriter interface {
	mineGoldLabelsWriter
	rebuildGoldLabelsWriter
	rescoreLabeledExamplesWriter
	calibrateCompositeWriter
}

// LabelKindDeps is what the labels group's native kinds need from their owner.
// F5-18 wires them: the stores and the read-only scorer and baseline config
// directly, the gated writer and the review engine through factories over the
// run's tasks.Writer; tests pass fakes.
type LabelKindDeps struct {
	Store labelKindStore
	// Scorer is the engine's read-only pair scorer (rescore-labeled-examples).
	Scorer pairScorer
	// Baseline is the live engine's score config, read-only (calibrate-composite).
	Baseline calibrateCompositeBaseline
	// NewWriter builds the run's writer over its gated tasks.Writer.
	NewWriter func(tasks.Writer) labelKindWriter
	// NewEngine builds the run's review engine over its gated tasks.Writer
	// (llm-review: the batch submit writes through it).
	NewEngine func(tasks.Writer) llmReviewEngine
}

// labelWriterFor builds the run's writer, refusing a run whose owner declared no
// factory (or one that returned nothing).
func labelWriterFor(kind tasks.Kind, d LabelKindDeps, rc *tasks.RC) (labelKindWriter, error) {
	if err := errNeed(kind, "Labels.NewWriter", d.NewWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// labelKinds lists the labels group's native kinds in the coordinator's order.
func labelKinds(p *Plugin, d LabelKindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.mineGoldLabelsNative(d, sched),
		p.rebuildGoldLabelsNative(d, sched),
		p.rescoreLabeledExamplesNative(d, sched),
		p.calibrateCompositeNative(d, sched),
		p.llmReviewNative(d, sched),
	}
}

// mineGoldLabelsNative is the native dedup.mine-gold-labels kind. Preview is
// HEAD's dry run (apply=false); Live upserts the mined true_dup labels.
func (p *Plugin) mineGoldLabelsNative(d LabelKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(mineGoldLabelsKind, sched, false)
	c.Title = "Mine high-confidence dedup labels"
	c.Help = mineGoldLabelsHelp
	// Writes found: EmbeddingStore.UpsertLabeledExample writes dedup:label: rows,
	// in the embedding store's keyspace (embeddings). Reads: the candidates'
	// books and files through the memo adapter (GetBookByID, GetBookFiles).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResBookFiles)...)
	// HEAD Drop: no checkpoint; the upserts are idempotent, a re-run mines again.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.mine-gold-labels")
	c.Timeout = 60 * time.Minute
	return tasks.Task(mineGoldLabelsKind, tasks.TaskSpec[json.RawMessage, mineGoldLabelsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (mineGoldLabelsResult, error) {
			if err := refuseKindModeKeys(mineGoldLabelsKind, raw); err != nil {
				return mineGoldLabelsResult{}, err
			}
			if err := errNeed(mineGoldLabelsKind, "Labels.Store", d.Store != nil); err != nil {
				return mineGoldLabelsResult{}, err
			}
			env := mineGoldLabelsEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := labelWriterFor(mineGoldLabelsKind, d, rc)
				if err != nil {
					return mineGoldLabelsResult{}, err
				}
				env.Writer = w
			}
			return mineGoldLabelsCore(rc.Context(), env)
		},
	})
}

// rebuildGoldLabelsNative is the native dedup.rebuild-gold-labels kind. Preview
// is HEAD's dry run (the diff report); Live wipes the rule/auto_high_conf rows
// and reinserts the fresh set. label-refinement starts it as a child.
func (p *Plugin) rebuildGoldLabelsNative(d LabelKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(rebuildGoldLabelsKind, sched, false)
	c.Title = "Rebuild mechanically-derived gold labels"
	c.Help = rebuildGoldLabelsHelp
	// Writes found: EmbeddingStore.DeleteLabeledExamplesBySource and
	// UpsertLabeledExample (dedup:label: rows, embeddings). Reads: candidates
	// (embeddings) and the books and files the adapter reads.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResBookFiles)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.rebuild-gold-labels")
	c.Timeout = 60 * time.Minute
	return tasks.Task(rebuildGoldLabelsKind, tasks.TaskSpec[json.RawMessage, rebuildGoldLabelsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (rebuildGoldLabelsResult, error) {
			if err := refuseKindModeKeys(rebuildGoldLabelsKind, raw); err != nil {
				return rebuildGoldLabelsResult{}, err
			}
			if err := errNeed(rebuildGoldLabelsKind, "Labels.Store", d.Store != nil); err != nil {
				return rebuildGoldLabelsResult{}, err
			}
			env := rebuildGoldLabelsEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := labelWriterFor(rebuildGoldLabelsKind, d, rc)
				if err != nil {
					return rebuildGoldLabelsResult{}, err
				}
				env.Writer = w
			}
			return rebuildGoldLabelsCore(rc.Context(), env)
		},
	})
}

// rescoreLabeledExamplesNative is the native dedup.rescore-labeled-examples
// kind. Preview is HEAD's dry run; Live narrow-writes each recomputed breakdown.
func (p *Plugin) rescoreLabeledExamplesNative(d LabelKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(rescoreLabeledExamplesKind, sched, false)
	c.Title = "Rescore labeled examples (populate ScoreBreakdowns for calibration)"
	c.Help = rescoreLabeledExamplesHelp
	// Writes found: EmbeddingStore.UpsertLabeledExample (dedup:label: rows,
	// embeddings). Reads: Engine.ScorePairsForBook reads the pair's books, files
	// and authors (GetBookByID, GetBookFiles, GetAuthorByID) and the ISBN index.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResBookFiles, ops.ResAuthors, ops.ResIndexes)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.rescore-labeled-examples")
	c.Timeout = 60 * time.Minute
	return tasks.Task(rescoreLabeledExamplesKind, tasks.TaskSpec[json.RawMessage, rescoreLabeledExamplesResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (rescoreLabeledExamplesResult, error) {
			if err := refuseKindModeKeys(rescoreLabeledExamplesKind, raw); err != nil {
				return rescoreLabeledExamplesResult{}, err
			}
			if err := errNeed(rescoreLabeledExamplesKind, "Labels.Store", d.Store != nil); err != nil {
				return rescoreLabeledExamplesResult{}, err
			}
			if err := errNeed(rescoreLabeledExamplesKind, "Labels.Scorer", d.Scorer != nil); err != nil {
				return rescoreLabeledExamplesResult{}, err
			}
			env := rescoreLabeledExamplesEnv{kindEnv: nativeEnv(rc), Scorer: d.Scorer, Store: d.Store}
			if env.Live {
				w, err := labelWriterFor(rescoreLabeledExamplesKind, d, rc)
				if err != nil {
					return rescoreLabeledExamplesResult{}, err
				}
				env.Writer = w
			}
			return rescoreLabeledExamplesCore(rc.Context(), env)
		},
	})
}

// calibrateCompositeNative is the native dedup.calibrate-composite kind. Preview
// is HEAD's dry run (the report); Live applies the recommended bands only when
// every tunable target is met. The native params drop apply; label-refinement
// starts this kind in Preview with no apply key.
func (p *Plugin) calibrateCompositeNative(d LabelKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(calibrateCompositeKind, sched, false)
	c.Title = "Calibrate composite scorer (report; apply gated)"
	c.Help = calibrateCompositeHelp
	// Writes found (apply only): PersistBandThresholds writes the
	// dedup.signals.* blob (settings); Engine.ReloadScoreConfig re-bands every
	// stored candidate (embeddings). Reads: the labeled examples and candidates
	// are in the same embeddings keyspace.
	c.Effects = tasks.Writes(res(ops.ResSettings, ops.ResEmbeddings)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.calibrate-composite")
	c.Timeout = 30 * time.Minute
	return tasks.Task(calibrateCompositeKind, tasks.TaskSpec[json.RawMessage, calibrateCompositeResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (calibrateCompositeResult, error) {
			params, err := decodeNativeParams[calibrateCompositeNativeParams](calibrateCompositeKind, raw)
			if err != nil {
				return calibrateCompositeResult{}, err
			}
			if err := errNeed(calibrateCompositeKind, "Labels.Store", d.Store != nil); err != nil {
				return calibrateCompositeResult{}, err
			}
			if err := errNeed(calibrateCompositeKind, "Labels.Baseline", d.Baseline != nil); err != nil {
				return calibrateCompositeResult{}, err
			}
			env := calibrateCompositeEnv{kindEnv: nativeEnv(rc), Store: d.Store, Baseline: d.Baseline}
			if env.Live {
				w, err := labelWriterFor(calibrateCompositeKind, d, rc)
				if err != nil {
					return calibrateCompositeResult{}, err
				}
				env.Writer = w
			}
			return calibrateCompositeCore(rc.Context(), env, params)
		},
	})
}

// llmReviewNative is the native dedup.llm-review kind. HEAD has no dry run, so a
// native Preview run is refused. HEAD's FormerIDs, maintenance.dedup-llm-review
// and scheduler.dedup-llm-review, are second and third registrations of this
// review that resolve to it (04-P14b); the seams file maps them to this kind.
func (p *Plugin) llmReviewNative(d LabelKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(llmReviewKind, sched, false)
	c.Title = "LLM review of candidates"
	c.Help = llmReviewHelp
	// Writes found: Engine.RunLLMReviewWithHooks submits a provider batch and
	// writes its ai_jobs row; no ops.Res names ai_jobs, so this takes the nearest
	// valid name, embeddings, where the reviewed candidates live (the same
	// stand-in the author dedup batch submit uses, ResReviewItems, in
	// maintenance/dedup_ops.go). A review decision for the owner. Reads: the
	// pair's books and authors (buildPairInput).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResAuthors)...)
	// HEAD Drop: the pass keeps no checkpoint; a re-run lists the candidates again.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.llm-review")
	c.Timeout = 120 * time.Minute
	return tasks.Task(llmReviewKind, tasks.TaskSpec[json.RawMessage, llmReviewResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (llmReviewResult, error) {
			if err := refuseKindModeKeys(llmReviewKind, raw); err != nil {
				return llmReviewResult{}, err
			}
			if err := kindLiveOnly(rc, llmReviewKind); err != nil {
				return llmReviewResult{}, err
			}
			if err := errNeed(llmReviewKind, "Labels.NewEngine", d.NewEngine != nil); err != nil {
				return llmReviewResult{}, err
			}
			eng := d.NewEngine(rc.Writer())
			if eng == nil {
				return llmReviewResult{}, errNeedEngine(llmReviewKind)
			}
			return llmReviewCore(rc.Context(), llmReviewEnv{kindEnv: nativeEnv(rc), Engine: eng})
		},
	})
}
