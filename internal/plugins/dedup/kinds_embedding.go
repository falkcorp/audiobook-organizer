// file: internal/plugins/dedup/kinds_embedding.go
// version: 1.1.1
// guid: 6209a7e9-41c2-4ab7-bf95-030d41c7eef1
// last-edited: 2026-11-18

package dedup

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The embedding group (F5-04): the definitions that embed books and authors,
// re-embed or re-encode the corpus, and clean orphaned embedding rows.

// embeddingKindStore is the reads the embedding group's cores take, composed by
// embedding each definition's own narrow interface (the F5-11 precedent): a core
// takes only its own piece, the composition is what EmbeddingKindDeps carries.
type embeddingKindStore interface {
	embeddingCleanupStore
	embeddingScanStore
}

// embeddingCleanupStore and embeddingScanStore split embeddingKindStore so each
// piece stays under the repo's interface width limit; the method set of the
// composition is unchanged.
type embeddingCleanupStore interface {
	cleanupOrphanEmbeddingsStore
	cleanupOrphanAuthorEmbeddingsStore
	embReencodeStore
	reembedEmbeddingsStore
}

type embeddingScanStore interface {
	embedScanStore
	calibrateEmbeddingThresholdsStore
	calibrateBookReader
	breakdownBackfillReads
	datasetBackfillReads
}

// embeddingKindWriter is the writes the embedding group's cores make, composed
// the same way.
type embeddingKindWriter interface {
	cleanupOrphanEmbeddingsWriter
	cleanupOrphanAuthorEmbeddingsWriter
	reembedEmbeddingsWriter
	breakdownBackfillWriter
	datasetBackfillWriter
	embReencodeWriter
}

// embeddingKindEngine is the engine the embedding group's cores take: the embed
// calls (embed-scan, embed-async, reembed-embeddings) and the pair scorer
// (breakdown-backfill).
type embeddingKindEngine interface {
	embedScanEngine
	reembedEmbeddingsEngine
	pairScorer
}

// EmbeddingKindDeps is what the embedding group's native kinds need from their
// owner. F5-18 wires them: the stores behind Store, the gated writer behind
// NewWriter, the dedup engine over the run's gated writer behind NewEngine;
// tests pass fakes.
type EmbeddingKindDeps struct {
	Store     embeddingKindStore
	NewWriter func(tasks.Writer) embeddingKindWriter
	// NewEngine builds the dedup engine over the run's gated writer.
	NewEngine func(tasks.Writer) embeddingKindEngine
	// EmbeddingModel is the wired embedding client's model name (HEAD:
	// Engine.EmbeddingModel). It is a pure read, so the kinds that only need the
	// name (calibrate-embedding-thresholds, which is ReadOnly and so is handed no
	// writer, and the reembed-embeddings preview) take it here and not through
	// NewEngine.
	EmbeddingModel func() string
}

// embeddingWriterFor builds the run's writer over its kernel-gated tasks.Writer,
// refusing a run whose owner declared no factory (or one that returned nothing).
func embeddingWriterFor(kind tasks.Kind, d EmbeddingKindDeps, rc *tasks.RC) (embeddingKindWriter, error) {
	if err := errNeed(kind, "Embedding.NewWriter", d.NewWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// embeddingEngineFor builds the run's engine over its gated writer, refusing a
// run whose owner declared no factory (or one that returned nothing).
func embeddingEngineFor(kind tasks.Kind, d EmbeddingKindDeps, rc *tasks.RC) (embeddingKindEngine, error) {
	if err := errNeed(kind, "Embedding.NewEngine", d.NewEngine != nil); err != nil {
		return nil, err
	}
	e := d.NewEngine(rc.Writer())
	if e == nil {
		return nil, errNeedEngine(kind)
	}
	return e, nil
}

// headEmbeddingReads is HEAD's reads for the embedding cores: the embedding
// store and the main store the HEAD bodies read through p.embeddingStore and
// p.store.
type headEmbeddingReads struct {
	emb   *database.EmbeddingStore
	books pluginBookReader
}

func (h headEmbeddingReads) ListByType(entityType string) ([]database.Embedding, error) {
	return h.emb.ListByType(entityType)
}

func (h headEmbeddingReads) Get(entityType, entityID string) (*database.Embedding, error) {
	return h.emb.Get(entityType, entityID)
}

func (h headEmbeddingReads) ListLabeledExamples(f database.LabeledExampleFilter) ([]database.LabeledExample, error) {
	return h.emb.ListLabeledExamples(f)
}

func (h headEmbeddingReads) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	return h.emb.ListCandidates(f)
}

func (h headEmbeddingReads) GetBookByID(id string) (*database.Book, error) {
	return h.books.GetBookByID(id)
}

func (h headEmbeddingReads) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return h.books.GetBookFiles(bookID)
}

func (h headEmbeddingReads) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return h.books.GetAllBooksCore(limit, offset)
}

func (h headEmbeddingReads) GetAllAuthors() ([]database.Author, error) {
	return h.books.GetAllAuthors()
}

// headEmbeddingWriter is HEAD's writes for the embedding cores: each method
// calls exactly what the HEAD body called (rule R10 item 5).
type headEmbeddingWriter struct{ emb *database.EmbeddingStore }

func (h headEmbeddingWriter) Delete(entityType, entityID string) error {
	return h.emb.Delete(entityType, entityID)
}

func (h headEmbeddingWriter) UpsertLabeledExample(ex database.LabeledExample) error {
	return h.emb.UpsertLabeledExample(ex)
}

func (h headEmbeddingWriter) ReclassifyCandidate(id int64, fromStatus, toStatus string) error {
	return h.emb.ReclassifyCandidate(id, fromStatus, toStatus)
}

func (h headEmbeddingWriter) UpdateCandidateScores(updates []database.CandidateScoreUpdate) (int, []int64, error) {
	return h.emb.UpdateCandidateScores(updates)
}

func (h headEmbeddingWriter) SyncCandidateWrites() error { return h.emb.SyncCandidateWrites() }

// embeddingKinds lists the embedding group's native kinds in HEAD file order.
func embeddingKinds(p *Plugin, d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.embedScanNative(d, sched),
		p.embedAsyncNative(d, sched),
		p.embReencodeNative(d, sched),
		p.reembedEmbeddingsNative(d, sched),
		p.cleanupOrphanEmbeddingsNative(d, sched),
		p.cleanupOrphanAuthorEmbeddingsNative(d, sched),
		p.calibrateEmbeddingThresholdsNative(d, sched),
		p.breakdownBackfillNative(d, sched),
		p.datasetBackfillNative(d, sched),
	}
}

// cleanupOrphanEmbeddingsNative is the native dedup.cleanup-orphan-embeddings
// kind. Preview is HEAD's dry run (apply=false: report only); Live deletes the
// rows the scan proved orphaned.
func (p *Plugin) cleanupOrphanEmbeddingsNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(cleanupOrphanEmbeddingsKind, sched, false)
	c.Title = "Clean up orphaned book embeddings"
	c.Help = cleanupOrphanEmbeddingsHelp
	// Writes found: the delete pass calls EmbeddingStore.Delete, which removes
	// emb:v:book:<id> rows (embeddings). The scan reads books and writes nothing.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.cleanup-orphan-embeddings")
	c.Timeout = 30 * time.Minute
	return tasks.Task(cleanupOrphanEmbeddingsKind, tasks.TaskSpec[json.RawMessage, cleanupOrphanEmbeddingsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (cleanupOrphanEmbeddingsResult, error) {
			if err := refuseKindModeKeys(cleanupOrphanEmbeddingsKind, raw); err != nil {
				return cleanupOrphanEmbeddingsResult{}, err
			}
			if err := errNeed(cleanupOrphanEmbeddingsKind, "Embedding.Store", d.Store != nil); err != nil {
				return cleanupOrphanEmbeddingsResult{}, err
			}
			env := cleanupOrphanEmbeddingsEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := embeddingWriterFor(cleanupOrphanEmbeddingsKind, d, rc)
				if err != nil {
					return cleanupOrphanEmbeddingsResult{}, err
				}
				env.Writer = w
			}
			return cleanupOrphanEmbeddingsCore(rc.Context(), env)
		},
	})
}

// embedScanNative is the native dedup.embed-scan kind. HEAD runs it live only (no
// Preview), so a native Preview run is refused. Resume: HEAD's Requeue is
// FromZero; the body reads no checkpoint and a re-run re-embeds only books
// without a fresh embedding.
func (p *Plugin) embedScanNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(embedScanKind, sched, false)
	c.Title = "Embed all books"
	c.Help = embedScanHelp
	// Writes found: Engine.EmbedBook -> EmbedBooks -> embedStore.Upsert/Delete
	// write emb:v:book rows (embeddings). Reads: prepBookEmbed's GetBookByID,
	// GetAuthorByID and GetSeriesByID (books, authors, series); the async
	// submission only reads, but a non-OpenAI backend downgrades it to this path.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResAuthors, ops.ResSeries)...)
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.embed-scan")
	c.Timeout = 120 * time.Minute
	return tasks.Task(embedScanKind, tasks.TaskSpec[json.RawMessage, embedScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (embedScanResult, error) {
			params, err := decodeNativeParams[EmbedScanParams](embedScanKind, raw)
			if err != nil {
				return embedScanResult{}, err
			}
			env, err := embedNativeEnv(embedScanKind, d, rc, params.Async)
			if err != nil {
				return embedScanResult{}, err
			}
			return embedScanCore(rc.Context(), env)
		},
	})
}

// embedAsyncNative is the native dedup.embed-async kind (deprecated at HEAD:
// embed-scan with async=true). Live only, like embed-scan.
func (p *Plugin) embedAsyncNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(embedAsyncKind, sched, false)
	c.Title = "Embed books async (batch API) [deprecated — use embed-scan with async:true]"
	c.Help = embedAsyncHelp
	// Writes found: as embed-scan. The batch submission itself writes no local
	// row (the poller ingests results later), but under a non-OpenAI backend the
	// core downgrades to the synchronous EmbedBook path.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResAuthors, ops.ResSeries)...)
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.embed-async")
	c.Timeout = 10 * time.Minute
	return tasks.Task(embedAsyncKind, tasks.TaskSpec[json.RawMessage, embedScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (embedScanResult, error) {
			if err := refuseKindModeKeys(embedAsyncKind, raw); err != nil {
				return embedScanResult{}, err
			}
			env, err := embedNativeEnv(embedAsyncKind, d, rc, true)
			if err != nil {
				return embedScanResult{}, err
			}
			return embedAsyncCore(rc.Context(), env)
		},
	})
}

// embedNativeEnv is the env both embed shells share, after refusing a Preview
// run and any unwired dependency.
func embedNativeEnv(kind tasks.Kind, d EmbeddingKindDeps, rc *tasks.RC, async bool) (embedScanEnv, error) {
	if err := kindLiveOnly(rc, kind); err != nil {
		return embedScanEnv{}, err
	}
	if err := errNeed(kind, "Embedding.Store", d.Store != nil); err != nil {
		return embedScanEnv{}, err
	}
	eng, err := embeddingEngineFor(kind, d, rc)
	if err != nil {
		return embedScanEnv{}, err
	}
	return embedScanEnv{kindEnv: nativeEnv(rc), Async: async, Store: d.Store, Engine: eng}, nil
}

// embReencodeNative is the native dedup.emb-reencode kind. Preview is HEAD's dry
// run (apply=false: report only); Live rewrites the v0 rows.
func (p *Plugin) embReencodeNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(embReencodeKind, sched, false)
	c.Title = "Re-encode embeddings to float16+zstd (T021)"
	c.Help = embReencodeHelp
	// Writes found: ReencodeRows batch-sets the emb:v: rows (embeddings) and the
	// done flag is a SetSetting (settings).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings, ops.ResSettings)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.emb-reencode")
	c.Timeout = 30 * time.Minute
	return tasks.Task(embReencodeKind, tasks.TaskSpec[json.RawMessage, embReencodeResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (embReencodeResult, error) {
			if err := refuseKindModeKeys(embReencodeKind, raw); err != nil {
				return embReencodeResult{}, err
			}
			if err := errNeed(embReencodeKind, "Embedding.Store", d.Store != nil); err != nil {
				return embReencodeResult{}, err
			}
			env := embReencodeEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := embeddingWriterFor(embReencodeKind, d, rc)
				if err != nil {
					return embReencodeResult{}, err
				}
				env.Writer = w
			}
			return embReencodeCore(rc.Context(), env)
		},
	})
}

// reembedEmbeddingsParamsNative is reembedEmbeddingsParams without HEAD's apply
// field: Live comes from the run's mode.
type reembedEmbeddingsParamsNative struct {
	BatchSize int `json:"batch_size"`
}

// reembedEmbeddingsNative is the native dedup.reembed-embeddings kind. Preview is
// HEAD's dry run. Resume: HEAD's Requeue is FromZero; the body reads no
// checkpoint, a re-run skips books already at the target model.
func (p *Plugin) reembedEmbeddingsNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(reembedEmbeddingsKind, sched, false)
	c.Title = "Re-embed corpus (embedding model change)"
	c.Help = reembedEmbeddingsHelp
	// Writes found: EmbeddingStore.Delete drops stale emb:v:book rows and
	// Engine.EmbedBooks upserts the fresh ones (embeddings). Reads: the book list
	// and prepBookEmbed's GetBookByID/GetAuthorByID/GetSeriesByID.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResAuthors, ops.ResSeries)...)
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.reembed-embeddings")
	c.Timeout = 12 * time.Hour
	return tasks.Task(reembedEmbeddingsKind, tasks.TaskSpec[json.RawMessage, reembedEmbeddingsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (reembedEmbeddingsResult, error) {
			params, err := decodeNativeParams[reembedEmbeddingsParamsNative](reembedEmbeddingsKind, raw)
			if err != nil {
				return reembedEmbeddingsResult{}, err
			}
			if err := errNeed(reembedEmbeddingsKind, "Embedding.Store", d.Store != nil); err != nil {
				return reembedEmbeddingsResult{}, err
			}
			if err := errNeed(reembedEmbeddingsKind, "Embedding.EmbeddingModel", d.EmbeddingModel != nil); err != nil {
				return reembedEmbeddingsResult{}, err
			}
			env := reembedEmbeddingsEnv{
				kindEnv:       nativeEnv(rc),
				Params:        reembedEmbeddingsParams{BatchSize: params.BatchSize},
				Store:         d.Store,
				Model:         d.EmbeddingModel,
				ToolAvailable: p.toolAvailableFunc(),
			}
			if env.Live {
				w, err := embeddingWriterFor(reembedEmbeddingsKind, d, rc)
				if err != nil {
					return reembedEmbeddingsResult{}, err
				}
				eng, err := embeddingEngineFor(reembedEmbeddingsKind, d, rc)
				if err != nil {
					return reembedEmbeddingsResult{}, err
				}
				env.Writer, env.Engine = w, eng
			}
			return reembedEmbeddingsCore(rc.Context(), env)
		},
	})
}

// cleanupOrphanAuthorEmbeddingsNative is the native
// dedup.cleanup-orphan-author-embeddings kind. Preview is HEAD's dry run; Live
// deletes the rows the scan proved orphaned.
func (p *Plugin) cleanupOrphanAuthorEmbeddingsNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(cleanupOrphanAuthorEmbeddingsKind, sched, false)
	c.Title = "Clean up orphaned author embeddings"
	c.Help = cleanupOrphanAuthorEmbeddingsHelp
	// Writes found: the delete pass calls EmbeddingStore.Delete, which removes
	// emb:v:author:<id> rows (embeddings). The scan reads authors.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResAuthors)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.cleanup-orphan-author-embeddings")
	c.Timeout = 10 * time.Minute
	return tasks.Task(cleanupOrphanAuthorEmbeddingsKind, tasks.TaskSpec[json.RawMessage, cleanupOrphanAuthorEmbeddingsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (cleanupOrphanAuthorEmbeddingsResult, error) {
			if err := refuseKindModeKeys(cleanupOrphanAuthorEmbeddingsKind, raw); err != nil {
				return cleanupOrphanAuthorEmbeddingsResult{}, err
			}
			if err := errNeed(cleanupOrphanAuthorEmbeddingsKind, "Embedding.Store", d.Store != nil); err != nil {
				return cleanupOrphanAuthorEmbeddingsResult{}, err
			}
			env := cleanupOrphanAuthorEmbeddingsEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := embeddingWriterFor(cleanupOrphanAuthorEmbeddingsKind, d, rc)
				if err != nil {
					return cleanupOrphanAuthorEmbeddingsResult{}, err
				}
				env.Writer = w
			}
			return cleanupOrphanAuthorEmbeddingsCore(rc.Context(), env)
		},
	})
}

// calibrateEmbeddingThresholdsParamsNative is the params the native run decodes
// (HEAD's has no mode field, so it is the same shape).
type calibrateEmbeddingThresholdsParamsNative = calibrateEmbeddingThresholdsParams

// calibrateEmbeddingThresholdsNative is the native
// dedup.calibrate-embedding-thresholds kind. HEAD has no preview for it (the body
// is a read-only report, so it has no dry-run switch), so a native Preview run is
// refused like any live-only kind; a ReadOnly run is handed no writer.
func (p *Plugin) calibrateEmbeddingThresholdsNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(calibrateEmbeddingThresholdsKind, sched, false)
	c.Title = "Calibrate embedding thresholds (report only)"
	c.Help = calibrateEmbeddingThresholdsHelp
	// Read-only: ListLabeledExamples and EmbeddingStore.Get read the labeled
	// dataset and emb:v:book rows (embeddings); the diagnostic sample reads books.
	c.Effects = tasks.ReadOnly(res(ops.ResEmbeddings, ops.ResBooks)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.calibrate-embedding-thresholds")
	c.Timeout = 30 * time.Minute
	return tasks.Task(calibrateEmbeddingThresholdsKind, tasks.TaskSpec[json.RawMessage, calibrateEmbeddingThresholdsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (calibrateEmbeddingThresholdsResult, error) {
			params, err := decodeNativeParams[calibrateEmbeddingThresholdsParamsNative](calibrateEmbeddingThresholdsKind, raw)
			if err != nil {
				return calibrateEmbeddingThresholdsResult{}, err
			}
			if err := kindLiveOnly(rc, calibrateEmbeddingThresholdsKind); err != nil {
				return calibrateEmbeddingThresholdsResult{}, err
			}
			if err := errNeed(calibrateEmbeddingThresholdsKind, "Embedding.Store", d.Store != nil); err != nil {
				return calibrateEmbeddingThresholdsResult{}, err
			}
			return calibrateEmbeddingThresholdsCore(rc.Context(), calibrateEmbeddingThresholdsEnv{
				kindEnv:      nativeEnv(rc),
				Params:       params,
				Store:        d.Store,
				Books:        d.Store,
				DefaultModel: d.EmbeddingModel,
			})
		},
	})
}

// breakdownBackfillNative is the native dedup.breakdown-backfill kind. Preview is
// HEAD's dry run: the scoring pass runs and nothing is written, so no writer is
// built.
func (p *Plugin) breakdownBackfillNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(breakdownBackfillKind, sched, false)
	c.Title = "Backfill ScoreBreakdowns onto pre-T015 pending candidates"
	c.Help = breakdownBackfillHelp
	// Writes found: UpdateCandidateScores rewrites the candidate rows' score
	// fields; candidate rows live in the embedding store's keyspace (embeddings),
	// as purge-stale's do. Reads: ListCandidates (embeddings) and
	// Engine.ScorePairsForBook's GetBookByID, GetAuthorByID and GetBookFiles
	// (books, authors, book_files).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResAuthors, ops.ResBookFiles)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.breakdown-backfill")
	c.Timeout = 60 * time.Minute
	return tasks.Task(breakdownBackfillKind, tasks.TaskSpec[json.RawMessage, breakdownBackfillReport]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (breakdownBackfillReport, error) {
			if err := refuseKindModeKeys(breakdownBackfillKind, raw); err != nil {
				return breakdownBackfillReport{}, err
			}
			if err := errNeed(breakdownBackfillKind, "Embedding.Store", d.Store != nil); err != nil {
				return breakdownBackfillReport{}, err
			}
			eng, err := embeddingEngineFor(breakdownBackfillKind, d, rc)
			if err != nil {
				return breakdownBackfillReport{}, err
			}
			env := breakdownBackfillEnv{kindEnv: nativeEnv(rc), Scorer: eng, Store: d.Store}
			if env.Live {
				w, err := embeddingWriterFor(breakdownBackfillKind, d, rc)
				if err != nil {
					return breakdownBackfillReport{}, err
				}
				env.Writer = w
			}
			return breakdownBackfillCore(rc.Context(), env)
		},
	})
}

// datasetBackfillNative is the native dedup.dataset-backfill kind. Preview is
// HEAD's dry run.
func (p *Plugin) datasetBackfillNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(datasetBackfillKind, sched, false)
	c.Title = "Backfill dedup tuning dataset"
	c.Help = datasetBackfillHelp
	// Writes found: UpsertLabeledExample writes dedup:label rows and
	// ReclassifyCandidate flips candidate status, both in the embedding store's
	// keyspace (embeddings). Reads: the example builder's GetBook and
	// GetBookFiles (books, book_files).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks, ops.ResBookFiles)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.dataset-backfill")
	c.Timeout = 60 * time.Minute
	return tasks.Task(datasetBackfillKind, tasks.TaskSpec[json.RawMessage, datasetBackfillResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (datasetBackfillResult, error) {
			if err := refuseKindModeKeys(datasetBackfillKind, raw); err != nil {
				return datasetBackfillResult{}, err
			}
			if err := errNeed(datasetBackfillKind, "Embedding.Store", d.Store != nil); err != nil {
				return datasetBackfillResult{}, err
			}
			env := datasetBackfillEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := embeddingWriterFor(datasetBackfillKind, d, rc)
				if err != nil {
					return datasetBackfillResult{}, err
				}
				env.Writer = w
			}
			return datasetBackfillCore(rc.Context(), env)
		},
	})
}
