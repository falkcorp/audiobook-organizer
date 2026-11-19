// file: internal/plugins/dedup/kinds_scoring.go
// version: 1.2.2
// guid: 87767b32-a764-471a-bc6d-1b845dc5a45d
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The scoring group (F5-04): the definitions that drive the dedup engine's
// candidate scorers and reviewers. Each native shell is a tasks.Task over the
// same core HEAD's shell calls.

// scoringEngine is the engine surface the scoring cores take, composed of each
// definition's own narrow interface (rule R2). The engine writes candidates,
// labels and merges through its own stores; the native run's engine is built
// over the run's gated writer by ScoringKindDeps.NewEngine, so those writes are
// the writer's, which is F5-18's wiring (seams.d/F5-04.md).
type scoringEngine interface {
	purgeStaleEngine
	autoResolveEngine
	fullScanEngine
	rescoreEngine
	bookSignatureScanEngine
	drainStaleEngine
}

// scoringKindStore is the reads the scoring cores take, composed the same way.
type scoringKindStore interface {
	fullScanStore
	bookSignatureScanStore
	drainStaleStore
	quarantineChapterArtifactsStore
}

// scoringKindWriter is the writes the scoring cores make, composed the same way.
type scoringKindWriter interface {
	drainStaleWriter
	quarantineChapterArtifactsWriter
}

// ScoringKindDeps is what the scoring group's native kinds need from their
// owner. NewEngine and NewWriter are handed the run's gated writer (nil in a
// Preview run, which writes nothing).
type ScoringKindDeps struct {
	Store     scoringKindStore
	NewEngine func(tasks.Writer) scoringEngine
	NewWriter func(tasks.Writer) scoringKindWriter
	// SplitReads and NewSplitWriter serve the two split-book kinds: the detector
	// and recheck reads over the main store, and the candidate-row and merge
	// writes behind the run's gated writer (F5-18 wires both).
	SplitReads     splitBookKindReads
	NewSplitWriter func(tasks.Writer) splitBookKindWriter
}

// splitBookKindReads is the reads the split-book cores take, composed of each
// definition's own narrow interface (rule R2).
type splitBookKindReads interface {
	splitBookScanReads
	splitBookBulkMergeReads
}

// splitBookKindWriter is the writes the split-book cores make, composed the same
// way.
type splitBookKindWriter interface {
	splitBookScanWriter
	splitBookBulkMergeWriter
}

// scoringWriterOf is the writer a shell hands the factories: the run's gated
// writer in a live run, nil in a Preview run (rc.Writer() is an intent recorder
// there; the engine's own apply=false is what keeps a dry run from writing).
func scoringWriterOf(rc *tasks.RC, live bool) tasks.Writer {
	if !live {
		return nil
	}
	return rc.Writer()
}

// scoringEngineFor builds the run's engine, refusing a run whose owner declared
// no factory (or one that returned nothing).
func scoringEngineFor(kind tasks.Kind, d ScoringKindDeps, rc *tasks.RC, live bool) (scoringEngine, error) {
	if err := errNeed(kind, "Scoring.NewEngine", d.NewEngine != nil); err != nil {
		return nil, err
	}
	e := d.NewEngine(scoringWriterOf(rc, live))
	if e == nil {
		return nil, errNeedEngine(kind)
	}
	return e, nil
}

// scoringWriterFor builds the run's writer; only a live run calls it.
func scoringWriterFor(kind tasks.Kind, d ScoringKindDeps, rc *tasks.RC) (scoringKindWriter, error) {
	if err := errNeed(kind, "Scoring.NewWriter", d.NewWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// scoringStoreFor refuses a run whose owner left the read store unset.
func scoringStoreFor(kind tasks.Kind, d ScoringKindDeps) (scoringKindStore, error) {
	if err := errNeed(kind, "Scoring.Store", d.Store != nil); err != nil {
		return nil, err
	}
	return d.Store, nil
}

// headScoringReads is HEAD's reads for the scoring cores: the main store and the
// embedding store the HEAD bodies read through p.store and p.embeddingStore
// (either may be nil, as at HEAD).
type headScoringReads struct {
	store pluginStore
	emb   *database.EmbeddingStore
}

// LSHIndexBuilt is HEAD's AsCapability probe: p.store is the server's
// search-indexing decorator, which does not carry IsLSHIndexBuilt itself.
func (h headScoringReads) LSHIndexBuilt() (built, available bool) {
	f, ok := database.AsCapability[LSHFlagStore](h.store)
	if !ok {
		return false, false
	}
	return f.IsLSHIndexBuilt(), true
}

// ListCandidates is EmbeddingStore.ListCandidates; a nil embedding store is an
// error, which the callers read as "no count" (HEAD skipped the read).
func (h headScoringReads) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	if h.emb == nil {
		return nil, 0, errors.New("embedding store not available")
	}
	return h.emb.ListCandidates(f)
}

func (h headScoringReads) GetSetting(key string) (*database.Setting, error) {
	return h.store.GetSetting(key)
}

func (h headScoringReads) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return h.store.GetAllBooksCore(limit, offset)
}

func (h headScoringReads) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return h.store.GetBookFiles(bookID)
}

// headScoringWriter is HEAD's writes for the scoring cores: each method calls
// exactly what the HEAD body called.
type headScoringWriter struct{ store pluginStore }

func (h headScoringWriter) SetSetting(key, value, typ string, secret bool) error {
	return h.store.SetSetting(key, value, typ, secret)
}

func (h headScoringWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return h.store.ModifyBook(id, fn)
}

func (h headScoringWriter) EnsureSinglePrimary(ctx context.Context, groupID string) error {
	_, err := versionprimary.EnsureSinglePrimary(ctx, h.store, groupID,
		versionprimary.Env{RootDir: config.AppConfig.RootDir})
	return err
}

// scoringKinds lists the scoring group's native kinds in HEAD file order. The
// two split-book kinds write split_book_candidate: rows, which ops.ResSplitBookCandidates
// names.
func scoringKinds(p *Plugin, d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.purgeStaleNative(d, sched),
		p.autoResolveNative(d, sched),
		p.fullScanNative(d, sched),
		p.rescoreNative(d, sched),
		p.bookSignatureScanNative(d, sched),
		p.drainStaleNative(d, sched),
		p.quarantineChapterArtifactsNative(d, sched),
		p.splitBookScanNative(d, sched),
		p.splitBookBulkMergeNative(d, sched),
	}
}

// mergeWrites is what a candidate merge (merge.Service.MergeBooks through
// Engine.MergeJournaled, plus EnsureSingletonBookTag and the candidate cleanup)
// writes: internal/maintenance/resources Table row "MergeBooks" (ModifyBook on
// every member: books, authors, metadata_cache, indexes; the losers' external
// ids, listening state and sync ids: external_ids, user_state, sync_identity;
// the undo journal: operations), plus the candidate rows the cleanup rewrites
// (embeddings). No merge path hard-deletes a book (the losers are soft-deleted
// through ModifyBook), so Table row "DeleteBook" does not apply.
var mergeWrites = []ops.Resource{
	ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes,
	ops.ResExternalIDs, ops.ResUserState, ops.ResSyncIdentity, ops.ResOperations,
	ops.ResEmbeddings,
}

// purgeStaleNative is the native dedup.purge-stale kind. HEAD runs it live only
// (no Writes, no Preview), so a native Preview run is refused.
func (p *Plugin) purgeStaleNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(purgeStaleKind, sched, true) // HEAD: NoWarmupWait
	c.Title = "Cleanup stale dedup candidates"
	c.Help = "Deletes pending dedup candidates that are no longer valid (chapter files in same folder, same version-group, distinct series volumes)."
	// Writes found: Engine.PurgeStaleCandidates deletes candidate rows, which live
	// in the embedding store's keyspace (embeddings).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityHigh
	c.ConcurrencyKey = tasks.Key("books.dedup.purge-stale")
	c.Timeout = 10 * time.Minute
	return tasks.Task(purgeStaleKind, tasks.TaskSpec[json.RawMessage, purgeStaleResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (purgeStaleResult, error) {
			if err := refuseKindModeKeys(purgeStaleKind, raw); err != nil {
				return purgeStaleResult{}, err
			}
			if err := kindLiveOnly(rc, purgeStaleKind); err != nil {
				return purgeStaleResult{}, err
			}
			eng, err := scoringEngineFor(purgeStaleKind, d, rc, true)
			if err != nil {
				return purgeStaleResult{}, err
			}
			return purgeStaleCore(rc.Context(), purgeStaleEnv{kindEnv: nativeEnv(rc), Engine: eng})
		},
	})
}

// autoResolveNative is the native dedup.auto-resolve kind. Preview is HEAD's dry
// run (apply=false). Resume Drop: HEAD's ResumeDrop, and the body keeps no
// checkpoint.
func (p *Plugin) autoResolveNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(autoResolveKind, sched, false)
	c.Title = "Auto-resolve CERTAIN dedup candidates (Tier 1)"
	c.Help = "Dry-run by default: reports which Band-CERTAIN dedup candidates WOULD auto-merge (≥2 primary signals or a whole-book-signature true_dup label). apply=true performs the merges, gated by the dedup.auto_resolve_enabled kill switch, and stops after max_merges successful merges (it is not bound to a reviewed dry run)."
	// Writes found: Engine.AutoResolveCertain (apply) -> autoMergeCertain ->
	// MergeJournaled (merge chain + undo journal), UpdateCandidateStatus and
	// CleanupCandidatesAfterMerge (embeddings), EnsureSingletonBookTag (user_state);
	// see mergeWrites. Reads: hasPlausibleAudio -> GetBookFiles (book_files).
	c.Effects = tasks.Writes(res(mergeWrites...)...).AlsoReads(res(ops.ResBookFiles)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityHigh
	c.ConcurrencyKey = tasks.Key(string(autoResolveKind))
	c.Timeout = 30 * time.Minute
	return tasks.Task(autoResolveKind, tasks.TaskSpec[json.RawMessage, autoResolveResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (autoResolveResult, error) {
			in, err := decodeNativeParams[autoResolveNativeParams](autoResolveKind, raw)
			if err != nil {
				return autoResolveResult{}, err
			}
			env := nativeEnv(rc)
			eng, err := scoringEngineFor(autoResolveKind, d, rc, env.Live)
			if err != nil {
				return autoResolveResult{}, err
			}
			return autoResolveCore(rc.Context(), autoResolveEnv{
				kindEnv: env, Engine: eng, MaxMerges: in.MaxMerges, SampleCap: in.SampleCap,
				Enabled: config.AppConfig.Dedup.AutoResolveEnabled,
			})
		},
	})
}

// fullScanNative is the native dedup.full-scan kind. HEAD has no dry run, so a
// native Preview is refused. Resume FromZero: HEAD's ResumeRequeue.
func (p *Plugin) fullScanNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(fullScanKind, sched, false)
	c.Title = "Full dedup scan"
	c.Help = "Runs a full embedding-based dedup scan, purging stale candidates first."
	// Writes found: Engine.PurgeStaleCandidates and FullScan emit and update
	// candidate rows and embed books (embeddings); the Layer-1 file-hash check can
	// call mergeService.MergeBooks when AutoMergeEnabled is set (mergeWrites).
	// Reads: duration/file checks -> GetBookFiles (book_files).
	c.Effects = tasks.Writes(res(mergeWrites...)...).AlsoReads(res(ops.ResBookFiles)...)
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key(string(fullScanKind))
	c.Timeout = 120 * time.Minute
	return tasks.Task(fullScanKind, tasks.TaskSpec[json.RawMessage, fullScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (fullScanResult, error) {
			if err := refuseKindModeKeys(fullScanKind, raw); err != nil {
				return fullScanResult{}, err
			}
			if err := kindLiveOnly(rc, fullScanKind); err != nil {
				return fullScanResult{}, err
			}
			st, err := scoringStoreFor(fullScanKind, d)
			if err != nil {
				return fullScanResult{}, err
			}
			eng, err := scoringEngineFor(fullScanKind, d, rc, true)
			if err != nil {
				return fullScanResult{}, err
			}
			return fullScanCore(rc.Context(), fullScanEnv{kindEnv: nativeEnv(rc), Engine: eng, Store: st})
		},
	})
}

// rescoreNative is the native dedup.rescore kind. Preview is HEAD's apply=false.
// Resume FromZero: HEAD's ResumeRequeue.
func (p *Plugin) rescoreNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(rescoreKind, sched, false)
	c.Title = "Re-band dedup candidates"
	c.Help = "Recomputes every pending dedup candidate's band from its stored signals under the current score ladder. Queued automatically when dedup.signals changes; also runnable directly (equivalent to POST /api/v1/dedup/rescore {\"apply\":true})."
	// Writes found: Engine.Rescore (apply) -> UpdateCandidateScores on the
	// candidate rows (embeddings).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...)
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityHigh
	// Deliberately the scan's key (HEAD): a re-band and a scan never run at once.
	c.ConcurrencyKey = tasks.Key(string(fullScanKind))
	c.Timeout = 60 * time.Minute
	return tasks.Task(rescoreKind, tasks.TaskSpec[json.RawMessage, rescoreResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (rescoreResult, error) {
			in, err := decodeNativeParams[rescoreNativeParams](rescoreKind, raw)
			if err != nil {
				return rescoreResult{}, err
			}
			env := nativeEnv(rc)
			eng, err := scoringEngineFor(rescoreKind, d, rc, env.Live)
			if err != nil {
				return rescoreResult{}, err
			}
			return rescoreCore(rc.Context(), rescoreEnv{kindEnv: env, Engine: eng, Reason: in.Reason})
		},
	})
}

// bookSignatureScanNative is the native dedup.book-signature-scan kind. HEAD has
// no dry run, so a native Preview is refused. Resume FromZero: HEAD's
// ResumeRequeue. HEAD declares no ConcurrencyKey, so none is set.
func (p *Plugin) bookSignatureScanNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(bookSignatureScanKind, sched, false)
	c.Title = "Book signature scan"
	c.Help = "Runs a unified per-book fingerprint scan comparing book signatures."
	// Writes found: Engine.BookSignatureScan -> upsertCandidateWithLiveLabel
	// (candidate rows, embeddings). Reads the books' stored signatures.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks)...)
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.Timeout = 120 * time.Minute
	return tasks.Task(bookSignatureScanKind, tasks.TaskSpec[json.RawMessage, bookSignatureScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (bookSignatureScanResult, error) {
			if err := refuseKindModeKeys(bookSignatureScanKind, raw); err != nil {
				return bookSignatureScanResult{}, err
			}
			if err := kindLiveOnly(rc, bookSignatureScanKind); err != nil {
				return bookSignatureScanResult{}, err
			}
			st, err := scoringStoreFor(bookSignatureScanKind, d)
			if err != nil {
				return bookSignatureScanResult{}, err
			}
			eng, err := scoringEngineFor(bookSignatureScanKind, d, rc, true)
			if err != nil {
				return bookSignatureScanResult{}, err
			}
			return bookSignatureScanCore(rc.Context(), bookSignatureScanEnv{kindEnv: nativeEnv(rc), Engine: eng, Store: st})
		},
	})
}

// drainStaleNative is the native dedup.drain-stale kind. Preview is HEAD's
// apply=false. Resume Drop: HEAD's ResumeDrop (the engine's own apply checkpoint
// is keyed by drainStaleCheckpointID, not the run).
func (p *Plugin) drainStaleNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(drainStaleKind, sched, false)
	c.Title = "Drain stale exact candidates"
	c.Help = "Re-evaluates pending exact-layer dedup candidates against today's emission " +
		"gates (CONS-16 duration-ms, CONS-17 title-leak, and pairs resting only on a placeholder " +
		"title such as \"Unknown Title\" or \"read by narrator\"), and reports counts/samples by " +
		"rejection reason. Dry-run by default; pass apply=true " +
		"to soft-reclassify would-purge rows to stale-drain. Idempotent: a versioned flag prevents " +
		"re-running after apply."
	// Writes found: Engine.DrainStaleCandidates (apply) -> ReclassifyCandidate
	// (embeddings) and operations.SaveCheckpoint -> SaveOperationState (opstate:
	// rows, operations); the done-flag is SetSetting (settings). Reads: books,
	// authors and book files for the gate chain.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings, ops.ResOperations, ops.ResSettings)...).
		AlsoReads(res(ops.ResBooks, ops.ResAuthors, ops.ResBookFiles)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key(string(drainStaleKind))
	c.Timeout = 60 * time.Minute
	return tasks.Task(drainStaleKind, tasks.TaskSpec[json.RawMessage, drainStaleResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (drainStaleResult, error) {
			if err := refuseKindModeKeys(drainStaleKind, raw); err != nil {
				return drainStaleResult{}, err
			}
			env := drainStaleEnv{kindEnv: nativeEnv(rc)}
			eng, err := scoringEngineFor(drainStaleKind, d, rc, env.Live)
			if err != nil {
				return drainStaleResult{}, err
			}
			env.Engine = eng
			if env.Live {
				if env.Store, err = scoringStoreFor(drainStaleKind, d); err != nil {
					return drainStaleResult{}, err
				}
				if env.Writer, err = scoringWriterFor(drainStaleKind, d, rc); err != nil {
					return drainStaleResult{}, err
				}
			}
			return drainStaleCore(rc.Context(), env)
		},
	})
}

// quarantineChapterArtifactsNative is the native dedup.quarantine-chapter-artifacts
// kind. Preview is HEAD's apply=false. Resume Drop: HEAD's ResumeDrop.
func (p *Plugin) quarantineChapterArtifactsNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(quarantineChapterArtifactsKind, sched, false)
	c.Title = "Quarantine chapter-file artifacts"
	c.Help = "Soft-deletes single short books whose generic title collides with many others " +
		"(idents/credits/intros the scanner imported as standalone books). Dry-run by default."
	// Writes found: ModifyBook (soft-delete) carries the whole book row per
	// internal/maintenance/resources Table "ModifyBook": books, authors,
	// metadata_cache, indexes; versionprimary.EnsureSinglePrimary hands the group's
	// primary off through the same ModifyBook. Reads: GetBookFiles (book_files).
	c.Effects = tasks.Writes(res(ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes)...).
		AlsoReads(res(ops.ResBookFiles)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key(string(quarantineChapterArtifactsKind))
	c.Timeout = 60 * time.Minute
	return tasks.Task(quarantineChapterArtifactsKind, tasks.TaskSpec[json.RawMessage, quarantineChapterArtifactsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (quarantineChapterArtifactsResult, error) {
			in, err := decodeNativeParams[quarantineChapterNativeParams](quarantineChapterArtifactsKind, raw)
			if err != nil {
				return quarantineChapterArtifactsResult{}, err
			}
			// An omitted (zero) threshold takes HEAD's defaults (5, 1200, 10) in
			// normalizeQuarantineSettings below, the one place they are applied.
			st, err := scoringStoreFor(quarantineChapterArtifactsKind, d)
			if err != nil {
				return quarantineChapterArtifactsResult{}, err
			}
			env := quarantineChapterArtifactsEnv{
				kindEnv:  nativeEnv(rc),
				Store:    st,
				Settings: normalizeQuarantineSettings(in.MinTitleCollisions, in.MaxDurationSec, in.MinTitleCollisionsUnscanned),
			}
			if env.Live {
				if env.Writer, err = scoringWriterFor(quarantineChapterArtifactsKind, d, rc); err != nil {
					return quarantineChapterArtifactsResult{}, err
				}
			}
			return quarantineChapterArtifactsCore(rc.Context(), env)
		},
	})
}

// splitReadsFor refuses a run whose owner left the split-book reads unset.
func splitReadsFor(kind tasks.Kind, d ScoringKindDeps) (splitBookKindReads, error) {
	if err := errNeed(kind, "Scoring.SplitReads", d.SplitReads != nil); err != nil {
		return nil, err
	}
	return d.SplitReads, nil
}

// splitWriterFor builds the run's split-book writer; only a live run calls it.
func splitWriterFor(kind tasks.Kind, d ScoringKindDeps, rc *tasks.RC) (splitBookKindWriter, error) {
	if err := errNeed(kind, "Scoring.NewSplitWriter", d.NewSplitWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewSplitWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// splitBookScanNative is the native dedup.split-book-scan kind. HEAD runs it live
// only (no Preview), so a native Preview run is refused. Resume FromZero: HEAD's
// Requeue.
func (p *Plugin) splitBookScanNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(splitBookScanKind, sched, false)
	c.Title = "Split-book backfill scan"
	c.Help = splitBookScanHelp
	// Writes found: SplitBookStore.SaveAll replaces the split_book_candidate: rows
	// (split_book_candidates). Reads: the detector loads every book and reads
	// each book's author name (books, authors).
	c.Effects = tasks.Writes(res(ops.ResSplitBookCandidates)...).
		AlsoReads(res(ops.ResBooks, ops.ResAuthors)...)
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.split-book-scan")
	c.Timeout = 60 * time.Minute
	return tasks.Task(splitBookScanKind, tasks.TaskSpec[json.RawMessage, splitBookScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (splitBookScanResult, error) {
			if err := kindLiveOnly(rc, splitBookScanKind); err != nil {
				return splitBookScanResult{}, err
			}
			if err := refuseKindModeKeys(splitBookScanKind, raw); err != nil {
				return splitBookScanResult{}, err
			}
			reads, err := splitReadsFor(splitBookScanKind, d)
			if err != nil {
				return splitBookScanResult{}, err
			}
			writer, err := splitWriterFor(splitBookScanKind, d, rc)
			if err != nil {
				return splitBookScanResult{}, err
			}
			return splitBookScanCore(rc.Context(), splitBookScanEnv{
				kindEnv:    nativeEnv(rc),
				Reads:      reads,
				OpenWriter: func() (splitBookScanWriter, error) { return writer, nil },
			})
		},
	})
}

// splitBookBulkMergeNative is the native dedup.split-book-bulk-merge kind.
// Preview is HEAD's dry_run (recheck only). Resume Ask: HEAD's ResumeAsk.
func (p *Plugin) splitBookBulkMergeNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(splitBookBulkMergeKind, sched, false)
	c.Title = "Merge reviewed split-book candidates"
	c.Help = splitBookBulkMergeHelp
	// Writes found: MergeSplitBookClusterRechecked moves the sources' files onto
	// the keep (book_files), soft-deletes the sources and fills the keep through
	// ModifyBook (books, authors, metadata_cache, indexes: Table "ModifyBook"),
	// reassigns external ids, listening state and sync ids (external_ids,
	// user_state, sync_identity) and journals the undo under the run's id
	// (operations); the merged candidate's row is deleted (split_book_candidates).
	// HEAD declared only books and book_files. The wider set is deliberate (a row
	// errs wide, resources.go). It deviates from rule R7's verbatim mapping of a
	// non-empty HEAD Writes, in the safe direction; the coordinator's sign-off on
	// that deviation is still pending (seams.d/F5-04.md).
	c.Effects = tasks.Writes(res(ops.ResBooks, ops.ResBookFiles, ops.ResAuthors, ops.ResMetadataCache,
		ops.ResIndexes, ops.ResExternalIDs, ops.ResUserState, ops.ResSyncIdentity, ops.ResOperations,
		ops.ResSplitBookCandidates)...)
	c.Resume = tasks.ResumeAsk
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.split-book-merge")
	c.Timeout = 60 * time.Minute
	return tasks.Task(splitBookBulkMergeKind, tasks.TaskSpec[json.RawMessage, splitBookBulkMergeResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (splitBookBulkMergeResult, error) {
			params, err := decodeNativeParams[splitBookBulkMergeParams](splitBookBulkMergeKind, raw)
			if err != nil {
				return splitBookBulkMergeResult{}, err
			}
			if len(params.Items) == 0 {
				return splitBookBulkMergeResult{}, errSplitBookNoItems
			}
			env := splitBookBulkMergeEnv{kindEnv: nativeEnv(rc), Items: params.Items}
			if env.Live {
				if env.Writer, err = splitWriterFor(splitBookBulkMergeKind, d, rc); err != nil {
					return splitBookBulkMergeResult{}, err
				}
			} else if env.Reads, err = splitReadsFor(splitBookBulkMergeKind, d); err != nil {
				return splitBookBulkMergeResult{}, err
			}
			return splitBookBulkMergeCore(rc.Context(), env)
		},
	})
}
