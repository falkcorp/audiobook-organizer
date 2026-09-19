// file: internal/server/batch_poller.go
// version: 1.10.0
// guid: f8a1b2c3-d4e5-6789-abcd-0123456789ab
// last-edited: 2026-09-19

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var batchPollerLog = logger.New("batch_poller")

// BatchCompletionHandler processes a completed batch.
// It receives the batch ID and the output file ID for downloading results.
//
// A handler must tolerate being called again for a batch it already finished:
// the durable "handled" mark is written only after it returns nil, so a process
// killed in that gap re-delivers the batch once on the next start.
type BatchCompletionHandler func(ctx context.Context, batchID string, outputFileID string) error

// BatchReconciler is shown every listed project batch of its type, in any
// status, before completed batches are dispatched. It repairs local records
// that lost their link to a batch (see aijobs.ReconcileOrphans).
type BatchReconciler func(ctx context.Context, batches []ai.BatchInfo) error

// BatchClient is the slice of *ai.OpenAIParser the poller uses. An interface so
// tests can drive Poll without a live OpenAI client.
type BatchClient interface {
	// ListProjectBatches lists our batches, paging back to since (one page
	// when since is zero).
	ListProjectBatches(ctx context.Context, since time.Time) ([]ai.BatchInfo, error)
	GetBatch(ctx context.Context, batchID string) (ai.BatchInfo, error)
	DownloadBatchRaw(ctx context.Context, outputFileID string) ([]ai.BatchRawResult, error)
}

// BatchTracker reports, from local state, the batch ids still owed results and
// the creation time of the oldest local record not yet linked to its batch
// (zero if none). Tracked batches are fetched by id every tick, so a retry
// never depends on the batch still being in the recent listing; the listing is
// only for discovery, paged back to the oldest unlinked record.
type BatchTracker func(ctx context.Context) (batchIDs []string, oldestUnlinked time.Time, err error)

// orphanListSlack widens the listing window below the oldest unlinked record's
// timestamp to absorb clock skew between this host and OpenAI.
const orphanListSlack = time.Hour

// dispatchable reports whether a listed batch has results to apply: completed,
// or ended by OpenAI (expired/cancelled/failed) with a partial output file for
// the requests that did finish — paid results that must not be dropped.
func dispatchable(b ai.BatchInfo) bool {
	switch b.Status {
	case "completed":
		return true
	case "expired", "cancelled", "failed":
		return b.OutputFileID != ""
	}
	return false
}

// batchJournalPrefix keys the durable "handled" record per OpenAI batch id. It
// shares the aijournal: root with internal/ai/resultjournal (whose kinds are
// content-addressed result caches, e.g. aijournal:whisper:); "batch" is a
// distinct kind, so the keyspaces cannot collide.
const batchJournalPrefix = "aijournal:batch:"

type batchJournalEntry struct {
	Type      string    `json:"type"`
	HandledAt time.Time `json:"handled_at"`
}

// BatchPoller is a unified poller that discovers completed OpenAI batches
// tagged with project metadata and routes them to the appropriate handler.
//
// Which batches have been handled is recorded durably (batchJournalPrefix in
// the store's raw KV space), not only in memory. It used to be a map alone, so
// every restart re-dispatched every completed batch still in the listing's 100
// most recent — re-applying dedup verdicts and re-upserting embeddings each
// time the process started.
type BatchPoller struct {
	db      database.OperationStore
	journal database.RawKVStore
	client  BatchClient

	handlers    map[string]BatchCompletionHandler
	reconcilers map[string]BatchReconciler
	trackers    map[string]BatchTracker

	mu sync.Mutex
	// processed caches journal hits so a handled batch costs one KV read per
	// process lifetime, not one per tick.
	processed map[string]bool
	// inFlight holds batch ids a Poll is dispatching right now. Poll has two
	// callers (the scheduler task and the maintenance deps hook); without this
	// both could pass the "not handled" check and dispatch the same batch.
	inFlight map[string]bool
}

// NewBatchPoller creates a new BatchPoller. db must expose RawKVStore (directly
// or through its Unwrap chain): without a durable journal the poller would
// silently fall back to re-applying every batch after each restart, so that is
// a construction error rather than a degraded mode.
func NewBatchPoller(db database.OperationStore, client BatchClient) (*BatchPoller, error) {
	journal, ok := database.AsCapability[database.RawKVStore](db)
	if !ok || journal == nil {
		return nil, fmt.Errorf("batch poller: store %T has no RawKVStore capability for the handled-batch journal", db)
	}
	return &BatchPoller{
		db:          db,
		journal:     journal,
		client:      client,
		handlers:    make(map[string]BatchCompletionHandler),
		reconcilers: make(map[string]BatchReconciler),
		trackers:    make(map[string]BatchTracker),
		processed:   make(map[string]bool),
		inFlight:    make(map[string]bool),
	}, nil
}

// RegisterHandler registers a handler for a specific batch type.
// The type corresponds to the "type" metadata key set during batch creation.
func (bp *BatchPoller) RegisterHandler(batchType string, handler BatchCompletionHandler) {
	bp.handlers[batchType] = handler
}

// RegisterReconciler registers a reconciler for a specific batch type.
func (bp *BatchPoller) RegisterReconciler(batchType string, r BatchReconciler) {
	bp.reconcilers[batchType] = r
}

// RegisterTracker registers a tracker for a specific batch type.
func (bp *BatchPoller) RegisterTracker(batchType string, t BatchTracker) {
	bp.trackers[batchType] = t
}

// Poll queries OpenAI for all project-tagged batches, runs the reconcilers,
// finds completed batches not yet handled, and dispatches them to registered
// handlers. Returns the number of batches successfully processed.
//
// Reconcile runs on every tick, the first one after startup included, rather
// than once at boot: it needs the store fully warmed, and the scheduler's first
// tick is where the poller first runs anyway. It is cheap — one job-row read per
// listed batch carrying an owner key.
func (bp *BatchPoller) Poll(ctx context.Context) (int, error) {
	tracked := make(map[string]string) // batch id -> type
	var since time.Time
	for typ, track := range bp.trackers {
		ids, oldest, err := track(ctx)
		if err != nil {
			batchPollerLog.Error("track %s batches: %v", typ, err)
			continue
		}
		for _, id := range ids {
			tracked[id] = typ
		}
		if !oldest.IsZero() && (since.IsZero() || oldest.Before(since)) {
			since = oldest
		}
	}
	if !since.IsZero() {
		since = since.Add(-orphanListSlack)
	}

	batches, listErr := bp.client.ListProjectBatches(ctx, since)
	if listErr != nil {
		// Tracked batches are still fetched by id below; the listing only
		// feeds discovery.
		batchPollerLog.Error("list project batches: %v", listErr)
	}
	listed := make(map[string]bool, len(batches))
	for _, b := range batches {
		listed[b.ID] = true
	}
	for id := range tracked {
		if listed[id] || bp.IsProcessed(id) {
			continue
		}
		b, err := bp.client.GetBatch(ctx, id)
		if err != nil {
			batchPollerLog.Error("get tracked batch %s: %v", id, err)
			continue
		}
		batches = append(batches, b)
	}

	bp.reconcile(ctx, batches)

	processed := 0
	for _, b := range batches {
		if !dispatchable(b) {
			continue
		}
		if bp.dispatch(ctx, b) {
			processed++
		}
	}
	if listErr != nil && len(tracked) == 0 {
		return processed, listErr
	}
	return processed, nil
}

// dispatch runs one completed batch through its handler and reports whether it
// was handled. The in-flight hold is released by a deferred finish so a
// panicking handler cannot leave the batch claimed for the process lifetime.
func (bp *BatchPoller) dispatch(ctx context.Context, b ai.BatchInfo) (handled bool) {
	if !bp.claim(b.ID) {
		return false
	}
	handler, ok := bp.handlers[b.Type]
	if !ok {
		// Memory only, never journaled: the poller did no work on this batch
		// (author_dedup / author_review / diagnostics batches are collected by
		// their owners), and a handler registered in a later build must still
		// see it. The in-memory mark just stops the warning repeating each tick.
		batchPollerLog.Warn("no handler for batch type %q (batch %s)", b.Type, b.ID)
		bp.release(b.ID, true)
		return false
	}
	defer func() { bp.finish(b.ID, b.Type, handled) }()

	if err := handler(ctx, b.ID, b.OutputFileID); err != nil {
		if errors.Is(err, aijobs.ErrApplyBackoff) {
			batchPollerLog.Info("%s batch %s: %v", b.Type, b.ID, err)
			return false
		}
		if errors.Is(err, database.ErrAIJobNotFound) {
			// No local job names this batch: another install sharing the
			// OpenAI project. Skip it for this process (memory only, so a
			// restart looks once more) instead of erroring every tick.
			batchPollerLog.Debug("%s batch %s is not ours (no job row); ignoring", b.Type, b.ID)
			// The deferred finish(handled=false) then only drops the claim.
			bp.release(b.ID, true)
			return false
		}
		batchPollerLog.Error("handler for %s batch %s failed, will retry next poll: %v", b.Type, b.ID, err)
		return false
	}
	batchPollerLog.Info("processed %s batch %s", b.Type, b.ID)
	return true
}

// reconcile hands each registered reconciler the listed batches of its type
// that are not yet handled. Errors are logged; they never block dispatch.
func (bp *BatchPoller) reconcile(ctx context.Context, batches []ai.BatchInfo) {
	if len(bp.reconcilers) == 0 {
		return
	}
	byType := make(map[string][]ai.BatchInfo)
	for _, b := range batches {
		if _, ok := bp.reconcilers[b.Type]; !ok {
			continue
		}
		if bp.IsProcessed(b.ID) {
			continue
		}
		byType[b.Type] = append(byType[b.Type], b)
	}
	for typ, list := range byType {
		if err := bp.reconcilers[typ](ctx, list); err != nil {
			batchPollerLog.Error("reconcile %s batches: %v", typ, err)
		}
	}
}

// claim reports whether the caller should dispatch batchID: not handled (in
// memory or in the journal) and not being dispatched by a concurrent Poll. On
// true the id is held in-flight until finish.
func (bp *BatchPoller) claim(batchID string) bool {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.processed[batchID] || bp.inFlight[batchID] {
		return false
	}
	if bp.journaledLocked(batchID) {
		bp.processed[batchID] = true
		return false
	}
	bp.inFlight[batchID] = true
	return true
}

// finish releases the in-flight hold and, when handled, records the batch
// durably. A failed journal write is logged and the batch stays handled in
// memory: the next restart re-delivers it once, which every handler tolerates
// (see BatchCompletionHandler).
func (bp *BatchPoller) finish(batchID, batchType string, handled bool) {
	var journalErr error
	if handled {
		journalErr = bp.writeJournal(batchID, batchType)
	}
	bp.release(batchID, handled)
	if journalErr != nil {
		batchPollerLog.Error("record batch %s handled: %v (it will be re-delivered once after a restart)", batchID, journalErr)
	}
}

// release drops the in-flight hold and, when handled, caches the in-memory mark.
func (bp *BatchPoller) release(batchID string, handled bool) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	delete(bp.inFlight, batchID)
	if handled {
		bp.processed[batchID] = true
	}
}

func (bp *BatchPoller) writeJournal(batchID, batchType string) error {
	data, err := json.Marshal(batchJournalEntry{Type: batchType, HandledAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return bp.journal.SetRaw(batchJournalPrefix+batchID, data)
}

// journaledLocked reads the durable mark. A read error counts as "not handled":
// re-delivering is safe (handlers are idempotent), dropping results is not.
func (bp *BatchPoller) journaledLocked(batchID string) bool {
	v, err := bp.journal.GetRaw(batchJournalPrefix + batchID)
	if err != nil {
		batchPollerLog.Warn("read handled mark for batch %s: %v; treating it as unhandled", batchID, err)
		return false
	}
	return len(v) > 0
}

// IsProcessed returns whether a batch ID has already been handled, in this
// process or (through the journal) any earlier one.
func (bp *BatchPoller) IsProcessed(batchID string) bool {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.processed[batchID] {
		return true
	}
	if bp.journaledLocked(batchID) {
		bp.processed[batchID] = true
		return true
	}
	return false
}

// MarkProcessed durably marks a batch as processed (e.g. from external code
// that handled the batch before the poller was created).
func (bp *BatchPoller) MarkProcessed(batchID string) {
	bp.finish(batchID, "", true)
}

// aijobsBatchHandler downloads an aijobs batch and hands it to aijobs.Dispatch.
// Re-delivery is safe: Dispatch returns nil without applying for a job already
// in a terminal status, and that status is written before the poller's mark.
func aijobsBatchHandler(client BatchClient, getStore func() database.AIJobsStore) BatchCompletionHandler {
	return func(ctx context.Context, batchID, outputFileID string) error {
		if outputFileID == "" {
			return fmt.Errorf("aijobs: no output file for batch %s", batchID)
		}
		store := getStore()
		if store == nil {
			return fmt.Errorf("aijobs: store does not implement AIJobsStore")
		}
		raw, err := client.DownloadBatchRaw(ctx, outputFileID)
		if err != nil {
			return fmt.Errorf("aijobs: download batch %s: %w", batchID, err)
		}
		results := make([]aijobs.RowResult, 0, len(raw))
		for _, r := range raw {
			results = append(results, aijobs.RowResult{
				CustomID: r.CustomID,
				Content:  r.Content,
				Err:      r.Error,
			})
		}
		return aijobs.Dispatch(ctx, store, batchID, results)
	}
}

// embedAsyncBatchHandler stores an embed_async batch's vectors. Any failed
// upsert fails the whole handler so the poller does not record the batch
// handled and retries it next tick; re-upserting the rows that did land is an
// overwrite keyed by (entity_type, entity_id), so the retry is idempotent.
// It used to log per-row failures and return nil, which journaled the batch
// and left those books without vectors for good.
func embedAsyncBatchHandler(
	download func(ctx context.Context, outputFileID string) ([]ai.EmbedBatchResult, error),
	upsert func(database.Embedding) error,
) BatchCompletionHandler {
	return func(ctx context.Context, batchID, outputFileID string) error {
		if outputFileID == "" {
			return fmt.Errorf("embed_async: no output file for batch %s", batchID)
		}
		results, err := download(ctx, outputFileID)
		if err != nil {
			return fmt.Errorf("embed_async: download results for batch %s: %w", batchID, err)
		}
		var failed int
		var firstErr error
		for _, r := range results {
			if err := upsert(database.Embedding{
				EntityType: "book",
				EntityID:   r.BookID,
				Vector:     r.Vector,
				Model:      "text-embedding-3-large",
			}); err != nil {
				failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("book %s: %w", r.BookID, err)
				}
			}
		}
		if failed > 0 {
			return fmt.Errorf("embed_async: batch %s: %d of %d upserts failed (first: %w)", batchID, failed, len(results), firstErr)
		}
		batchPollerLog.Info("embed_async stored %d embeddings from batch %s", len(results), batchID)
		return nil
	}
}

// aijobsTracker feeds the poller the aijobs batches still owed results and the
// oldest job not yet linked to its batch.
func aijobsTracker(getStore func() database.AIJobsStore) BatchTracker {
	return func(context.Context) ([]string, time.Time, error) {
		store := getStore()
		if store == nil {
			return nil, time.Time{}, fmt.Errorf("aijobs: store does not implement AIJobsStore")
		}
		t, err := aijobs.TrackJobs(store)
		return t.BatchIDs, t.OldestUnlinked, err
	}
}

// aijobsReconciler re-attaches aijobs batches whose id never reached their job
// row (process killed between CreateBatch and MarkAIJobSubmitted).
func aijobsReconciler(getStore func() database.AIJobsStore) BatchReconciler {
	return func(_ context.Context, batches []ai.BatchInfo) error {
		store := getStore()
		if store == nil {
			return fmt.Errorf("aijobs: store does not implement AIJobsStore")
		}
		orphans := make([]aijobs.OrphanBatch, 0, len(batches))
		for _, b := range batches {
			orphans = append(orphans, aijobs.OrphanBatch{ID: b.ID, Status: b.Status, HasOutput: b.OutputFileID != "", Metadata: b.Metadata})
		}
		_, err := aijobs.ReconcileOrphans(store, orphans)
		return err
	}
}

// registerBatchPollerHandlers sets up handlers for all known batch types.
func (s *Server) registerBatchPollerHandlers() {
	if s.batchPoller == nil {
		return
	}

	// author_dedup, author_review and diagnostics used to be handled here: each
	// downloaded its results and passed them to storeBatchResultForOperation,
	// which located the owning operation by listing the 100 most recent legacy
	// rows and matching batch_id inside result_data.
	//
	// Nothing could reach that path any more, so it was deleted rather than
	// carried forward. To be found, an operation had to write batch_id into its
	// legacy result_data at SUBMIT time, and only handlers.SubmitAI ever did —
	// the other two producers already own their batches end to end:
	// maintenance/dedup_ops.go polls its own and writes batch_id only after it
	// completes, and aiscan/pipeline.go keeps the id on the scan-phase row that
	// pipeline.go's own loop polls. SubmitAI is now the diagnostics.ai-analyze
	// OperationDef, which likewise polls its own batch, so the last caller went
	// with it.
	//
	// Deleting the scan also retires the bug inside it: a batch may run for 24h,
	// and an operation that had scrolled past that 100-row window by the time
	// its results arrived had them dropped with nothing but an info log.

	// pipeline: delegate to the pipeline manager. PollBatchPhases only acts on
	// phases still "submitted", so re-delivery after a restart is a no-op.
	s.batchPoller.RegisterHandler("pipeline", func(ctx context.Context, batchID, outputFileID string) error {
		if s.pipelineManager == nil {
			return fmt.Errorf("pipeline manager not initialized")
		}
		s.pipelineManager.PollBatchPhases(ctx)
		return nil
	})

	// aijobs: unified layer for all bulk-scale LLM work. All such batches
	// carry metadata.type="aijobs"; the per-feature routing happens inside
	// aijobs.Dispatch by looking up the ai_jobs row for this batch_id.
	// Re-delivery is harmless: Dispatch applies nothing for a job already in a
	// terminal status. The reconciler re-attaches jobs orphaned by a kill
	// between CreateBatch and MarkAIJobSubmitted.
	getAIJobs := func() database.AIJobsStore { return database.GetAIJobs(s.Ops()) }
	s.batchPoller.RegisterHandler("aijobs", aijobsBatchHandler(s.batchPoller.client, getAIJobs))
	s.batchPoller.RegisterReconciler("aijobs", aijobsReconciler(getAIJobs))
	s.batchPoller.RegisterTracker("aijobs", aijobsTracker(getAIJobs))

	// embed_async: re-delivery re-upserts the same vectors keyed by
	// (entity_type, entity_id), which overwrites rather than adds.
	s.batchPoller.RegisterHandler("embed_async", embedAsyncBatchHandler(
		func(ctx context.Context, fileID string) ([]ai.EmbedBatchResult, error) {
			if s.embedClient == nil {
				return nil, fmt.Errorf("embedding client not available")
			}
			return s.embedClient.DownloadEmbeddingBatchResults(ctx, fileID)
		},
		func(e database.Embedding) error {
			if s.embeddingStore == nil {
				return fmt.Errorf("embedding store not available")
			}
			return s.embeddingStore.Upsert(e)
		},
	))
}
