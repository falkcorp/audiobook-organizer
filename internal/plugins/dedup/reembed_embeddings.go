// file: internal/plugins/dedup/reembed_embeddings.go
// version: 1.8.0
// guid: 9d8c7b6a-5e4f-3a2b-1c0d-9e8f7a6b5c4d
// last-edited: 2026-11-18

// Package dedup — op dedup.reembed-embeddings.
//
// Re-embeds the book corpus when the configured embedding model changes
// (e.g. switching from OpenAI text-embedding-3-large [3072-dim] to a local
// Ollama bge-m3 [1024-dim]). A model switch invalidates every stored vector:
// the dimensions differ and the vector spaces are not comparable, so the old
// vectors must be replaced before the embedding-based dedup layer is re-enabled.
//
// # Why a dedicated op (not just EmbedBooks)
//
// EmbedBooks/prepBookEmbed short-circuit on a TextHash cache hit — they do NOT
// compare the stored vector's model. A book whose title/author text is
// unchanged would therefore keep its OLD-model vector (wrong dimension) and
// even mirror it into the in-memory chromem ANN store. This op deletes the
// stale entity embedding first, forcing a genuine re-embed at the new model.
//
// # Resumability
//
// Stored entity embeddings are tagged with the model that produced them
// (EmbedBooks records de.embedClient.Model()). The scan phase skips any book
// already at the target model, so a re-run after interruption only processes
// the remainder. ResumePolicy is ResumeRequeue.
//
// # Cutover sequence (operational)
//
//	1. PUT /config {embedding:{model, dimensions, base_url},
//	   dedup:{embeddings_enabled:false}}   ← Layer 2 OFF during re-embed
//	   (nested form only: flat keys such as embedding_model or
//	   dedup_embeddings_enabled are rejected with a 400)
//	2. restart (chromem rebuilds at the new dim; embed client points at backend)
//	3. POST /operations/v2 {def_id:"dedup.reembed-embeddings"}              # dry-run
//	4. POST /operations/v2 {def_id:"dedup.reembed-embeddings",params:{apply:true}}
//	5. restart (hydrate chromem from the fresh vectors) → PUT /config {dedup:{embeddings_enabled:true}}
//
// Usage:
//
//	POST /api/v1/operations/v2  {"def_id":"dedup.reembed-embeddings"}                       # dry-run
//	POST /api/v1/operations/v2  {"def_id":"dedup.reembed-embeddings","params":{"apply":true}}  # execute
//
// Scope: books only. Author embeddings are a separate, much smaller set and
// live in a distinct chromem collection ("author"); re-embedding them is left
// to a follow-up.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	// defaultReembedBatchSize is the number of books embedded per backend call.
	// One batch = one /v1/embeddings request; bge-m3 on Ollama handles batched
	// input arrays. Kept modest so a single failed request loses little work.
	defaultReembedBatchSize = 64
	maxReembedBatchSize     = 512
)

// reembedEmbeddingsParams are the JSON parameters accepted by the op.
type reembedEmbeddingsParams struct {
	// Apply, if true, deletes stale vectors and writes fresh ones. Default
	// false (dry-run) — the op counts what it would re-embed and returns.
	Apply bool `json:"apply"`
	// BatchSize overrides the per-request book count (1..maxReembedBatchSize).
	// Zero uses defaultReembedBatchSize.
	BatchSize int `json:"batch_size"`
}

// reembedEmbeddingsKind is rule R3's native kind for dedup.reembed-embeddings.
const reembedEmbeddingsKind = tasks.Kind("books.dedup.reembed-embeddings")

// reembedEmbeddingsHelp is the definition's description, HEAD's and the native
// kind's Help (rule R5: verbatim).
const reembedEmbeddingsHelp = "Re-embeds every book whose stored embedding was produced by a " +
	"different model than the one currently configured. Required after switching " +
	"embedding models (e.g. OpenAI 3072-dim → local bge-m3 1024-dim) because the " +
	"vector spaces are incompatible. Dry-run by default (pass apply=true to execute). " +
	"Resumable — re-running skips books already at the target model."

// reembedEmbeddingsDef returns the OperationDef for dedup.reembed-embeddings.
func (p *Plugin) reembedEmbeddingsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.reembed-embeddings",
		Preview:         sdk.PreviewSpec{Params: reembedEmbeddingsParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Re-embed corpus (embedding model change)",
		Description:     reembedEmbeddingsHelp,
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.reembed-embeddings",
		Cancellable:     true,
		Isolate:         false,
		// Re-embedding the full corpus through a CPU-bound local backend can run
		// for hours; allow generous headroom. Cancellation + resume cover the
		// case where it genuinely needs longer.
		Timeout: 12 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runReembedEmbeddings,
	}
}

// reembedEmbeddingsStore is the reads the core takes (rule R10): the paged book
// list and the per-book embedding lookup of the Phase-1 scan.
type reembedEmbeddingsStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	bookEmbeddingGetter
}

// reembedEmbeddingsWriter is the one direct write the core makes: dropping a
// stale entity embedding before its re-embed (HEAD: EmbeddingStore.Delete).
type reembedEmbeddingsWriter interface {
	Delete(entityType, entityID string) error
}

// reembedEmbeddingsEngine is the engine write the core makes (EmbedBooks upserts
// the fresh vectors through the engine's own store).
type reembedEmbeddingsEngine interface {
	EmbedBooks(ctx context.Context, bookIDs []string) (map[string]dedupengine.EmbedStatus, error)
}

// reembedEmbeddingsEnv is what the core takes from its shell (rules R2 and R10).
// Live is HEAD's apply=true; a dry run never calls Writer or Engine, which may
// then be nil. Model is the wired embedding client's model (HEAD:
// p.engine.EmbeddingModel); ToolAvailable is HEAD's p.toolRegistry.Available (nil
// when no registry is wired).
type reembedEmbeddingsEnv struct {
	kindEnv
	Params        reembedEmbeddingsParams
	Store         reembedEmbeddingsStore
	Writer        reembedEmbeddingsWriter
	Engine        reembedEmbeddingsEngine
	Model         func() string
	ToolAvailable func(name string) bool
}

// reembedEmbeddingsResult is the native run's result. HEAD stored no result, so
// its shell drops it.
type reembedEmbeddingsResult struct {
	TargetModel string `json:"target_model"`
	Total       int    `json:"total"`
	Current     int    `json:"current"`
	NeedReembed int    `json:"need_reembed"`
	Embedded    int    `json:"embedded"`
	Skipped     int    `json:"skipped"`
	Canceled    bool   `json:"canceled"`
}

// runReembedEmbeddings is the v2 shell: HEAD's nil checks over
// reembedEmbeddingsCore.
func (p *Plugin) runReembedEmbeddings(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available (embeddings disabled?)")
	}
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	// --- Parse params ---
	var params reembedEmbeddingsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	_, err := reembedEmbeddingsCore(ctx, reembedEmbeddingsEnv{
		kindEnv:       headEnv(reporter, params.Apply),
		Params:        params,
		Store:         headEmbeddingReads{emb: p.embeddingStore, books: p.store},
		Writer:        headEmbeddingWriter{emb: p.embeddingStore},
		Engine:        p.engine,
		Model:         p.engine.EmbeddingModel,
		ToolAvailable: p.toolAvailableFunc(),
	})
	return err
}

// toolAvailableFunc is the plugin's tool registry as a function, nil when none
// is wired.
func (p *Plugin) toolAvailableFunc() func(string) bool {
	if p.toolRegistry == nil {
		return nil
	}
	return p.toolRegistry.Available
}

// reembedEmbeddingsCore is the body of runReembedEmbeddings, shared by both
// shells.
func reembedEmbeddingsCore(ctx context.Context, env reembedEmbeddingsEnv) (reembedEmbeddingsResult, error) {
	// Guard: local re-embed requires Ollama to be running.
	if config.AppConfig.Embedding.BaseURL != "" {
		if env.ToolAvailable != nil && !env.ToolAvailable("ollama") {
			return reembedEmbeddingsResult{}, fmt.Errorf("reembed-embeddings: local embedding base URL configured but Ollama is not available — install or enable via Settings → Tools: %w", tools.ErrToolNotAvailable)
		}
	}

	// The target model is whatever the wired embedding client is pinned to —
	// the authoritative value EmbedBooks will tag fresh vectors with.
	targetModel := env.Model()
	if targetModel == "" {
		return reembedEmbeddingsResult{}, fmt.Errorf("no embedding client configured (need OpenAIAPIKey + EmbeddingEnabled; " +
			"for a local backend also set embedding_base_url + embedding_model)")
	}

	batchSize := env.Params.BatchSize
	if batchSize <= 0 {
		batchSize = defaultReembedBatchSize
	}
	if batchSize > maxReembedBatchSize {
		batchSize = maxReembedBatchSize
	}

	env.Logger.Info("reembed-embeddings start",
		"target_model", targetModel, "apply", env.Live, "batch_size", batchSize)

	// --- Phase 1: scan, partition books by stored model ---
	env.Progress(0, 2, "Scanning books for stale-model embeddings…")

	// Page the library in with bounded GetAllBooksCore reads, then classify
	// every book against the embedding store in one parallel pass. Paging and
	// classifying were previously fused into a single sequential loop doing one
	// synchronous embeddingStore.Get per book on one core (DA-03).
	const scanBatch = 500
	var books []database.BookCore
	offset := 0
	for {
		if env.Canceled() {
			return reembedEmbeddingsResult{}, context.Canceled
		}
		select {
		case <-ctx.Done():
			return reembedEmbeddingsResult{}, ctx.Err()
		default:
		}

		batch, err := env.Store.GetAllBooksCore(scanBatch, offset)
		if err != nil {
			return reembedEmbeddingsResult{}, fmt.Errorf("get all books at offset %d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}
		books = append(books, batch...)
		if len(batch) < scanBatch {
			break
		}
		offset += scanBatch
	}

	scan, err := scanReembedCandidatesVia(ctx, env.kindEnv, books, targetModel, env.Store)
	if err != nil {
		return reembedEmbeddingsResult{}, err
	}
	totalBooks, current, toReembed := scan.Total, scan.Current, scan.ToReembed

	needCount := len(toReembed)
	res := reembedEmbeddingsResult{TargetModel: targetModel, Total: totalBooks, Current: current, NeedReembed: needCount}
	summary := fmt.Sprintf("%d books total, %d already at %q, %d need re-embedding",
		totalBooks, current, targetModel, needCount)
	env.Logger.Info("reembed-embeddings: scan complete",
		"total_books", totalBooks, "already_current", current,
		"need_reembed", needCount, "target_model", targetModel)

	if !env.Live {
		env.Progress(2, 2, fmt.Sprintf(
			"Dry-run — %d books would be re-embedded with %q (%d already current). Pass apply=true to execute.",
			needCount, targetModel, current))
		env.Logger.Info("reembed-embeddings: dry-run only; no changes written",
			"would_reembed", needCount)
		return res, nil
	}

	// --- Phase 2: delete stale vectors + re-embed, in batches ---
	env.Progress(1, 2, fmt.Sprintf("Re-embedding %d books with %q…", needCount, targetModel))

	// Sequential on purpose: each batch is one /v1/embeddings request the
	// backend already fans out (and the engine enforces each endpoint's live cap
	// on), and the delete-then-embed order within a batch must hold so
	// prepBookEmbed cannot TextHash-cache-hit a stale vector; batches also stop
	// at the first whole-batch failure so a re-run resumes from the remainder.
	var embedded, skipped int
	for start := 0; start < needCount; start += batchSize {
		if env.Canceled() {
			// Emit a canceled (not "complete") message so an operator watching
			// the progress text isn't misled; the worker independently records
			// the terminal status as canceled from the run context.
			env.Progress(1, 2, fmt.Sprintf(
				"Canceled — %d of %d re-embedded (%d skipped). Re-run to continue.",
				embedded, needCount, skipped))
			env.Logger.Warn("reembed-embeddings: canceled mid-run; re-run to continue",
				"embedded", embedded, "skipped", skipped, "candidates", needCount)
			res.Embedded, res.Skipped, res.Canceled = embedded, skipped, true
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		end := min(start+batchSize, needCount)
		chunk := toReembed[start:end]

		// Delete the stale entity embedding first so prepBookEmbed cannot
		// TextHash-cache-hit the old-model vector and skip the re-embed.
		for _, id := range chunk {
			if err := env.Writer.Delete("book", id); err != nil {
				env.Logger.Warn("reembed-embeddings: delete stale embedding",
					"book_id", id, "err", err)
			}
		}

		results, err := env.Engine.EmbedBooks(ctx, chunk)
		if err != nil {
			// Whole-batch failure (e.g. backend unreachable). Fail visibly so the
			// operator fixes the backend and re-runs; already-embedded books are
			// skipped on the re-run (they now carry the target model).
			env.Logger.Error("reembed-embeddings: embed batch failed",
				"chunk_start", start, "size", len(chunk), "err", err)
			res.Embedded, res.Skipped = embedded, skipped
			return res, fmt.Errorf("embed batch at offset %d (%d books): %w", start, len(chunk), err)
		}
		for _, id := range chunk {
			if results[id] == dedupengine.EmbedStatusEmbedded {
				embedded++
			} else {
				// Cached (shouldn't happen post-delete), SkippedNonPrimary,
				// SkippedEmptyTitle, or omitted due to a per-book error.
				skipped++
			}
		}

		env.Progress(1, 2, fmt.Sprintf(
			"Re-embedded %d/%d (%d embedded, %d skipped)…", end, needCount, embedded, skipped))
	}
	res.Embedded, res.Skipped = embedded, skipped

	env.Progress(2, 2, fmt.Sprintf(
		"Complete — %d embedded, %d skipped of %d candidates (target model %q). %s",
		embedded, skipped, needCount, targetModel, summary))
	env.Logger.Info("reembed-embeddings: complete",
		"embedded", embedded, "skipped", skipped, "candidates", needCount,
		"target_model", targetModel)
	return res, nil
}

// embeddableForReembed reports whether a book with NO stored embedding is one
// the engine would actually embed — i.e. worth selecting into the re-embed set.
// Mirrors the engine's prepBookEmbed skip rules (non-primary versions and
// near-empty titles produce no embedding) so the op's candidate count converges
// instead of re-selecting un-embeddable books on every run. Books that already
// carry a stale-model vector are handled separately (they must be deleted
// regardless of embeddability) and do not go through this check.
// bookEmbeddingGetter is the one embedding-store read the Phase-1 scan needs.
// *database.EmbeddingStore satisfies it; tests substitute a fake so the scan
// can be driven without a Pebble-backed store.
type bookEmbeddingGetter interface {
	Get(entityType, entityID string) (*database.Embedding, error)
}

// reembedVerdict is the per-book outcome of one embedding-store lookup.
type reembedVerdict uint8

const (
	reembedVerdictSkip    reembedVerdict = iota // no embedding and not embeddable — nothing to do
	reembedVerdictCurrent                       // stored vector already at the target model
	reembedVerdictNeeded                        // stale-model vector, or embeddable with no vector yet
)

// reembedScanResult is what Phase 1 hands to Phase 2: the book count, how
// many are already at the target model, and the IDs to re-embed in the
// order GetAllBooksCore returned them.
type reembedScanResult struct {
	Total     int
	Current   int
	ToReembed []string
}

// scanReembedCandidates classifies every book by the model of its stored
// embedding: already at targetModel (Current), stale or missing-but-embeddable
// (ToReembed), or nothing to do. Split out of runReembedEmbeddings so the
// scan is directly unit-testable without a wired Engine.
//
// The lookups fan out over a runtime.NumCPU()-wide worker pool (DA-03): one
// synchronous embedding-store point-get per book over the whole library is
// exactly the single-core shape CLAUDE.md's concurrency mandate exists to
// catch. Workers never share mutable state: each writes only its own slot in
// verdicts (a disjoint partition by index), and the result is assembled from
// those slots afterwards in input order, so ToReembed keeps the listing
// order the sequential loop produced regardless of which worker finished
// first. The Label closure reads nothing shared either; RunItems invokes it
// inside the workers.
func scanReembedCandidates(ctx context.Context, reporter sdk.Reporter, books []database.BookCore, targetModel string, embeddings bookEmbeddingGetter) (reembedScanResult, error) {
	return scanReembedCandidatesVia(ctx, headEnv(reporter, false), books, targetModel, embeddings)
}

// scanReembedCandidatesVia is scanReembedCandidates over the env's hooks, so the
// native shell shares it (rule R10).
func scanReembedCandidatesVia(ctx context.Context, env kindEnv, books []database.BookCore, targetModel string, embeddings bookEmbeddingGetter) (reembedScanResult, error) {
	verdicts := make([]reembedVerdict, len(books))

	if err := env.Items(ctx, tasks.CPU(), len(books), kindItemsOptions{
		PureLabel: func(i, total int) string {
			return fmt.Sprintf("Scanning book %d/%d for stale-model embeddings…", i+1, total)
		},
	}, func(_ context.Context, i int) error {
		if env.Canceled() {
			return context.Canceled
		}
		b := &books[i]
		existing, getErr := embeddings.Get("book", b.ID)
		if getErr != nil {
			env.Logger.Warn("reembed-embeddings: get embedding during scan",
				"book_id", b.ID, "err", getErr)
			existing = nil
		}
		switch {
		case existing != nil && existing.Model == targetModel:
			// Already at the target model — nothing to do.
			verdicts[i] = reembedVerdictCurrent
		case existing != nil:
			// Stale-model vector present: must be deleted (and re-embedded if
			// the book is embeddable). Always include so the wrong-dimension
			// vector can't survive into chromem on the next hydrate.
			verdicts[i] = reembedVerdictNeeded
		case embeddableForReembed(b):
			// No embedding yet and the book is embeddable — a model switch
			// must populate it.
			verdicts[i] = reembedVerdictNeeded
		default:
			// No embedding and not embeddable (non-primary or near-empty
			// title): nothing to do. Excluding these lets the candidate count
			// converge to 0 across re-runs instead of re-selecting them forever.
			verdicts[i] = reembedVerdictSkip
		}
		return nil
	}); err != nil {
		// The callback only fails for cancellation (lookup errors are logged
		// and treated as "no embedding", never returned), and RunItems wraps
		// callback errors in errors.Join — hand back the same bare sentinel
		// the sequential loop returned.
		if ctx.Err() != nil {
			return reembedScanResult{}, ctx.Err()
		}
		if env.Canceled() {
			return reembedScanResult{}, context.Canceled
		}
		return reembedScanResult{}, err
	}

	result := reembedScanResult{Total: len(books)}
	for i, v := range verdicts {
		switch v {
		case reembedVerdictCurrent:
			result.Current++
		case reembedVerdictNeeded:
			result.ToReembed = append(result.ToReembed, books[i].ID)
		}
	}
	return result, nil
}

func embeddableForReembed(b *database.BookCore) bool {
	if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
		return false
	}
	return len([]rune(strings.TrimSpace(b.Title))) > 2
}
