// file: internal/dedup/engine.go
// version: 1.27.0
// guid: 8f3a1c6e-d472-4b9a-a5e1-7c2d9f0b3e84
// last-edited: 2026-06-14

package dedup

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var dedupTracer = otel.Tracer("audiobook-organizer/dedup")

// Engine orchestrates a 3-layer dedup system:
//   - Layer 1: Exact matching (free, instant) — same file hash, ISBN/ASIN, or near-identical titles
//   - Layer 2: Embedding similarity (cheap, ~250ms) — cosine similarity of OpenAI embeddings
//   - Layer 3: LLM review (expensive, batch only) — for ambiguous candidates
type Engine struct {
	embedStore   *database.EmbeddingStore
	chromemStore *database.ChromemEmbeddingStore
	bookStore    database.Store
	embedClient  *ai.EmbeddingClient
	llmParser    *ai.OpenAIParser
	mergeService *merge.Service
	aiJobsStore  database.AIJobsStore

	// Thresholds (read from config or set directly)
	BookHighThreshold   float64
	BookLowThreshold    float64
	AuthorHighThreshold float64
	AuthorLowThreshold  float64
	AutoMergeEnabled    bool

	// Layer 3 ambiguous zones — candidates whose similarity falls inside these
	// ranges (inclusive) are sent to the LLM during RunLLMReview.
	LLMBookLow    float64
	LLMBookHigh   float64
	LLMAuthorLow  float64
	LLMAuthorHigh float64

	// LLMMaxPairsPerRun caps how many pairs a single RunLLMReview invocation will
	// send to the LLM. Zero means "all pending ambiguous candidates".
	LLMMaxPairsPerRun int

	// Background-context fields used by event-driven flows (e.g. dedup-on-import
	// subscriber, chromem hydrate). Initialised lazily in PostInit so the
	// engine doesn't need a New-time ctx and Stop can cleanly cancel
	// outstanding work. Guarded by bgMu (RWMutex — readers are subscriber
	// goroutines that snapshot the ctx; writers are PostInit and Stop).
	//
	// bgWg tracks goroutines started under bgCtx so Stop can join them and
	// confirm the Pebble read is fully finished before the store closes.
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgMu     sync.RWMutex
	bgWg     sync.WaitGroup

	// stopTimeout overrides the default join timeout in Stop.  Zero means use
	// defaultHydrationStopTimeout.  Only set in tests.
	stopTimeout_ time.Duration

	// Unified scoring fields (T014).
	//
	// scoreConfig holds per-signal calibration and band thresholds for
	// unified.ComposeScore.  Loaded once at startup by LoadScoreConfig or
	// set directly in tests.  Zero value falls back to
	// unified.DefaultScoreConfig() on first use.
	scoreConfig *unified.ScoreConfig

	// acoustidBookFileStore is the narrow interface used by T013's
	// CollectExactAcoustID; set via SetAcoustIDBookFileStore.
	acoustidBookFileStore ExactAcoustIDStore

	// lshAcoustIDStore is the narrow interface used by T013's
	// CollectLSHAcoustID; set via SetLSHStore.
	lshAcoustIDStore LSHAcoustIDStore
}

// NewEngine creates a Engine with sensible defaults.
// llmParser may be nil if Layer 3 LLM review should be disabled.
// aiJobsStore may be nil; if so, RunLLMReview will fall back to synchronous ReviewDedupPairs.
func NewEngine(
	embedStore *database.EmbeddingStore,
	bookStore database.Store,
	embedClient *ai.EmbeddingClient,
	llmParser *ai.OpenAIParser,
	mergeService *merge.Service,
) *Engine {
	return &Engine{
		embedStore:          embedStore,
		bookStore:           bookStore,
		embedClient:         embedClient,
		llmParser:           llmParser,
		mergeService:        mergeService,
		BookHighThreshold:   0.95,
		BookLowThreshold:    0.85,
		AuthorHighThreshold: 0.92,
		AuthorLowThreshold:  0.80,
		AutoMergeEnabled:    false,
		LLMBookLow:          0.80,
		LLMBookHigh:         0.92,
		LLMAuthorLow:        0.75,
		LLMAuthorHigh:       0.85,
		LLMMaxPairsPerRun:   200,
	}
}

// SetAIJobsStore configures the aijobs store for async batch submissions.
// Must be called before RunLLMReview if async review is desired.
func (de *Engine) SetAIJobsStore(store database.AIJobsStore) {
	de.aiJobsStore = store
}

// LookupCandidate reloads a dedup candidate by ID from the embed store.
// Returns ok=false if the candidate has been deleted or purged since initial submission.
// Used by the aijobs dedup review callback to reconstruct state after the batch completes.
func (de *Engine) LookupCandidate(id int64) (database.DedupCandidate, bool) {
	c, err := de.embedStore.GetCandidateByID(id)
	if err != nil {
		slog.Error("dedup LookupCandidate() error", "id", id, "err", err)
		return database.DedupCandidate{}, false
	}
	if c == nil {
		return database.DedupCandidate{}, false
	}
	return *c, true
}

// SetChromemStore configures the ANN vector store for Layer 2.
// When set, FindSimilar queries go through chromem instead of
// the SQLite linear scan.
func (de *Engine) SetChromemStore(cs *database.ChromemEmbeddingStore) {
	de.chromemStore = cs
}

// SetAcoustIDBookFileStore wires the ExactAcoustIDStore used by
// CollectExactAcoustID (T013).  In production the caller passes the same
// *database.PebbleStore as bookStore.
func (de *Engine) SetAcoustIDBookFileStore(s ExactAcoustIDStore) {
	de.acoustidBookFileStore = s
}

// SetLSHStore wires the LSHAcoustIDStore used by CollectLSHAcoustID (T013).
// In production the caller passes the same *database.PebbleStore as bookStore.
func (de *Engine) SetLSHStore(s LSHAcoustIDStore) {
	de.lshAcoustIDStore = s
}

// embeddingsEnabled reports whether the embedding layer (Layer 2) should be
// active for this engine. Both conditions must hold:
//   - an embedding client is wired (de.embedClient != nil)
//   - the global config flag DedupEmbeddingsEnabled is true (default true;
//     set false on no-internet boxes via config or the settings API)
//
// Reading config.AppConfig directly (not a construction-time snapshot) mirrors
// the pattern used by DedupReviewModel and DedupLLMAutoMergeHighConfidence so
// a runtime settings toggle takes effect on the next FullScan without a restart.
func (de *Engine) embeddingsEnabled() bool {
	return de.embedClient != nil && config.AppConfig.DedupEmbeddingsEnabled
}

// SetScoreConfig overrides the unified scoring calibration.  Call this before
// any CheckBook/FullScan if you need non-default thresholds (tests, A/B).
// Pass a nil pointer to revert to DefaultScoreConfig.
func (de *Engine) SetScoreConfig(cfg *unified.ScoreConfig) {
	de.scoreConfig = cfg
}

// getScoreConfig returns the active ScoreConfig, initialising from
// unified.DefaultScoreConfig if none has been set.  WHY a pointer: we want
// the zero value of the Engine (before any setter call) to still work; lazy
// init here avoids a required setup step at NewEngine call sites.
func (de *Engine) getScoreConfig() unified.ScoreConfig {
	if de.scoreConfig != nil {
		return *de.scoreConfig
	}
	return unified.DefaultScoreConfig()
}

// HydrateChromem walks the SQLite embedding rows and copies any that are
// missing from the chromem ANN index. Run once at startup to bring chromem
// into sync with the canonical SQLite table — without this step, a fresh
// chromem dir (or one that fell behind because writes only went to SQLite)
// will return zero matches and Layer 2 silently degrades to "no candidates".
//
// Books that no longer exist or are non-primary version-group members are
// skipped; the embedding table may contain stale rows that EmbedBook would
// have cleaned up on its next visit.
//
// Returns counts so the caller can log progress. Errors on individual rows
// are logged but never abort the hydrate — partial coverage is better than
// no coverage.
func (de *Engine) HydrateChromem(ctx context.Context) (booksHydrated, authorsHydrated int, err error) {
	if de.chromemStore == nil || de.embedStore == nil {
		return 0, 0, nil
	}

	bookEmbeds, err := de.embedStore.ListByType("book")
	if err != nil {
		return 0, 0, fmt.Errorf("list book embeddings: %w", err)
	}
	for _, e := range bookEmbeds {
		if err := ctx.Err(); err != nil {
			return booksHydrated, authorsHydrated, err
		}
		if len(e.Vector) == 0 {
			continue
		}
		book, _ := de.bookStore.GetBookByID(e.EntityID)
		if book == nil {
			continue
		}
		// Skip non-primary versions — they should not participate in
		// dedup matches and the on-disk row is stale.
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}
		de.mirrorBookToChromem(ctx, book, e.Vector)
		booksHydrated++
	}

	authorEmbeds, err := de.embedStore.ListByType("author")
	if err != nil {
		return booksHydrated, authorsHydrated, fmt.Errorf("list author embeddings: %w", err)
	}
	for _, e := range authorEmbeds {
		if err := ctx.Err(); err != nil {
			return booksHydrated, authorsHydrated, err
		}
		if len(e.Vector) == 0 {
			continue
		}
		de.mirrorAuthorToChromem(ctx, e.EntityID, e.Vector)
		authorsHydrated++
	}
	return booksHydrated, authorsHydrated, nil
}

// CheckBook runs Layer 1 (exact) and Layer 2 (embedding) dedup checks for a book.
// Returns true if the book was auto-merged (Layer 1 only, when AutoMergeEnabled).
// Honors ctx cancellation so the dedup-on-import hook can bail immediately
// when the server is shutting down, rather than racing Pebble close.
func (de *Engine) CheckBook(ctx context.Context, bookID string) (bool, error) {
	_, span := dedupTracer.Start(ctx, "dedup.check_book",
		trace.WithAttributes(
			attribute.String("book_id", bookID),
		))
	defer span.End()

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return false, err
	}
	book, err := de.bookStore.GetBookByID(bookID)
	if err != nil {
		err := fmt.Errorf("get book %s: %w", bookID, err)
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return false, err
	}
	if book == nil {
		err := fmt.Errorf("book %s not found", bookID)
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true), attribute.Bool("not_found", true))
		return false, err
	}

	// Resolve author name
	authorName := ""
	if book.AuthorID != nil {
		author, err := de.bookStore.GetAuthorByID(*book.AuthorID)
		if err == nil && author != nil {
			authorName = author.Name
		}
	}

	// --- Layer 1: Exact matching ---
	merged, err := de.checkExactFileHash(book, authorName)
	if err != nil {
		slog.Error("dedup file hash check error for", "bookID", bookID, "err", err)
	}
	if merged {
		span.SetAttributes(attribute.Bool("merged", true))
		return true, nil
	}

	if err := de.checkExactISBN(book); err != nil {
		slog.Error("dedup ISBN check error for", "bookID", bookID, "err", err)
	}

	if err := de.checkExactMetadataSourceHash(book); err != nil {
		slog.Error("dedup metadata-source-hash check error for", "bookID", bookID, "err", err)
	}

	if err := de.checkExactTitle(book, authorName); err != nil {
		slog.Error("dedup title check error for", "bookID", bookID, "err", err)
	}

	if err := de.checkDurationMatch(book); err != nil {
		slog.Error("dedup duration check error for", "bookID", bookID, "err", err)
	}

	// --- Layer 2: Embedding similarity ---
	if de.embeddingsEnabled() {
		if _, err := de.EmbedBook(ctx, bookID); err != nil {
			slog.Error("dedup embed book error for", "bookID", bookID, "err", err)
		} else {
			if err := de.findSimilarBooks(ctx, bookID); err != nil {
				slog.Error("dedup similarity search error for", "bookID", bookID, "err", err)
			}
		}
	}

	// --- Unified composite scoring (T014) ---
	// Run the full collector suite for all candidate pairs that the above
	// layers produced, compose a single score via ComposeScore, and persist
	// ScoreBreakdown/Band/FormulaVersion alongside the existing
	// Layer/Similarity fields for backward compat.
	//
	// WHY after layers 1+2: embedding top-K results are required as the
	// candidate source for CollectMetaFuzzy (TASK-014 constraint: never O(N²)
	// title scan).  Running the unified pass here guarantees the embedding is
	// available.
	if err := de.runUnifiedScoringForBook(ctx, book, authorName); err != nil {
		slog.Error("dedup unified scoring error for", "bookID", bookID, "err", err)
	}

	return false, nil
}

// ─── unified scoring orchestration (T014) ────────────────────────────────────

// runUnifiedScoringForBook retrieves the embedding candidates for book (those
// written by findSimilarBooks in the same CheckBook/FullScan pass), then for
// each candidate pair:
//
//  1. Calls PairEligibility — drops suppressed pairs immediately.
//  2. Runs all collectors: exact-file, ISBN/ASIN, metadata-source-hash,
//     embedding (from stored similarity result), duration, metadata-fuzzy
//     (over embedding+LSH candidate IDs only — no O(N²) title scan), and the
//     T013 acoustid collectors (exact + LSH).
//  3. Calls unified.ComposeScore over the collected signals.
//  4. Persists the result by upserting via embedStore.UpsertCandidate, filling
//     ScoreBreakdown, Band, and FormulaVersion (T015 fields).  The existing
//     Layer and Similarity fields are also populated for backward compat:
//     Similarity = Score/100, Layer derived from the strongest primary signal.
//
// This method is best-effort: errors are logged at Debug level and do not abort
// the scan — the existing per-layer candidates written by the earlier checks
// remain valid.
func (de *Engine) runUnifiedScoringForBook(ctx context.Context, book *database.Book, authorName string) error {
	if de.embedStore == nil {
		return nil
	}

	// Load the embedding candidates that findSimilarBooks just wrote for this
	// book.  We iterate only these (embedding top-K) rather than all pending
	// candidates to avoid O(N²) cost and to satisfy the MetaFuzzy constraint.
	candidates, _, err := de.embedStore.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
	})
	if err != nil {
		return fmt.Errorf("runUnifiedScoringForBook: list candidates: %w", err)
	}

	// Build the set of other-book IDs this book is paired with in the current
	// embedding + LSH candidate set.  This is the candidate pool for MetaFuzzy.
	var embeddingCandIDs []string
	for _, c := range candidates {
		if c.EntityAID != book.ID && c.EntityBID != book.ID {
			continue
		}
		otherID := c.EntityBID
		if c.EntityBID == book.ID {
			otherID = c.EntityAID
		}
		embeddingCandIDs = append(embeddingCandIDs, otherID)
	}

	if len(embeddingCandIDs) == 0 {
		return nil
	}

	cfg := de.getScoreConfig()
	embCfg := DefaultEmbeddingCollectorConfig()
	embCfg.HighThreshold = de.BookHighThreshold
	embCfg.LowThreshold = de.BookLowThreshold
	durCfg := DefaultDurationCollectorConfig()
	fuzCfg := DefaultMetaFuzzyConfig()
	lshCfg := DefaultLSHAcoustIDConfig()

	// Get the book's files once for acoustid collectors.
	bookFiles, _ := de.bookStore.GetBookFiles(book.ID)

	for _, candID := range embeddingCandIDs {
		otherBook, err := de.bookStore.GetBookByID(candID)
		if err != nil || otherBook == nil {
			continue
		}

		// 1. Eligibility pre-filter.
		ok, suppressors := PairEligibility(book, otherBook)
		if !ok {
			slog.Debug("dedup unified: suppressed pair",
				"book", book.ID, "other", candID,
				"suppressors", suppressors)
			continue
		}

		// 2. Collect signals from all available collectors.
		var signals []unified.Signal

		// Exact-file hash signals.
		if sigs, err := CollectExactFileHash(de.bookStore, book); err == nil {
			for _, s := range sigs {
				// Only keep signals that match this specific candidate.
				if isSigForPair(s, book.ID, candID) {
					signals = append(signals, s)
				}
			}
		}

		// ISBN/ASIN — only emit if candidate matches.
		if sigs, err := CollectISBNASIN(de.bookStore, book); err == nil {
			for _, s := range sigs {
				if isSigForPair(s, book.ID, candID) {
					signals = append(signals, s)
				}
			}
		}

		// Metadata source hash.
		if sigs, err := CollectMetaSrcHash(de.bookStore, book); err == nil {
			for _, s := range sigs {
				if isSigForPair(s, book.ID, candID) {
					signals = append(signals, s)
				}
			}
		}

		// Embedding signal from existing candidate row (avoid re-scanning).
		for _, c := range candidates {
			if (c.EntityAID == book.ID && c.EntityBID == candID) ||
				(c.EntityBID == book.ID && c.EntityAID == candID) {
				if c.Similarity != nil && c.Layer == "embedding" {
					cos := float32(*c.Similarity)
					if float64(cos) >= embCfg.HighThreshold {
						signals = append(signals, unified.Signal{
							Kind:       unified.SigEmbedHigh,
							Raw:        float64(cos),
							Confidence: embedHighConfidence(cos),
							Evidence: fmt.Sprintf(
								"embedding cosine %.4f (high tier): book %s ↔ %s",
								cos, book.ID, candID),
						})
					} else if float64(cos) >= embCfg.LowThreshold {
						signals = append(signals, unified.Signal{
							Kind:       unified.SigEmbedMedium,
							Raw:        float64(cos),
							Confidence: embedMediumConfidence(cos),
							Evidence: fmt.Sprintf(
								"embedding cosine %.4f (medium tier): book %s ↔ %s",
								cos, book.ID, candID),
						})
					}
				}
				break
			}
		}

		// Duration signal.
		if sigs, err := CollectDuration(de.bookStore, de.bookStore, book, durCfg); err == nil {
			for _, s := range sigs {
				if isDurationSigFor(s.Evidence, book.ID, candID) {
					signals = append(signals, s)
				}
			}
		}

		// Metadata-fuzzy (uses embedding top-K candidates only per spec).
		if sigs, err := CollectMetaFuzzy(de.bookStore, book, authorName, []string{candID}, fuzCfg); err == nil {
			signals = append(signals, sigs...)
		}

		// AcoustID collectors (T013) — run per BookFile of the query book.
		if de.acoustidBookFileStore != nil {
			for _, bf := range bookFiles {
				exactSigs, _ := CollectExactAcoustID(de.acoustidBookFileStore, &bf, book.ID)
				for _, s := range exactSigs {
					if isSigForBookID(s.Evidence, candID) {
						signals = append(signals, s)
					}
				}
			}
		}
		if de.lshAcoustIDStore != nil {
			for _, bf := range bookFiles {
				lshSigs, _ := CollectLSHAcoustID(de.lshAcoustIDStore, &bf, book.ID, lshCfg)
				for _, s := range lshSigs {
					if isSigForBookID(s.Evidence, candID) {
						signals = append(signals, s)
					}
				}
			}
		}

		if len(signals) == 0 {
			continue
		}

		// 3. Compose score.
		canonicalPair := canonicalPairIDs(book.ID, candID)
		composed := unified.ComposeScore(signals, nil, cfg, canonicalPair)

		// Skip pairs below the review threshold — not worth persisting.
		if composed.Band == "" {
			continue
		}

		// 4. Persist: upsert with unified fields + back-compat Layer/Similarity.
		sim := composed.Score / 100.0
		layer := bestLayerFromSignals(signals)
		if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
			EntityType:     "book",
			EntityAID:      canonicalPair[0],
			EntityBID:      canonicalPair[1],
			Layer:          layer,
			Similarity:     &sim,
			Status:         "pending",
			ScoreBreakdown: &composed,
			Band:           composed.Band,
			FormulaVersion: composed.Formula,
		}); err != nil {
			slog.Debug("dedup unified: upsert error",
				"book", book.ID, "other", candID, "err", err)
		}
	}

	return nil
}

// canonicalPairIDs returns [aID, bID] in lexicographically sorted order so
// that UpsertCandidate's pair dedup key is stable regardless of call order.
func canonicalPairIDs(a, b string) [2]string {
	if a < b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

// isSigForPair reports whether sig.Evidence contains both bookID and candID.
// Used as a simple filter to match a signal emitted by a book-level collector
// (which scans all books) to a specific candidate pair.
func isSigForPair(sig unified.Signal, bookID, candID string) bool {
	return containsStr(sig.Evidence, bookID) && containsStr(sig.Evidence, candID)
}

// isSigForBookID reports whether sig.Evidence contains candBookID.
// Used to filter acoustid signals to a specific candidate book.
func isSigForBookID(evidence, candBookID string) bool {
	return containsStr(evidence, candBookID)
}

// isDurationSigFor checks whether the duration signal evidence mentions
// both bookIDs.
func isDurationSigFor(evidence, bookID, candID string) bool {
	return containsStr(evidence, bookID) && containsStr(evidence, candID)
}

// containsStr reports whether haystack contains needle (substring).
func containsStr(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// bestLayerFromSignals returns the most informative layer string for
// DedupCandidate.Layer field, for backward compat with old readers that
// sort/filter by layer name.
//
// Priority: exact_file > exact_acoustid > isbn_asin > metadata_hash >
// lsh_acoustid > embedding_high > metadata_fuzzy > embedding_med > duration
func bestLayerFromSignals(signals []unified.Signal) string {
	priority := map[unified.SignalKind]int{
		unified.SigExactFile:     9,
		unified.SigExactAcoustID: 8,
		unified.SigISBNASIN:      7,
		unified.SigMetaSrcHash:   6,
		unified.SigLSHAcoustID:   5,
		unified.SigEmbedHigh:     4,
		unified.SigMetaFuzzy:     3,
		unified.SigEmbedMedium:   2,
		unified.SigDuration:      1,
	}
	best := ""
	bestP := -1
	for _, s := range signals {
		p, ok := priority[s.Kind]
		if !ok {
			continue
		}
		if p > bestP {
			bestP = p
			best = layerNameForKind(s.Kind)
		}
	}
	if best == "" {
		return "embedding" // safe fallback
	}
	return best
}

// layerNameForKind maps a SignalKind to the legacy DedupCandidate.Layer string
// value.  The layer strings are the values already in use in the candidate
// store; new consumers should use ScoreBreakdown instead.
func layerNameForKind(k unified.SignalKind) string {
	switch k {
	case unified.SigExactFile, unified.SigISBNASIN, unified.SigMetaSrcHash:
		return "exact"
	case unified.SigExactAcoustID, unified.SigLSHAcoustID:
		return "acoustid"
	case unified.SigEmbedHigh, unified.SigEmbedMedium, unified.SigMetaFuzzy:
		return "embedding"
	default:
		return "embedding"
	}
}

// ─── end unified scoring orchestration ──────────────────────────────────────

// checkExactFileHash checks if any other book shares a file hash.
// Auto-merges if hashes match AND same normalized author AND same normalized title.
func (de *Engine) checkExactFileHash(book *database.Book, authorName string) (bool, error) {
	// Check book-level file hash
	if book.FileHash != nil && *book.FileHash != "" {
		other, err := de.bookStore.GetBookByFileHash(*book.FileHash)
		if err != nil {
			return false, err
		}
		if other != nil && other.ID != book.ID {
			return de.handleFileHashMatch(book, other, authorName)
		}
	}

	// Also check via book files
	files, err := de.bookStore.GetBookFiles(book.ID)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if f.FileHash == "" {
			continue
		}
		other, err := de.bookStore.GetBookByFileHash(f.FileHash)
		if err != nil {
			continue
		}
		if other != nil && other.ID != book.ID {
			merged, err := de.handleFileHashMatch(book, other, authorName)
			if err != nil {
				return false, err
			}
			if merged {
				return true, nil
			}
		}
	}
	return false, nil
}

// handleFileHashMatch decides whether to auto-merge or create a candidate for a file hash match.
func (de *Engine) handleFileHashMatch(book, other *database.Book, authorName string) (bool, error) {
	otherAuthorName := ""
	if other.AuthorID != nil {
		otherAuthor, err := de.bookStore.GetAuthorByID(*other.AuthorID)
		if err == nil && otherAuthor != nil {
			otherAuthorName = otherAuthor.Name
		}
	}

	sameAuthor := NormalizeAuthorName(authorName) == NormalizeAuthorName(otherAuthorName)
	sameTitle := normalizeTitle(book.Title) == normalizeTitle(other.Title)

	if sameAuthor && sameTitle && de.AutoMergeEnabled && de.mergeService != nil {
		_, err := de.mergeService.MergeBooks([]string{book.ID, other.ID}, other.ID)
		if err != nil {
			return false, fmt.Errorf("auto-merge failed: %w", err)
		}
		slog.Info("dedup auto-merged book into (file hash match)", "book", book.ID, "other", other.ID)
		return true, nil
	}

	// Create candidate even if we don't auto-merge
	sim := 1.0
	return false, de.embedStore.UpsertCandidate(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  book.ID,
		EntityBID:  other.ID,
		Layer:      "exact",
		Similarity: &sim,
		Status:     "pending",
	})
}

// checkExactISBN scans all books for matching ISBN10, ISBN13, or ASIN.
func (de *Engine) checkExactISBN(book *database.Book) error {
	bookISBN10 := derefStr(book.ISBN10)
	bookISBN13 := derefStr(book.ISBN13)
	bookASIN := derefStr(book.ASIN)

	if bookISBN10 == "" && bookISBN13 == "" && bookASIN == "" {
		return nil
	}
	if !hasPlausibleAudio(book) {
		return nil // stub / unscanned shell — never anchor an exact-ISBN match
	}

	const batchSize = 500
	offset := 0
	for {
		batch, err := de.bookStore.GetAllBooks(batchSize, offset)
		if err != nil {
			return fmt.Errorf("get all books at offset %d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}

		for i := range batch {
			other := &batch[i]
			if other.ID == book.ID {
				continue
			}
			if !hasPlausibleAudio(other) {
				continue // stub / unscanned shell on the other side
			}
			matched := false
			if bookISBN10 != "" && derefStr(other.ISBN10) == bookISBN10 {
				matched = true
			}
			if bookISBN13 != "" && derefStr(other.ISBN13) == bookISBN13 {
				matched = true
			}
			if bookASIN != "" && derefStr(other.ASIN) == bookASIN {
				matched = true
			}
			if matched {
				sim := 1.0
				if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
					EntityType: "book",
					EntityAID:  book.ID,
					EntityBID:  other.ID,
					Layer:      "exact",
					Similarity: &sim,
					Status:     "pending",
				}); err != nil {
					slog.Error("dedup upsert ISBN candidate error", "err", err)
				}
			}
		}

		if len(batch) < batchSize {
			break
		}
		offset += batchSize
	}
	return nil
}

// checkExactMetadataSourceHash is the MATCH-1 fast pre-pass: if two books
// share the same metadata_source_hash (sha256 of source:canonical_id) they
// were applied from the exact same external record and are almost certainly
// duplicates. Creates a dedup candidate with similarity 0.99.
//
// Note: intentionally NOT gated by hasPlausibleAudio. A shared
// metadata-source hash is a valid duplicate signal even when one side is an
// unscanned/incomplete file — unlike the title/ISBN emitters, this match is
// anchored to a concrete external record, not just a name collision.
func (de *Engine) checkExactMetadataSourceHash(book *database.Book) error {
	if book.MetadataSourceHash == nil || *book.MetadataSourceHash == "" {
		return nil
	}
	others, err := de.bookStore.GetBooksByMetadataSourceHash(*book.MetadataSourceHash)
	if err != nil {
		return fmt.Errorf("get books by metadata source hash: %w", err)
	}
	for i := range others {
		other := &others[i]
		if other.ID == book.ID {
			continue
		}
		sim := 0.99
		if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "book",
			EntityAID:  book.ID,
			EntityBID:  other.ID,
			Layer:      "metadata_hash",
			Similarity: &sim,
			Status:     "pending",
		}); err != nil {
			slog.Error("dedup upsert metadata-hash candidate error (book ↔ )", "book", book.ID, "other", other.ID, "err", err)
		}
	}
	return nil
}

// checkExactTitle checks all books by the same author for near-identical
// titles. Near-identical is defined as Levenshtein distance < 3 on the
// normalized titles, WITH a series-volume safety check: if both books
// carry a distinct series position (either on the Book.SeriesSequence
// field or extracted from the title string), they are rejected even when
// the raw Levenshtein falls under the threshold. Without this guard,
// numbered series volumes like "X 3: A LitRPG Adventure (X, Book 3)" vs
// "X 2: A LitRPG Adventure (X, Book 2)" match at distance 2 and get
// incorrectly flagged as exact duplicates.
//
// Books with empty or near-empty titles are also rejected here — a pair
// of empty strings has a Levenshtein distance of 0 and would otherwise
// match every other empty-titled book by the same author.
func (de *Engine) checkExactTitle(book *database.Book, authorName string) error {
	if book.AuthorID == nil {
		return nil
	}
	if !hasUsableTitle(book.Title) {
		return nil
	}
	if !hasPlausibleAudio(book) {
		return nil // stub / unscanned shell — never anchor an exact-title match
	}

	others, err := de.bookStore.GetBooksByAuthorID(*book.AuthorID)
	if err != nil {
		return fmt.Errorf("get books by author: %w", err)
	}

	bookForms := de.allNormalizedTitleForms(book)
	bookSeriesNum := seriesNumberOf(book)
	for i := range others {
		other := &others[i]
		if other.ID == book.ID {
			continue
		}
		if !hasUsableTitle(other.Title) {
			continue
		}
		if !hasPlausibleAudio(other) {
			continue // stub / unscanned shell on the other side
		}
		otherForms := de.allNormalizedTitleForms(other)
		// Closest-form distance: a match exists if ANY form of book is
		// within Levenshtein 2 of ANY form of other. Alt titles let the
		// user encode variants the normalizer can't auto-derive
		// (manga romaji vs English, rebrands, subtitle reorderings).
		dist := minLevenshteinBetweenForms(bookForms, otherForms)
		if dist >= 3 {
			continue
		}
		// Keep the original primary-title pair as the "chosen" form for
		// downstream guards (series-number, digit-diff). Alt-title
		// matching is only for reaching the pair — once we're past the
		// distance threshold we still sanity-check with primary titles.
		normTitle := normalizeTitle(book.Title)
		otherNormTitle := normalizeTitle(other.Title)
		// Series-volume safety (primary): if both books identify as
		// distinct volumes of the same series via structured metadata or
		// an explicit "Book N" / "bk N" / "Vol N" / "#N" marker in the
		// title, reject the pair. Merging volume 3 into volume 2 would
		// silently destroy user content.
		otherSeriesNum := seriesNumberOf(other)
		if bookSeriesNum != "" && otherSeriesNum != "" && bookSeriesNum != otherSeriesNum {
			continue
		}
		// Series-volume safety (fallback): if the normalized titles differ
		// ONLY in digit characters (same non-digit structure, different
		// numbers) the pair is almost certainly two volumes of a series
		// whose volume marker the regex didn't catch. This is the last-
		// ditch guard for title patterns like "Series Name 3" with no
		// explicit "book"/"bk"/"vol" token. False positives here are
		// limited to two books whose titles genuinely differ only in a
		// number — rare enough that dismissing them manually is much
		// cheaper than accidentally merging a wrong volume.
		if titlesDifferOnlyInDigits(normTitle, otherNormTitle) {
			continue
		}
		sim := 1.0
		if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "book",
			EntityAID:  book.ID,
			EntityBID:  other.ID,
			Layer:      "exact",
			Similarity: &sim,
			Status:     "pending",
		}); err != nil {
			slog.Error("dedup upsert title candidate error", "err", err)
		}
	}
	return nil
}

// durationMatchTolerance is the max percent difference in
// duration (seconds) for two books to be considered duration-
// matches. Two percent catches normal transcoding / chapter
// reshuffling variance while excluding abridged / reorganized
// editions which typically differ by 10%+.
const durationMatchTolerance = 0.02 // 2%

// durationAbridgedThreshold is the minimum percent difference
// for two books with matching title + author to be flagged as
// a likely abridged / unabridged pair rather than a duplicate.
// These aren't emitted as merge candidates — same book content,
// different editions — but BOTH books get a system tag so users
// can filter them for separate handling.
const durationAbridgedThreshold = 0.20 // 20%

// durationLevenshteinMax is the max Levenshtein distance
// between normalized titles for the duration-signal fallback
// to still emit a candidate. This is looser than the
// checkExactTitle threshold (3) because duration match is a
// strong physical-content signal — two files with near-
// identical length and a recognizably similar title are almost
// certainly the same book, even if the title formatting
// differs more than the exact-title check tolerates.
const durationLevenshteinMax = 6

// checkDurationMatch scans books by the same author for duration-
// based similarity signals. A strong duration match (±2%) combined
// with a recognizably similar title is a near-certain duplicate
// indicator — duration is one of the hardest physical signals to
// fake, and normal transcoding variance stays well under 2%.
//
// Emits candidates with layer="exact" when duration matches AND
// titles are within the relaxed Levenshtein threshold. Flags
// obvious abridged/unabridged edition pairs with a system tag on
// BOTH books so the user can filter them manually without merging.
//
// Backlog 1.2. Runs after checkExactTitle so the cheap/strict
// signal fires first; duration is the "I know these are the same
// book but the title encoding differs enough that the strict
// check missed it" fallback.
func (de *Engine) checkDurationMatch(book *database.Book) error {
	if book.AuthorID == nil {
		return nil
	}
	if book.Duration == nil || *book.Duration <= 0 {
		return nil
	}
	if !hasUsableTitle(book.Title) {
		return nil
	}

	others, err := de.bookStore.GetBooksByAuthorID(*book.AuthorID)
	if err != nil {
		return fmt.Errorf("get books by author: %w", err)
	}

	bookDur := float64(*book.Duration)
	bookNorm := normalizeTitle(book.Title)
	bookForms := de.allNormalizedTitleForms(book)

	for i := range others {
		other := &others[i]
		if other.ID == book.ID {
			continue
		}
		if other.Duration == nil || *other.Duration <= 0 {
			continue
		}
		if !hasUsableTitle(other.Title) {
			continue
		}

		otherDur := float64(*other.Duration)
		// Symmetric percent difference so order doesn't matter.
		diff := bookDur - otherDur
		if diff < 0 {
			diff = -diff
		}
		base := bookDur
		if otherDur > base {
			base = otherDur
		}
		pct := diff / base

		// Short-circuit: completely unrelated durations. 20% is a
		// generous upper bound — anything past that can't be the
		// same book content even in abridged form.
		if pct >= durationAbridgedThreshold {
			continue
		}

		otherForms := de.allNormalizedTitleForms(other)
		titleDist := minLevenshteinBetweenForms(bookForms, otherForms)
		otherNorm := normalizeTitle(other.Title)

		// Series-volume guard: same rejection as checkExactTitle.
		// If both books identify as distinct series volumes, don't
		// emit a candidate even when duration matches (a reread of
		// the series often has every volume at the same length).
		bookSeriesNum := seriesNumberOf(book)
		otherSeriesNum := seriesNumberOf(other)
		if bookSeriesNum != "" && otherSeriesNum != "" && bookSeriesNum != otherSeriesNum {
			continue
		}
		if titlesDifferOnlyInDigits(bookNorm, otherNorm) {
			continue
		}

		// Exact duration match (±2%) + recognizable title →
		// emit a merge candidate. The relaxed Levenshtein
		// threshold (6 vs 3 in checkExactTitle) is OK here
		// because duration is the strong signal.
		if pct <= durationMatchTolerance && titleDist <= durationLevenshteinMax {
			sim := 1.0
			if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
				EntityType: "book",
				EntityAID:  book.ID,
				EntityBID:  other.ID,
				Layer:      "exact",
				Similarity: &sim,
				Status:     "pending",
			}); err != nil {
				slog.Error("dedup duration candidate upsert error", "err", err)
				continue
			}
			// Tag both sides so users can filter "books the
			// dedup engine matched on duration signal".
			_ = database.EnsureSingletonBookTag(
				de.bookStore, book.ID, "dedup:duration-match", "dedup:duration-match", "system",
			)
			_ = database.EnsureSingletonBookTag(
				de.bookStore, other.ID, "dedup:duration-match", "dedup:duration-match", "system",
			)
			continue
		}

		// Duration mismatch 10-20% with same/near-same title is
		// almost always an abridged/unabridged edition pair.
		// Don't emit a merge candidate (they're legitimately
		// different content), but tag both sides so users can
		// filter and handle manually. The threshold starts at
		// 10% because normal transcoding noise ends around 2-5%
		// and 10% is safely above that.
		if pct >= 0.10 && titleDist <= durationLevenshteinMax {
			_ = database.EnsureSingletonBookTag(
				de.bookStore, book.ID, "dedup:duration-abridged", "dedup:duration-abridged", "system",
			)
			_ = database.EnsureSingletonBookTag(
				de.bookStore, other.ID, "dedup:duration-abridged", "dedup:duration-abridged", "system",
			)
		}
	}
	return nil
}

// hasUsableTitle reports whether a title string is meaningful enough to
// drive dedup decisions. Empty strings, whitespace-only strings, and
// extremely short strings (≤ 2 characters after trimming) are rejected
// — their embeddings collapse into a tiny region of the vector space
// where unrelated records spuriously hit 100% cosine similarity, and
// their Levenshtein distance against any other empty-ish title is 0.
func hasUsableTitle(title string) bool {
	trimmed := strings.TrimSpace(title)
	return len([]rune(trimmed)) > 2
}

// minPlausibleAudioBytes is the smallest file size we treat as a real audio
// file. Anything smaller is a placeholder/stub (a 32-byte .url shortcut, a
// 182-byte broken download) that must never anchor an exact-duplicate match.
const minPlausibleAudioBytes = 256 * 1024 // 256 KiB

// hasPlausibleAudio reports whether a book references real audio content rather
// than a stub or a never-scanned placeholder. A book qualifies if it has a
// positive duration OR a file size at/above the plausible-audio floor. This is
// the engine-side counterpart to the dataset missingFile catcher: it stops the
// exact-title / ISBN emitters from flagging "100% duplicate" when one side is a
// 32-byte stub or an unscanned shell. A large unscanned copy (real size, zero
// duration) still qualifies — it is a genuine duplicate, not garbage.
func hasPlausibleAudio(book *database.Book) bool {
	if book == nil {
		return false
	}
	if book.Duration != nil && *book.Duration > 0 {
		return true
	}
	if book.FileSize != nil && *book.FileSize >= minPlausibleAudioBytes {
		return true
	}
	return false
}

// seriesNumberOf returns a stable string representation of a book's
// series position, if one can be determined. It prefers the structured
// Book.SeriesSequence field; if that's unset it falls back to extracting
// a trailing book-number token from the title (handling patterns like
// "Title 3", "Title, Book 3", "Title (Book 3)", "Title #3"). Returns ""
// if no position can be determined.
func seriesNumberOf(book *database.Book) string {
	if book.SeriesSequence != nil {
		return strconv.Itoa(*book.SeriesSequence)
	}
	return extractSeriesNumberFromTitle(book.Title)
}

// seriesNumberInTitleRe matches an explicit book-volume marker followed by
// a number. Recognized markers (case-insensitive, optional trailing dot):
//
//	book, bk, volume, vol, number, no, part, pt, episode, ep, #
//
// Examples that match: "Reclaiming Honor bk 6", "Title, Book 3",
// "Title Vol. 12", "Title #4", "Title Ep 7", "title bk.3" (no space).
// The capture group is the digit portion only.
var seriesNumberInTitleRe = regexp.MustCompile(
	`(?i)(?:book|bk|volume|vol|number|no|part|pt|episode|ep|#)\.?\s*(\d+(?:\.\d+)?)`,
)

// extractSeriesNumberFromTitle looks for an explicit volume marker token
// anywhere in the title and returns the matched number as a string.
// Returns "" if none found. It only matches explicit book-volume markers —
// a bare number in a title ("1984") is not treated as a series position.
//
// This is the safety net that stops Layer 1 from merging "Reclaiming
// Honor bk 6" into "Reclaiming Honor bk 7" just because the normalized
// titles differ by two characters and slip under the Levenshtein
// threshold.
func extractSeriesNumberFromTitle(title string) string {
	m := seriesNumberInTitleRe.FindStringSubmatch(title)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// titlesDifferOnlyInDigits reports whether two titles have identical
// structure after stripping all digit characters. When true, the two
// titles are near-certainly two volumes of a series where the volume
// marker token isn't one the regex recognizes — e.g. "Title 3" vs
// "Title 4", "Series Name Six" vs "Series Name Seven" (no, that one
// has letter differences and returns false), "Reclaiming Honor abc6"
// vs "Reclaiming Honor abc7".
//
// The function is intentionally strict: both titles must match exactly
// after digit removal AND at least one digit must be present in each.
// This avoids false rejections for unrelated books with matching
// non-digit content (e.g. two books titled "Untitled" — no digits, so
// the function returns false and the pair is allowed through).
func titlesDifferOnlyInDigits(a, b string) bool {
	// Normalize: replace each digit with a space, then collapse runs of
	// whitespace, then trim. Replacing (rather than dropping) digits
	// avoids "title12" and "title" producing different strippeds when
	// one is "title12sub" and the other is "title sub" — both collapse
	// to "title sub" the right way. Collapsing whitespace handles
	// "title 2" -> "title " -> "title" vs plain "title".
	normalize := func(s string) string {
		var sb strings.Builder
		for _, r := range s {
			if r >= '0' && r <= '9' {
				sb.WriteRune(' ')
				continue
			}
			sb.WriteRune(r)
		}
		return strings.Join(strings.Fields(sb.String()), " ")
	}
	normA := normalize(a)
	normB := normalize(b)
	if normA != normB {
		return false
	}
	// Non-digit content is identical (ignoring whitespace differences
	// where digits used to be). For this to count as a series-volume
	// diff, the digit strings must differ. If both digit strings are
	// identical, it's the same title (e.g. both "Foundation 1" — same
	// book, not a series difference). If one side has no digits and the
	// other has a digit, that's a "Backyard Dungeon" vs "Backyard Dungeon
	// 2" pattern where the first book is volume 1 and the second is
	// volume 2 — a real series-volume diff.
	digitsA := extractDigits(a)
	digitsB := extractDigits(b)
	if digitsA == digitsB {
		return false
	}
	// At least one side must have a digit; otherwise both digit strings
	// are "" and the == check above would have caught it.
	return true
}

// extractDigits returns all digit characters from s concatenated in
// order. "Book 3 part 2" → "32".
func extractDigits(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// findSimilarBooks runs Layer 2 embedding similarity search for a book.
func (de *Engine) findSimilarBooks(ctx context.Context, bookID string) error {
	emb, err := de.embedStore.Get("book", bookID)
	if err != nil || emb == nil {
		return fmt.Errorf("no embedding for book %s", bookID)
	}

	// Load the query book once so we can consult its version_group_id, its
	// title, and its series position when filtering candidates. If the
	// lookup fails we proceed without the filter — skipping pairs is an
	// optimisation, not a correctness need, but a nil queryBook means we
	// must skip the version-group and series-volume checks below.
	queryBook, _ := de.bookStore.GetBookByID(bookID)

	// Guard against embeddings that should never have been created in the
	// first place. If the query book has an empty/near-empty title, its
	// embedding is noise and everything it matches will be garbage —
	// treat this as a no-op rather than creating candidates.
	if queryBook != nil && !hasUsableTitle(queryBook.Title) {
		return nil
	}

	querySeriesNum := ""
	if queryBook != nil {
		querySeriesNum = seriesNumberOf(queryBook)
	}

	var results []database.SimilarityResult
	if de.chromemStore != nil {
		filter := map[string]string{"is_primary_version": "true"}
		chromemResults, cErr := de.chromemStore.FindSimilar(ctx, "book", emb.Vector, 20, filter)
		if cErr != nil {
			return cErr
		}
		for _, cr := range chromemResults {
			if cr.Similarity >= float32(de.BookLowThreshold) {
				results = append(results, database.SimilarityResult{
					EntityID:   cr.EntityID,
					Similarity: cr.Similarity,
				})
			}
		}
	}
	// SQLite linear-scan fallback. Hit when chromem isn't wired at all,
	// OR when chromem is wired but empty (the DEDUP_CHROMEM_LAZY=true
	// path skips the eager HydrateChromem at startup to save ~6GB heap).
	// SQLite full-scan + cosine is ~50-200ms per query for 42K books vs
	// chromem's <10ms; dedup queries are rare so the tradeoff is fine.
	if len(results) == 0 && de.embedStore != nil {
		fallback, fErr := de.embedStore.FindSimilar("book", emb.Vector, float32(de.BookLowThreshold), 20)
		if fErr != nil {
			return fErr
		}
		results = fallback
	}

	for _, r := range results {
		if r.EntityID == bookID {
			continue
		}
		otherBook, _ := de.bookStore.GetBookByID(r.EntityID)
		if otherBook == nil {
			// Other book no longer exists — skip the candidate.
			continue
		}
		// Drop candidates with no usable title on the other side. Their
		// embedding is noise (same reason as the query-side guard above).
		if !hasUsableTitle(otherBook.Title) {
			continue
		}
		// Drop candidates that are already siblings in the same version
		// group. The version-group system already knows these are the same
		// logical book in different formats, so surfacing them as dedup
		// candidates is just noise.
		if queryBook != nil && queryBook.VersionGroupID != nil && *queryBook.VersionGroupID != "" &&
			otherBook.VersionGroupID != nil &&
			*otherBook.VersionGroupID == *queryBook.VersionGroupID {
			continue
		}
		// Drop candidates that are distinct volumes of a numbered series.
		// Embeddings cannot distinguish "Book 3" from "Book 4" well because
		// the titles are 99% identical — we have to filter these out by
		// structured metadata. The explicit marker check is the primary
		// signal (Book.SeriesSequence or a "bk"/"book"/"vol"/"#" token in
		// the title); the digit-structure check is the fallback for titles
		// like "Series Name 3" that have no explicit marker.
		if querySeriesNum != "" {
			if otherSeriesNum := seriesNumberOf(otherBook); otherSeriesNum != "" && otherSeriesNum != querySeriesNum {
				continue
			}
		}
		if queryBook != nil && titlesDifferOnlyInDigits(normalizeTitle(queryBook.Title), normalizeTitle(otherBook.Title)) {
			continue
		}
		// Drop candidates where both books share the same parent directory.
		// Multi-file audiobooks split into chapters (011.mp3, 062.mp3, …)
		// stored in the same folder produce identical text embeddings, causing
		// 100% similarity scores between sibling chapter-books that are clearly
		// not duplicates of each other.
		if queryBook != nil && queryBook.FilePath != "" && otherBook.FilePath != "" &&
			filepath.Dir(queryBook.FilePath) == filepath.Dir(otherBook.FilePath) {
			continue
		}
		sim := float64(r.Similarity)
		if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "book",
			EntityAID:  bookID,
			EntityBID:  r.EntityID,
			Layer:      "embedding",
			Similarity: &sim,
			Status:     "pending",
		}); err != nil {
			slog.Error("dedup upsert embedding candidate error", "err", err)
		}
	}
	return nil
}

// CheckAuthor runs Layer 2 embedding similarity for an author.
func (de *Engine) CheckAuthor(ctx context.Context, authorID int) error {
	author, err := de.bookStore.GetAuthorByID(authorID)
	if err != nil {
		return fmt.Errorf("get author %d: %w", authorID, err)
	}
	if author == nil {
		return fmt.Errorf("author %d not found", authorID)
	}

	if de.embedClient != nil {
		if err := de.EmbedAuthor(ctx, authorID); err != nil {
			return fmt.Errorf("embed author: %w", err)
		}
	}

	entityID := strconv.Itoa(authorID)
	emb, err := de.embedStore.Get("author", entityID)
	if err != nil || emb == nil {
		return nil // no embedding, nothing to compare
	}

	var results []database.SimilarityResult
	if de.chromemStore != nil {
		chromemResults, cErr := de.chromemStore.FindSimilar(ctx, "author", emb.Vector, 20, nil)
		if cErr != nil {
			return cErr
		}
		for _, cr := range chromemResults {
			if cr.Similarity >= float32(de.AuthorLowThreshold) {
				results = append(results, database.SimilarityResult{EntityID: cr.EntityID, Similarity: cr.Similarity})
			}
		}
	}
	// SQLite linear-scan fallback. Hit when chromem isn't wired, or when
	// chromem is wired-but-empty under DEDUP_CHROMEM_LAZY=true. See the
	// matching block in CheckBook above for the tradeoff rationale.
	if len(results) == 0 && de.embedStore != nil {
		fallback, fErr := de.embedStore.FindSimilar("author", emb.Vector, float32(de.AuthorLowThreshold), 20)
		if fErr != nil {
			return fErr
		}
		results = fallback
	}

	for _, r := range results {
		if r.EntityID == entityID {
			continue
		}
		sim := float64(r.Similarity)
		if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "author",
			EntityAID:  entityID,
			EntityBID:  r.EntityID,
			Layer:      "embedding",
			Similarity: &sim,
			Status:     "pending",
		}); err != nil {
			slog.Error("dedup upsert author candidate error", "err", err)
		}
	}
	return nil
}

// EmbedStatus classifies the outcome of a single EmbedBook call so callers
// can count live API usage separately from no-op traversals. The log line
// in runEmbeddingBackfill used to say "N books embedded" for every
// successful return regardless of what actually happened, which made the
// backfill's real cost invisible. With this enum a caller can report:
//
//	Embedded N (cached M, skipped_non_primary P, skipped_empty_title Q)
//
// making it obvious at a glance whether a run actually called the
// embeddings API or just walked the library to validate state.
type EmbedStatus int

const (
	// EmbedStatusEmbedded means the embeddings API was called and the
	// resulting vector was written to the store. This is the only status
	// that costs money.
	EmbedStatusEmbedded EmbedStatus = iota

	// EmbedStatusCached means the book already had an embedding whose
	// text_hash matched the current title/author/narrator, so no API
	// call was made and no row was written. On re-runs of an unchanged
	// library almost every book lands here.
	EmbedStatusCached

	// EmbedStatusSkippedNonPrimary means the book is a non-primary
	// member of a version group (alternate format of another book).
	// Its identity is owned by the primary, so it gets no embedding.
	// Any stale row for the book is deleted on the way out.
	EmbedStatusSkippedNonPrimary

	// EmbedStatusSkippedEmptyTitle means the book has no usable title
	// (empty, whitespace-only, or ≤ 2 characters after trimming).
	// Embedding such a book would collapse into a dense cluster where
	// unrelated records spuriously match at ~100% cosine. Any stale row
	// for the book is deleted on the way out.
	EmbedStatusSkippedEmptyTitle
)

// String returns a short human-readable form of the status, suitable for
// log output.
func (s EmbedStatus) String() string {
	switch s {
	case EmbedStatusEmbedded:
		return "embedded"
	case EmbedStatusCached:
		return "cached"
	case EmbedStatusSkippedNonPrimary:
		return "skipped_non_primary"
	case EmbedStatusSkippedEmptyTitle:
		return "skipped_empty_title"
	default:
		return "unknown"
	}
}

// EmbedBook generates and stores an embedding for the given book.
// Returns a status classifying what actually happened so callers can
// distinguish live API calls from cache hits and skipped-by-policy books.
//
// Non-primary versions (members of a version group that are not the primary
// representative) are skipped entirely: their embedding would be a duplicate
// of the primary's by construction, and surfacing them as dedup candidates
// just clutters the UI with noise. Any existing embedding for a non-primary
// book is deleted on the spot so historical rows from earlier backfills get
// cleaned up as we walk the library. Empty-title books are skipped with
// the same cleanup behavior.
func (de *Engine) EmbedBook(ctx context.Context, bookID string) (EmbedStatus, error) {
	results, err := de.EmbedBooks(ctx, []string{bookID})
	if err != nil {
		return 0, err
	}
	return results[bookID], nil
}

// prepBookEmbed runs the per-book skip-checks and builds the embedding text +
// hash. Returns terminal=true when a final EmbedStatus has been determined
// (skip cases or pre-existing cache hit by hash) so the caller can record the
// result without further work; terminal=false means the book needs a fresh
// embed and (text, hash) are valid.
//
// Extracted from the old EmbedBook so the per-book prep is shared between
// EmbedBook (single) and EmbedBooks (batched). The mirror-to-chromem call on
// the cached path happens here because it's free regardless of which caller
// invoked us.
func (de *Engine) prepBookEmbed(ctx context.Context, bookID string) (
	book *database.Book, text string, hash string, status EmbedStatus, terminal bool, err error,
) {
	book, err = de.bookStore.GetBookByID(bookID)
	if err != nil {
		return nil, "", "", 0, true, fmt.Errorf("get book %s: %w", bookID, err)
	}
	if book == nil {
		return nil, "", "", 0, true, fmt.Errorf("book %s not found", bookID)
	}

	if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
		if delErr := de.embedStore.Delete("book", bookID); delErr != nil {
			slog.Info("dedup delete stale embedding for non-primary", "bookID", bookID, "delErr", delErr)
		}
		de.deleteBookFromChromem(ctx, bookID)
		return book, "", "", EmbedStatusSkippedNonPrimary, true, nil
	}

	if !hasUsableTitle(book.Title) {
		if delErr := de.embedStore.Delete("book", bookID); delErr != nil {
			slog.Info("dedup delete stale embedding for empty-title", "bookID", bookID, "delErr", delErr)
		}
		de.deleteBookFromChromem(ctx, bookID)
		return book, "", "", EmbedStatusSkippedEmptyTitle, true, nil
	}

	authorName := ""
	if book.AuthorID != nil {
		if author, lookupErr := de.bookStore.GetAuthorByID(*book.AuthorID); lookupErr == nil && author != nil {
			authorName = author.Name
		}
	}
	seriesName := ""
	if book.SeriesID != nil {
		if series, lookupErr := de.bookStore.GetSeriesByID(*book.SeriesID); lookupErr == nil && series != nil {
			seriesName = series.Name
		}
	}
	seriesSeq := seriesNumberOf(book)

	text = ai.BuildBookEmbeddingText(book.Title, authorName, derefStr(book.Narrator), seriesName, seriesSeq)
	hash = ai.TextHash(text)

	existing, getErr := de.embedStore.Get("book", bookID)
	if getErr == nil && existing != nil && existing.TextHash == hash {
		de.mirrorBookToChromem(ctx, book, existing.Vector)
		return book, text, hash, EmbedStatusCached, true, nil
	}
	return book, text, hash, 0, false, nil
}

// EmbedBooks generates embeddings for the given book IDs in a single API call
// per chunk, instead of one call per book. Returns a status map keyed by book
// ID describing what happened (Embedded / Cached / SkippedNonPrimary /
// SkippedEmptyTitle). Per-book errors are logged and the book is omitted from
// the result map; the call only returns an error for whole-batch failures
// (API call, missing client).
//
// Why batch: text-embedding-3-large bills per request and per token. Calling
// it once with N inputs costs roughly the same as calling it once with 1
// input — token volume dominates, request overhead does not. Sending books
// one at a time across a 10K-book FullScan therefore wasted ~10K request
// round-trips and ~10× the wall time vs. batched calls, with no cost benefit.
func (de *Engine) EmbedBooks(ctx context.Context, bookIDs []string) (map[string]EmbedStatus, error) {
	if de.embedClient == nil {
		return nil, fmt.Errorf("no embedding client configured")
	}
	results := make(map[string]EmbedStatus, len(bookIDs))
	if len(bookIDs) == 0 {
		return results, nil
	}

	type pending struct {
		id   string
		book *database.Book
		text string
		hash string
	}
	var todo []pending
	for _, id := range bookIDs {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		book, text, hash, status, terminal, err := de.prepBookEmbed(ctx, id)
		if err != nil {
			slog.Info("dedup prep embed for", "id", id, "err", err)
			continue
		}
		if terminal {
			results[id] = status
			continue
		}
		todo = append(todo, pending{id: id, book: book, text: text, hash: hash})
	}

	if len(todo) == 0 {
		return results, nil
	}

	texts := make([]string, len(todo))
	for i, p := range todo {
		texts[i] = p.text
	}

	vecs, err := de.embedClient.EmbedBatch(ctx, texts)
	if err != nil {
		return results, fmt.Errorf("embed batch (%d books): %w", len(todo), err)
	}
	if len(vecs) != len(todo) {
		return results, fmt.Errorf("embed batch returned %d vectors for %d inputs", len(vecs), len(todo))
	}

	for i, p := range todo {
		if upErr := de.embedStore.Upsert(database.Embedding{
			EntityType: "book",
			EntityID:   p.id,
			TextHash:   p.hash,
			Vector:     vecs[i],
			Model:      "text-embedding-3-large",
		}); upErr != nil {
			slog.Info("dedup upsert embedding for", "p", p.id, "upErr", upErr)
			continue
		}
		de.mirrorBookToChromem(ctx, p.book, vecs[i])
		results[p.id] = EmbedStatusEmbedded
	}
	return results, nil
}

// mirrorBookToChromem writes the book's vector + filter metadata to the
// chromem ANN store. Without this mirror the chromem index drifts out of
// sync with the SQLite embeddings table — new embeds aren't indexed, and
// the dedup engine queries an empty (or stale) ANN store and either finds
// nothing or returns wrong matches.
//
// Best-effort: chromem write failures are logged and dropped. The SQLite
// embedding row is the source of truth; chromem is just an index.
func (de *Engine) mirrorBookToChromem(ctx context.Context, book *database.Book, vec []float32) {
	if de.chromemStore == nil || book == nil || len(vec) == 0 {
		return
	}
	primary := "true"
	if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
		primary = "false"
	}
	meta := map[string]string{
		"is_primary_version": primary,
	}
	if book.SeriesID != nil {
		meta["series_id"] = strconv.Itoa(*book.SeriesID)
	}
	if seq := seriesNumberOf(book); seq != "" {
		meta["series_sequence"] = seq
	}
	if err := de.chromemStore.Upsert(ctx, "book", book.ID, vec, meta); err != nil {
		slog.Warn("dedup chromem upsert book", "book", book.ID, "err", err)
	}
}

// EmbedAuthor generates and stores an embedding for the given author.
func (de *Engine) EmbedAuthor(ctx context.Context, authorID int) error {
	if de.embedClient == nil {
		return fmt.Errorf("no embedding client configured")
	}

	author, err := de.bookStore.GetAuthorByID(authorID)
	if err != nil {
		return fmt.Errorf("get author %d: %w", authorID, err)
	}
	if author == nil {
		return fmt.Errorf("author %d not found", authorID)
	}

	text := ai.BuildEmbeddingText("author", author.Name, "", "")
	hash := ai.TextHash(text)
	entityID := strconv.Itoa(authorID)

	existing, err := de.embedStore.Get("author", entityID)
	if err == nil && existing != nil && existing.TextHash == hash {
		// Mirror to chromem on cache hits too — see mirrorBookToChromem
		// for the rationale (keeps ANN index in sync with sqlite).
		de.mirrorAuthorToChromem(ctx, entityID, existing.Vector)
		return nil
	}

	vec, err := de.embedClient.EmbedOne(ctx, text)
	if err != nil {
		return fmt.Errorf("embed text: %w", err)
	}

	if err := de.embedStore.Upsert(database.Embedding{
		EntityType: "author",
		EntityID:   entityID,
		TextHash:   hash,
		Vector:     vec,
		Model:      "text-embedding-3-large",
	}); err != nil {
		return err
	}
	de.mirrorAuthorToChromem(ctx, entityID, vec)
	return nil
}

// EmbedBooksAsync submits all primary books that lack a current embedding to
// the OpenAI Batch API (/v1/embeddings) for offline processing. The batch
// completes within 24 hours; results are ingested by BatchPoller when the
// "embed_async" batch type completes.
//
// Returns the OpenAI batch ID and the number of books submitted. Returns an
// empty batchID (and count=0, err=nil) when all books are already embedded.
func (de *Engine) EmbedBooksAsync(ctx context.Context) (batchID string, count int, err error) {
	if de.embedClient == nil {
		return "", 0, fmt.Errorf("no embedding client configured")
	}

	books, err := de.getAllBooks()
	if err != nil {
		return "", 0, fmt.Errorf("load books: %w", err)
	}

	var items []ai.EmbedBatchItem
	for _, book := range books {
		if !hasUsableTitle(book.Title) {
			continue
		}
		authorName := ""
		if book.AuthorID != nil {
			if a, lookupErr := de.bookStore.GetAuthorByID(*book.AuthorID); lookupErr == nil && a != nil {
				authorName = a.Name
			}
		}
		seriesName := ""
		if book.SeriesID != nil {
			if s, lookupErr := de.bookStore.GetSeriesByID(*book.SeriesID); lookupErr == nil && s != nil {
				seriesName = s.Name
			}
		}
		text := ai.BuildBookEmbeddingText(book.Title, authorName, derefStr(book.Narrator), seriesName, seriesNumberOf(&book))
		hash := ai.TextHash(text)

		// Skip books that already have a current embedding.
		existing, getErr := de.embedStore.Get("book", book.ID)
		if getErr == nil && existing != nil && existing.TextHash == hash {
			continue
		}
		items = append(items, ai.EmbedBatchItem{BookID: book.ID, Text: text})
	}

	if len(items) == 0 {
		return "", 0, nil
	}

	id, err := de.embedClient.CreateEmbeddingBatch(ctx, items)
	if err != nil {
		return "", 0, fmt.Errorf("submit embedding batch: %w", err)
	}
	slog.Info("dedup submitted async embedding batch for books", "id", id, "items_count", len(items))
	return id, len(items), nil
}

// mirrorAuthorToChromem writes an author embedding to the chromem index.
// Best-effort; see mirrorBookToChromem for rationale.
func (de *Engine) mirrorAuthorToChromem(ctx context.Context, authorID string, vec []float32) {
	if de.chromemStore == nil || authorID == "" || len(vec) == 0 {
		return
	}
	if err := de.chromemStore.Upsert(ctx, "author", authorID, vec, nil); err != nil {
		slog.Warn("dedup chromem upsert author", "authorID", authorID, "err", err)
	}
}

// deleteBookFromChromem removes a book entry from the chromem ANN index.
// Called whenever EmbedBook deletes the SQLite embedding row (non-primary
// version, empty title, etc.) so the chromem index doesn't keep returning
// stale matches for a book that should no longer participate in dedup.
//
// chromem-go's Delete returns nil for missing IDs, so this is safe to call
// even when the entry doesn't exist.
func (de *Engine) deleteBookFromChromem(ctx context.Context, bookID string) {
	if de.chromemStore == nil || bookID == "" {
		return
	}
	if err := de.chromemStore.Delete(ctx, "book", bookID); err != nil {
		slog.Warn("dedup chromem delete book", "bookID", bookID, "err", err)
	}
}

// FullScan re-embeds stale entities and runs both Layer 1 (exact) and
// Layer 2 (embedding) dedup checks for every primary book in the library.
// The progress callback is invoked periodically with (done, total).
//
// Layer 1 used to only run on ingest and metadata-apply events, which meant
// the `exact` bucket stayed at zero for libraries that hadn't seen new books
// since the initial backfill. Running it inside FullScan populates the
// bucket with the hash/ISBN/near-title-match candidates that were always
// there but never surfaced.
func (de *Engine) FullScan(ctx context.Context, progress func(done, total int)) error {
	_, span := dedupTracer.Start(ctx, "dedup.full_scan")
	defer span.End()

	books, err := de.getAllBooks()
	if err != nil {
		err := fmt.Errorf("get all books: %w", err)
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return err
	}

	// Embedding chunk size — picked so the OpenAI request comfortably fits
	// under the 2048-input / 300K-token caps for text-embedding-3-large
	// while still amortising request overhead. Empirically, batches of
	// ~64 cut FullScan wall time and request count by an order of
	// magnitude vs. the previous one-call-per-book loop.
	const embedChunkSize = 64

	// Circuit-breaker state: if the embedding backend fails consecutively
	// (network unreachable, quota exhausted, etc.) we give up on Layer 2 for
	// the rest of this scan rather than hanging/retrying ~780 times.
	// embeddingsGaveUp is set after embedFailThreshold consecutive failures.
	const embedFailThreshold = 3
	var (
		embedConsecutiveFails int
		embeddingsGaveUp      bool
	)

	total := len(books)
	span.SetAttributes(attribute.Int("total_books", total))
	chunkIDs := make([]string, 0, embedChunkSize)
	chunkStart := 0

	// flushChunk sends the accumulated book IDs to EmbedBooks and then runs
	// findSimilarBooks for each successfully embedded book. It now returns the
	// EmbedBooks error (if any) so the circuit-breaker above can count
	// consecutive failures; chunkIDs is always reset so we advance past the
	// failed chunk regardless.
	flushChunk := func(endIdx int) error {
		if len(chunkIDs) == 0 {
			return nil
		}
		statuses, err := de.EmbedBooks(ctx, chunkIDs)
		if err != nil {
			slog.Error("dedup full scan embed batch error", "chunkStart", chunkStart, "chunkIDs_count", len(chunkIDs), "err", err)
		}
		for _, id := range chunkIDs {
			st, ok := statuses[id]
			if !ok {
				continue
			}
			if st == EmbedStatusEmbedded || st == EmbedStatusCached {
				if simErr := de.findSimilarBooks(ctx, id); simErr != nil {
					slog.Error("dedup full scan similarity error for", "id", id, "simErr", simErr)
				}
			}
		}
		chunkIDs = chunkIDs[:0]
		chunkStart = endIdx
		return err // return the EmbedBooks error for circuit-breaker accounting
	}

	for i, book := range books {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Resolve author name once — used by Layer 1 title check below.
		authorName := ""
		if book.AuthorID != nil {
			if author, err := de.bookStore.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
				authorName = author.Name
			}
		}

		// Layer 1 exact checks (file hash, ISBN/ASIN, near-identical title,
		// duration match). Cheap and synchronous, no API calls — runs
		// inline regardless of embed batching or the circuit-breaker state.
		if _, err := de.checkExactFileHash(&book, authorName); err != nil {
			slog.Error("dedup full scan hash check error for", "book", book.ID, "err", err)
		}
		if err := de.checkExactISBN(&book); err != nil {
			slog.Error("dedup full scan ISBN check error for", "book", book.ID, "err", err)
		}
		if err := de.checkExactTitle(&book, authorName); err != nil {
			slog.Error("dedup full scan title check error for", "book", book.ID, "err", err)
		}
		if err := de.checkDurationMatch(&book); err != nil {
			slog.Error("dedup full scan duration check error for", "book", book.ID, "err", err)
		}

		// Layer 2 embedding: accumulate IDs and flush in batches to keep
		// OpenAI calls coalesced. findSimilarBooks runs after each batch
		// flush so similarity work overlaps with the embedding work for
		// the next chunk in flight.
		//
		// Guarded by embeddingsEnabled() (client present + config flag) and
		// the circuit-breaker flag. When the flag is false from the start,
		// no API calls are made at all. When the breaker trips mid-scan,
		// remaining chunks are skipped; Layer 1 candidates already written
		// are unaffected and the scan completes normally.
		if de.embeddingsEnabled() && !embeddingsGaveUp {
			chunkIDs = append(chunkIDs, book.ID)
			if len(chunkIDs) >= embedChunkSize {
				if err := flushChunk(i + 1); err != nil {
					embedConsecutiveFails++
					if embedConsecutiveFails >= embedFailThreshold {
						embeddingsGaveUp = true
						slog.Warn("dedup full-scan: embeddings unavailable after consecutive failures — completing scan with Layer-1 + signature only",
							"consecutive_failures", embedConsecutiveFails,
							"books_processed_so_far", i+1,
							"total_books", total,
						)
					}
				} else {
					embedConsecutiveFails = 0
				}
			}
		}

		if progress != nil && (i%10 == 0 || i == total-1) {
			progress(i+1, total)
		}
	}

	// Final partial chunk — only if embeddings are still active.
	if de.embeddingsEnabled() && !embeddingsGaveUp {
		if err := flushChunk(total); err != nil {
			slog.Error("dedup full scan final embed chunk error", "err", err)
		}
	}

	// --- Unified composite scoring (T014) ---
	// Second pass over all books: for each book, compose a unified score from
	// all signals emitted by the Layer 1 + Layer 2 passes above and persist
	// ScoreBreakdown/Band/FormulaVersion alongside the existing
	// Layer/Similarity fields for backward compat.
	//
	// WHY a second pass: the embedding batching in flushChunk means that
	// findSimilarBooks (which writes the embedding candidate rows consumed by
	// runUnifiedScoringForBook) may not have run yet for a given book when we
	// are still iterating the book loop above.  Running the scoring pass after
	// flushChunk(total) guarantees all embedding candidates are written.
	for _, book := range books {
		if ctx.Err() != nil {
			break
		}
		authorName := ""
		if book.AuthorID != nil {
			if author, err := de.bookStore.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
				authorName = author.Name
			}
		}
		if err := de.runUnifiedScoringForBook(ctx, &book, authorName); err != nil {
			slog.Error("dedup full scan unified scoring error for", "book", book.ID, "err", err)
		}
	}

	return nil
}

// PurgeStaleCandidates deletes book-dedup-candidate rows that are no longer
// meaningful under the current rules:
//
//   - either side references a non-primary version-group member (their
//     identity is owned by their group's primary, not the row itself)
//   - both sides belong to the same version_group_id (the version-group
//     system already knows they are the same logical book)
//
// Returns the number of rows deleted. Intended to be called once at startup
// after the backfill completes and again at the start of a user-triggered
// Re-scan so the candidate table stays clean after the Layer 1 + Layer 2
// rules tighten.
// CleanupCandidatesAfterMerge marks dedup candidate rows referencing any of
// the given merged-away book IDs as status="merged" so the candidates UI drops
// them on its next refresh. Without this, candidate Y comparing book B vs book
// C remains pending after book B was collapsed into book A by a separate
// candidate row — clicking Merge on Y then 500s ("book not found"), the
// 409-hotfix path in PR #1160 notwithstanding.
//
// Best-effort: per-ID errors are logged, not returned. Returns total rows
// transitioned across all IDs.
func (de *Engine) CleanupCandidatesAfterMerge(mergedAwayBookIDs []string) int {
	if de == nil || de.embedStore == nil || len(mergedAwayBookIDs) == 0 {
		return 0
	}
	total := 0
	for _, id := range mergedAwayBookIDs {
		if id == "" {
			continue
		}
		n, err := de.embedStore.MarkCandidatesAsMergedForEntity("book", id)
		if err != nil {
			slog.Warn("dedup cleanup candidates after merge", "book_id", id, "err", err)
			continue
		}
		total += n
	}
	if total > 0 {
		slog.Info("dedup cleanup candidates after merge",
			"merged_away_count", len(mergedAwayBookIDs),
			"orphan_candidates_marked_merged", total,
		)
	}
	return total
}

// RescoreResult is returned by Engine.Rescore. It summarises how many
// candidates changed band (per-band breakdown), how many were skipped
// (no stored signals, i.e. pre-unified-pipeline rows), and whether the
// changes were persisted (apply=true) or are dry-run only.
type RescoreResult struct {
	// Inspected is the total number of pending candidates examined.
	Inspected int `json:"inspected"`
	// Skipped is the count of candidates with no stored signal set
	// (pre-T015 rows; re-scoring without signals is impossible).
	Skipped int `json:"skipped"`
	// Changed is the count of candidates whose band or score changed.
	Changed int `json:"changed"`
	// Applied is true when changes were written back to the store.
	Applied bool `json:"applied"`
	// BandDeltas maps old_band→new_band to occurrence count.
	// Key format: "<old>→<new>" (e.g. "HIGH→CERTAIN").
	BandDeltas map[string]int `json:"band_deltas,omitempty"`
}

// Rescore re-runs unified.ComposeScore over the stored signal set of every
// pending candidate and returns a summary of band changes.
//
// "Stored signal sets only" means no re-collection or re-embedding: only
// candidates that have a non-nil ScoreBreakdown (i.e., were scored by the
// T015+ unified pipeline) are eligible. Pre-unified-pipeline rows are
// counted as Skipped.
//
// By default (apply=false) this is a dry-run: scores are computed but NOT
// written back to the store. Set apply=true to persist new scores and bands;
// this is the equivalent of "re-calibrate in production". The T016 HTTP
// handler exposes this via the {"apply": true} body pattern already used by
// emb-reencode and purge-legacy-fp.
func (de *Engine) Rescore(ctx context.Context, apply bool) (RescoreResult, error) {
	if de.embedStore == nil {
		return RescoreResult{}, nil
	}

	candidates, _, err := de.embedStore.ListCandidates(database.CandidateFilter{
		Status: "pending",
		Limit:  100000,
	})
	if err != nil {
		return RescoreResult{}, fmt.Errorf("rescore: list candidates: %w", err)
	}

	cfg := de.getScoreConfig()
	result := RescoreResult{
		Applied:    apply,
		BandDeltas: make(map[string]int),
	}

	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Inspected++
		if cand.ScoreBreakdown == nil || len(cand.ScoreBreakdown.Signals) == 0 {
			result.Skipped++
			continue
		}
		sb := cand.ScoreBreakdown
		newScore := unified.ComposeScore(sb.Signals, sb.Suppressors, cfg, sb.Pair)
		if newScore.Band == cand.Band && newScore.Score == sb.Score {
			continue // no change
		}
		result.Changed++
		deltaKey := cand.Band + "→" + newScore.Band
		result.BandDeltas[deltaKey]++

		if apply {
			if err := de.embedStore.UpdateCandidateScore(cand.ID, &newScore, newScore.Band, newScore.Formula); err != nil {
				slog.Warn("dedup rescore: update candidate score",
					"candidate_id", cand.ID,
					"err", err,
				)
			}
		}
	}

	slog.Info("dedup rescore",
		"inspected", result.Inspected,
		"skipped", result.Skipped,
		"changed", result.Changed,
		"applied", result.Applied,
	)
	return result, nil
}

func (de *Engine) PurgeStaleCandidates(ctx context.Context) (int, error) {
	_, span := dedupTracer.Start(ctx, "dedup.purge_stale_candidates")
	defer span.End()

	if de.embedStore == nil || de.bookStore == nil {
		span.SetAttributes(attribute.Bool("no_stores", true))
		return 0, nil
	}

	// First, canonicalize existing rows so duplicate-direction pairs
	// collapse into a single logical row. This has to run BEFORE the
	// stale-rule sweep below because otherwise we'd list the same pair
	// twice (once as (A,B), once as (B,A)) and maybe delete one copy
	// based on one rule and leave the other copy to cause confusion.
	if rewritten, deleted, err := de.embedStore.CanonicalizeCandidates(); err != nil {
		slog.Info("dedup canonicalize candidates", "err", err)
	} else if rewritten > 0 || deleted > 0 {
		slog.Info("dedup canonicalized candidate pair(s), deleted duplicate(s)", "rewritten", rewritten, "deleted", deleted)
	}

	// CRITICAL: Only purge PENDING candidates. Merged and dismissed rows
	// are historical records the user explicitly wants to keep — they're
	// what populates the Merged / Dismissed tabs and lets the user see
	// what they've already actioned. Without this filter, every rescan
	// would delete every previously-merged candidate (because merged
	// books share a version_group_id, which is one of the stale-rule
	// conditions below), making the Merged tab useless.
	candidates, _, err := de.embedStore.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      100000,
	})
	if err != nil {
		err := fmt.Errorf("list candidates: %w", err)
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return 0, err
	}

	// Memoise book lookups so a book referenced by many candidates is only
	// fetched once per purge run.
	type bookMeta struct {
		isNonPrimary   bool
		emptyTitle     bool
		versionGroupID string
		seriesNumber   string
		normTitle      string
		filePath       string
		missing        bool
	}
	cache := make(map[string]bookMeta, len(candidates)*2)
	lookup := func(id string) bookMeta {
		if m, ok := cache[id]; ok {
			return m
		}
		b, err := de.bookStore.GetBookByID(id)
		m := bookMeta{}
		if err != nil || b == nil {
			m.missing = true
			cache[id] = m
			return m
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			m.isNonPrimary = true
		}
		if !hasUsableTitle(b.Title) {
			m.emptyTitle = true
		}
		if b.VersionGroupID != nil {
			m.versionGroupID = *b.VersionGroupID
		}
		m.seriesNumber = seriesNumberOf(b)
		m.normTitle = normalizeTitle(b.Title)
		m.filePath = b.FilePath
		cache[id] = m
		return m
	}

	deleted := 0
	for _, c := range candidates {
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		default:
		}

		a := lookup(c.EntityAID)
		b := lookup(c.EntityBID)

		stale := false
		switch {
		case a.missing || b.missing:
			// One side no longer exists — the candidate can't be actioned.
			stale = true
		case a.isNonPrimary || b.isNonPrimary:
			stale = true
		case a.emptyTitle || b.emptyTitle:
			// One or both books have no usable title — their embedding
			// was garbage and any similarity match they produced is noise.
			stale = true
		case a.versionGroupID != "" && a.versionGroupID == b.versionGroupID:
			stale = true
		case a.seriesNumber != "" && b.seriesNumber != "" && a.seriesNumber != b.seriesNumber:
			// Distinct volumes of a numbered series (detected via a
			// structured field or an explicit "book/bk/vol/#" marker).
			stale = true
		case titlesDifferOnlyInDigits(a.normTitle, b.normTitle):
			// Fallback: normalized titles differ only in digit content,
			// meaning they're different volumes of a series whose marker
			// the explicit regex didn't match. "Reclaiming Honor bk 6"
			// vs "Reclaiming Honor bk 7" is the canonical example.
			stale = true
		case a.filePath != "" && b.filePath != "" && filepath.Dir(a.filePath) == filepath.Dir(b.filePath):
			// Both books reside in the same directory — they are chapter-files
			// of the same multi-file audiobook, not independent duplicates.
			stale = true
		}
		if !stale {
			continue
		}
		if err := de.embedStore.DeleteCandidate(c.ID); err != nil {
			slog.Info("dedup purge stale candidate", "c", c.ID, "err", err)
			continue
		}
		deleted++
	}
	span.SetAttributes(attribute.Int("candidates_deleted", deleted))
	return deleted, nil
}

// getAllBooks fetches all PRIMARY-version books in a single pass.
// Non-primary version-group members are filtered out so FullScan never
// processes them (their identity is owned by the primary) and similarity
// scanning only produces primary-vs-primary candidate pairs.
//
// Previously this used batched pagination (500 per page, ~48 batches for
// a 24K-book library). Each batch re-opened a Pebble iterator and walked
// from the start to the target offset before yielding results, producing
// O(n²) iterator ops per FullScan — ~576K reads for a 24K-book library.
// Passing limit=0 tells the store to return everything in one iteration,
// which is O(n). The memory cost is ~50MB for 24K Book structs — same
// order of magnitude as the old cumulative batch allocation and far
// cheaper than the wasted CPU on re-walked iterators.
func (de *Engine) getAllBooks() ([]database.Book, error) {
	batch, err := de.bookStore.GetAllBooks(0, 0)
	if err != nil {
		return nil, err
	}
	filtered := make([]database.Book, 0, len(batch))
	for _, b := range batch {
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered, nil
}

// RunLLMReview processes ambiguous candidates through LLM review (Layer 3).
// Pending book candidates whose similarity falls in [LLMBookLow, LLMBookHigh] and
// pending author candidates in [LLMAuthorLow, LLMAuthorHigh] are fetched, enriched
// with entity metadata, batched, and sent to the OpenAI chat LLM. The verdict is
// persisted via UpdateCandidateLLM (which also sets layer='llm').
//
// Candidates that are already at layer='llm' are skipped — rerunning is cheap in
// bookkeeping but expensive in API calls, so callers should use UpsertCandidate to
// clear the layer back to 'embedding' if they want a re-review.
func (de *Engine) RunLLMReview(ctx context.Context) error {
	if de.llmParser == nil || !de.llmParser.IsEnabled() {
		slog.Info("dedup LLM review skipped — llmParser not configured")
		return nil
	}
	if de.embedStore == nil {
		return fmt.Errorf("dedup: LLM review requires embedStore")
	}

	bookCandidates, err := de.listAmbiguousCandidates("book", de.LLMBookLow, de.LLMBookHigh)
	if err != nil {
		return fmt.Errorf("list ambiguous book candidates: %w", err)
	}
	authorCandidates, err := de.listAmbiguousCandidates("author", de.LLMAuthorLow, de.LLMAuthorHigh)
	if err != nil {
		return fmt.Errorf("list ambiguous author candidates: %w", err)
	}

	allCandidates := append(bookCandidates, authorCandidates...)
	if de.LLMMaxPairsPerRun > 0 && len(allCandidates) > de.LLMMaxPairsPerRun {
		allCandidates = allCandidates[:de.LLMMaxPairsPerRun]
	}
	if len(allCandidates) == 0 {
		slog.Info("dedup LLM review found no pending ambiguous candidates")
		return nil
	}
	slog.Info("dedup LLM review starting — pair(s) queued", "allCandidates_count", len(allCandidates))

	// Build inputs alongside an index→candidate map for verdict routing.
	inputs := make([]ai.DedupPairInput, 0, len(allCandidates))
	byIndex := make(map[int]database.DedupCandidate, len(allCandidates))
	for i, c := range allCandidates {
		input, ok := de.buildPairInput(i, c)
		if !ok {
			slog.Info("dedup skipping candidate — could not load entities", "c", c.ID)
			continue
		}
		inputs = append(inputs, input)
		byIndex[i] = c
	}
	if len(inputs) == 0 {
		return nil
	}

	// Build byIndex as map[int]int64 for the aijobs payload.
	byIndexIDs := make(map[int]int64, len(byIndex))
	for idx, c := range byIndex {
		byIndexIDs[idx] = c.ID
	}

	if de.aiJobsStore == nil {
		return fmt.Errorf("dedup: aiJobsStore not configured; cannot submit async batch")
	}

	deps := aijobs.Deps{
		Store:  de.aiJobsStore,
		Client: &ai.AIJobsBatchClient{Parser: de.llmParser},
	}
	model := config.AppConfig.DedupReviewModel // per-feature model knob (AI-MODEL-1)
	if model == "" {
		model = "gpt-5-mini"
	}
	jobID, err := ai.SubmitDedupReviewJob(ctx, deps, model, inputs, byIndexIDs)
	if err != nil {
		return fmt.Errorf("submit dedup review job: %w", err)
	}
	subBatchCount := (len(inputs) + 24) / 25
	slog.Info("dedup LLM review job submitted — ( pair(s), row(s))", "jobID", jobID, "inputs_count", len(inputs), "subBatchCount", subBatchCount)
	return nil
}

// listAmbiguousCandidates returns pending embedding-layer candidates whose
// similarity falls inside [low, high].
func (de *Engine) listAmbiguousCandidates(entityType string, low, high float64) ([]database.DedupCandidate, error) {
	filter := database.CandidateFilter{
		EntityType:    entityType,
		Status:        "pending",
		Layer:         "embedding",
		MinSimilarity: &low,
		MaxSimilarity: &high,
		Limit:         10000,
	}
	candidates, _, err := de.embedStore.ListCandidates(filter)
	return candidates, err
}

// buildPairInput enriches a stored candidate with entity details suitable for
// the LLM prompt. Returns false if either entity could not be loaded.
func (de *Engine) buildPairInput(index int, c database.DedupCandidate) (ai.DedupPairInput, bool) {
	input := ai.DedupPairInput{
		Index:      index,
		EntityType: c.EntityType,
	}
	if c.Similarity != nil {
		input.Similarity = *c.Similarity
	}

	switch c.EntityType {
	case "book":
		a, aOK := de.loadBookEntity(c.EntityAID)
		b, bOK := de.loadBookEntity(c.EntityBID)
		if !aOK || !bOK {
			return input, false
		}
		input.A = a
		input.B = b
	case "author":
		a, aOK := de.loadAuthorEntity(c.EntityAID)
		b, bOK := de.loadAuthorEntity(c.EntityBID)
		if !aOK || !bOK {
			return input, false
		}
		input.A = a
		input.B = b
	default:
		return input, false
	}
	return input, true
}

// loadBookEntity fetches a book and converts it into a DedupEntity. The caller
// may rely on ID always being populated when the second return value is true.
func (de *Engine) loadBookEntity(bookID string) (ai.DedupEntity, bool) {
	book, err := de.bookStore.GetBookByID(bookID)
	if err != nil || book == nil {
		return ai.DedupEntity{}, false
	}
	entity := ai.DedupEntity{ID: book.ID, Title: book.Title}
	if book.AuthorID != nil {
		if author, aerr := de.bookStore.GetAuthorByID(*book.AuthorID); aerr == nil && author != nil {
			entity.Author = author.Name
		}
	}
	if book.Narrator != nil {
		entity.Narrator = *book.Narrator
	}
	if book.ISBN13 != nil && *book.ISBN13 != "" {
		entity.ISBN = *book.ISBN13
	} else if book.ISBN10 != nil {
		entity.ISBN = *book.ISBN10
	}
	if book.ASIN != nil {
		entity.ASIN = *book.ASIN
	}
	return entity, true
}

// loadAuthorEntity fetches an author and converts it into a DedupEntity. The
// Title field carries the author name so the prompt treats both entity types
// uniformly.
func (de *Engine) loadAuthorEntity(entityID string) (ai.DedupEntity, bool) {
	id, err := strconv.Atoi(entityID)
	if err != nil {
		return ai.DedupEntity{}, false
	}
	author, err := de.bookStore.GetAuthorByID(id)
	if err != nil || author == nil {
		return ai.DedupEntity{}, false
	}
	return ai.DedupEntity{ID: entityID, Title: author.Name}, true
}

// ApplyVerdicts persists each LLM verdict via UpdateCandidateLLM
// and, when DedupLLMAutoMergeHighConfidence is enabled, fires an
// immediate merge for "duplicate" verdicts at confidence "high".
//
// Returns the number of verdicts successfully persisted (not the
// number of merges fired — that's logged separately).
//
// Auto-merges via LLM verdict tag the surviving book with
// `dedup:merge-survivor:llm-auto` as a system tag so the user
// can later filter "things the LLM decided for me" and review
// them post-hoc. The LLM's reason string is recorded in the
// candidate's llm_reason column for audit trail.
//
// Errors are logged and skipped so one bad row doesn't abort
// the whole batch.
func (de *Engine) ApplyVerdicts(verdicts []ai.DedupPairVerdict, byIndex map[int]database.DedupCandidate) int {
	applied := 0
	autoMerged := 0
	for _, v := range verdicts {
		candidate, ok := byIndex[v.Index]
		if !ok {
			slog.Info("dedup LLM returned unknown index", "v", v.Index)
			continue
		}
		verdict := "not_duplicate"
		if v.IsDuplicate {
			verdict = "duplicate"
		}
		reason := v.Reason
		if v.Confidence != "" {
			reason = fmt.Sprintf("[%s] %s", v.Confidence, reason)
		}
		if err := de.embedStore.UpdateCandidateLLM(candidate.ID, verdict, reason); err != nil {
			slog.Error("dedup failed to update candidate", "candidate", candidate.ID, "err", err)
			continue
		}
		applied++

		// Auto-merge path (opt-in, book candidates only, high
		// confidence only). Author candidates are NOT auto-merged
		// — author merges are structural and user-visible enough
		// that we require manual confirmation regardless of
		// confidence.
		if !config.AppConfig.DedupLLMAutoMergeHighConfidence {
			continue
		}
		if candidate.EntityType != "book" {
			continue
		}
		if !v.IsDuplicate {
			continue
		}
		if !strings.EqualFold(v.Confidence, "high") {
			continue
		}
		if de.mergeService == nil {
			slog.Info("dedup LLM auto-merge skipped for candidate — mergeService unavailable", "candidate", candidate.ID)
			continue
		}

		result, mergeErr := de.mergeService.MergeBooks(
			[]string{candidate.EntityAID, candidate.EntityBID},
			"", // auto-pick primary via bookIsBetter
		)
		if mergeErr != nil {
			slog.Error("dedup LLM auto-merge failed for candidate ( + )", "candidate", candidate.ID, "candidate", candidate.EntityAID, "candidate", candidate.EntityBID, "mergeErr", mergeErr)
			continue
		}

		// Mark candidate as merged in the dedup store so it
		// drops off the pending/review tab.
		if err := de.embedStore.UpdateCandidateStatus(candidate.ID, "merged"); err != nil {
			slog.Error("dedup failed to mark candidate merged", "candidate", candidate.ID, "err", err)
		}

		// Tag the surviving book with the auto-merge provenance.
		// The tag carries the "llm-auto" suffix so users can
		// filter "merged by the LLM at high confidence" separately
		// from hand-merges and other auto-merge sources.
		if result != nil && result.PrimaryID != "" {
			if tagErr := database.EnsureSingletonBookTag(
				de.bookStore,
				result.PrimaryID,
				"dedup:merge-survivor",
				"dedup:merge-survivor:llm-auto",
				"system",
			); tagErr != nil {
				slog.Error("dedup failed to tag auto-merged survivor", "result", result.PrimaryID, "tagErr", tagErr)
			}
		}

		autoMerged++
		slog.Info("dedup LLM auto-merged candidate ( + ) — reason", "candidate", candidate.ID, "candidate", candidate.EntityAID, "candidate", candidate.EntityBID, "reason", reason)
	}
	if autoMerged > 0 {
		slog.Info("dedup LLM auto-merge fired on high-confidence pair(s)", "autoMerged", autoMerged)
	}
	return applied
}

// levenshteinDistance computes the Levenshtein edit distance between two strings.
func levenshteinDistance(a, b string) int {
	la := len(a)
	lb := len(b)

	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Use two rows instead of full matrix for O(min(m,n)) space.
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)

	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = min3(del, ins, sub)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// min3 returns the minimum of three integers.
func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// normalizeTitle lowercases, trims whitespace, and collapses internal whitespace.
// normalizeTitleRe matches anything that isn't alphanumeric or whitespace —
// used to strip punctuation so "Foo: The Bar" and "Foo The Bar" collapse to
// the same normalized form.
var normalizeTitleRe = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)

// normalizeTitleQuoteStripper matches apostrophes / single quotes / smart
// quotes — characters that should be stripped to *nothing* rather than a
// space so "Ender's Game" becomes "enders game" instead of "ender s game".
var normalizeTitleQuoteStripper = regexp.MustCompile("[\u0027\u2018\u2019\u201C\u201D\"]")

// normalizeTitle folds a title to the canonical form used across the dedup
// engine's exact-match layer. It is deliberately aggressive:
//
//   - lowercase
//   - "&" and "+" fold to " and " so "Foundation & Empire" matches
//     "Foundation and Empire"
//   - all non-alphanumeric characters (punctuation, smart quotes, em-dashes)
//     are stripped
//   - leading articles ("the", "a", "an") are dropped so "The Hobbit"
//     matches "Hobbit"
//   - multiple whitespace runs collapse to a single space
//
// These transforms mirror what a human naturally ignores when deciding if
// two titles are "the same book". They are applied uniformly on both sides
// of any title comparison so the folding never produces false positives on
// its own — the caller still has to decide how close is close enough.
func normalizeTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))

	// Strip quotes and apostrophes to *nothing* first, so "Ender's Game"
	// collapses to "enders game" instead of "ender s game". This has to
	// happen before the general punctuation pass because that one
	// replaces with a space.
	title = normalizeTitleQuoteStripper.ReplaceAllString(title, "")

	// Fold ampersands / plus signs to the word "and" BEFORE stripping
	// punctuation, otherwise the regex would eat the "&" and leave the
	// words around it glued together ("Foo & Bar" -> "foo  bar", which
	// normalizes the same as "Foo Bar" and loses the conjunction).
	title = strings.ReplaceAll(title, "&", " and ")
	title = strings.ReplaceAll(title, "+", " and ")

	// Strip everything else that isn't a letter, digit, or whitespace —
	// colons, dashes, parens, etc. all get replaced with a space so the
	// words on either side don't glue together.
	title = normalizeTitleRe.ReplaceAllString(title, " ")

	// Collapse whitespace runs to a single space.
	title = strings.Join(strings.Fields(title), " ")

	// Drop a leading article. Only a leading one — "A Game of Thrones"
	// should match "Game of Thrones" but "Go Set a Watchman" must not
	// turn into "Go Set Watchman".
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(title, article) {
			title = title[len(article):]
			break
		}
	}

	return title
}

// allNormalizedTitleForms returns the set of normalized title strings
// for a book — its primary title plus every alternative title stored
// in book_alternative_titles. Alt titles let users encode variants
// the normalizer can't auto-derive: manga romaji vs translated
// English, rebrands where the title changed entirely, subtitle
// reorderings, etc. The exact-match Layer uses this set to answer
// "does ANY form of book A match ANY form of book B" — a single
// user-entered alt title can rescue a previously-missed duplicate.
//
// Errors from the alt-title lookup are swallowed and logged — if
// the alt-title store is unavailable we fall back to primary-only
// matching, which is the pre-alt-titles behavior.
func (de *Engine) allNormalizedTitleForms(book *database.Book) []string {
	forms := []string{normalizeTitle(book.Title)}
	seen := map[string]struct{}{forms[0]: {}}
	if de.bookStore != nil {
		if alts, err := de.bookStore.GetBookAlternativeTitles(book.ID); err == nil {
			for _, alt := range alts {
				norm := normalizeTitle(alt.Title)
				if norm == "" {
					continue
				}
				if _, dup := seen[norm]; dup {
					continue
				}
				seen[norm] = struct{}{}
				forms = append(forms, norm)
			}
		} else {
			slog.Info("dedup alt title lookup for", "book", book.ID, "err", err)
		}
	}
	return forms
}

// minLevenshteinBetweenForms returns the minimum Levenshtein distance
// between any pair of strings drawn one from each input slice. Used by
// checkExactTitle to find the closest match across primary + alt title
// forms of two books. With typical N, M of 1-5 the O(N*M) nested loop
// is trivially fast and much simpler than any clever early-exit.
func minLevenshteinBetweenForms(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 1 << 30
	}
	minDist := 1 << 30
	for _, x := range a {
		for _, y := range b {
			d := levenshteinDistance(x, y)
			if d < minDist {
				minDist = d
				if minDist == 0 {
					return 0
				}
			}
		}
	}
	return minDist
}

// AcoustIDScan walks all primary books, extracts their stored acoustic
// fingerprint segments, and emits DedupCandidate rows (layer="acoustid")
// for any two books whose audio content matches.
//
// Per-pair deduplication is enforced via a canonical pair key so the scan
// never emits (A,B) and (B,A) as separate candidates, regardless of which
// segment triggered the match.
//
// Matching strategy per segment:
//  1. Exact: O(1) index lookup. Similarity = 1.0.
//  2. Fuzzy: Hamming distance scan. Similarity = actual bit-agreement fraction.
//
// The scan skips books whose files have no fingerprints yet (not yet backfilled).
// Progress callback receives (done, total) book counts.
func (de *Engine) AcoustIDScan(ctx context.Context, progress func(done, total int)) error {
	books, err := de.getAllBooks()
	if err != nil {
		return fmt.Errorf("acoustid scan: get all books: %w", err)
	}

	// emitted tracks canonical pair keys we've already inserted this run so we
	// don't call UpsertCandidate multiple times for the same pair (can happen
	// when two books share several segments).
	emitted := make(map[string]struct{})
	pairKey := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + ":" + b
	}

	// Per-book parent-directory cache so we don't fetch BookFiles twice per
	// comparison. Empty string = unknown / no files / different parents.
	parentDirCache := make(map[string]string)
	parentDirForBook := func(bookID string) string {
		if v, ok := parentDirCache[bookID]; ok {
			return v
		}
		bfs, err := de.bookStore.GetBookFiles(bookID)
		if err != nil || len(bfs) == 0 {
			parentDirCache[bookID] = ""
			return ""
		}
		dir := filepath.Dir(bfs[0].FilePath)
		for _, bf := range bfs[1:] {
			if filepath.Dir(bf.FilePath) != dir {
				parentDirCache[bookID] = ""
				return ""
			}
		}
		parentDirCache[bookID] = dir
		return dir
	}

	emit := func(bookAID, bookBID string, sim float64) {
		key := pairKey(bookAID, bookBID)
		if _, already := emitted[key]; already {
			return
		}
		// Suppress when both books' files live in the same directory: those
		// are chapter/part files of one multi-file book, not duplicates.
		// The scanner's split-book detection (PR #1167) prevents this for
		// new imports, but the library has thousands of pre-PR splits that
		// would otherwise be flagged as 100% AcoustID matches just because
		// their fingerprint segments happen to overlap.
		if dirA := parentDirForBook(bookAID); dirA != "" {
			if dirB := parentDirForBook(bookBID); dirB == dirA {
				return
			}
		}
		emitted[key] = struct{}{}
		if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "book",
			EntityAID:  bookAID,
			EntityBID:  bookBID,
			Layer:      "acoustid",
			Similarity: &sim,
			Status:     "pending",
		}); err != nil {
			slog.Info("[dedup] acoustid scan upsert candidate (, )", "bookAID", bookAID, "bookBID", bookBID, "err", err)
		}
	}

	total := len(books)
	for i, book := range books {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		files, err := de.bookStore.GetBookFiles(book.ID)
		if err != nil {
			slog.Info("[dedup] acoustid scan get files for", "book", book.ID, "err", err)
			continue
		}

		// Prime cache for this book (avoids the duplicate GetBookFiles
		// inside parentDirForBook when emit is called for this side).
		if _, cached := parentDirCache[book.ID]; !cached {
			if len(files) == 0 {
				parentDirCache[book.ID] = ""
			} else {
				dir := filepath.Dir(files[0].FilePath)
				ok := true
				for _, bf := range files[1:] {
					if filepath.Dir(bf.FilePath) != dir {
						ok = false
						break
					}
				}
				if ok {
					parentDirCache[book.ID] = dir
				} else {
					parentDirCache[book.ID] = ""
				}
			}
		}

		// LSH-backed candidate lookup is optional — only PebbleStore
		// implements it. SQLite/mock stores skip this path and fall
		// through to the segment walk below.
		lshStore, _ := de.bookStore.(interface {
			LookupAcoustIDCandidates(fp []byte, maxCandidates int) ([]string, error)
			GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
		})

		for _, f := range files {
			// Tier-0: whole-file LSH candidate set + Hamming refine.
			// Sub-linear via the fpidx: secondary index, so it runs
			// unconditionally (index caps candidates, so work is bounded).
			if lshStore != nil && len(f.AcoustIDFingerprint) > 0 {
				cands, _ := lshStore.LookupAcoustIDCandidates(f.AcoustIDFingerprint, 200)
				for _, candID := range cands {
					if candID == f.ID {
						continue
					}
					cand, _ := lshStore.GetBookFileByID("", candID)
					if cand == nil || cand.BookID == book.ID || len(cand.AcoustIDFingerprint) == 0 {
						continue
					}
					sim, simErr := fingerprint.WholeFileSimilarity(f.AcoustIDFingerprint, cand.AcoustIDFingerprint)
					if simErr != nil {
						continue
					}
					if sim >= fingerprint.FuzzyMinSimilarity {
						emit(book.ID, cand.BookID, sim)
					}
				}
			}

			// Tier-1 (exact) and Tier-2 (legacy fuzzy walk) over seg
			// strings — still useful for rows that haven't been
			// re-fingerprinted to whole-file yet (no AcoustIDFingerprint).
			segs := []string{
				f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2,
				f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5,
				f.AcoustIDSeg6,
			}
			for _, seg := range segs {
				if seg == "" {
					continue
				}
				// Reject degenerate fingerprints (e.g. "AQAAAA" sentinel
				// from a failed ffmpeg seek). They'd otherwise match every
				// other book carrying the same sentinel at similarity 1.0.
				// The writer now drops these, but old rows in production
				// still need this guard until reset-and-rescan clears them.
				if !fingerprint.IsUsefulFingerprint(seg) {
					continue
				}

				// Tier 1: exact match (O(1) via Pebble book_file_acoustid: index).
				exactHit, _ := de.bookStore.GetBookFileByAcoustID(seg)
				if exactHit != nil && exactHit.BookID != book.ID {
					emit(book.ID, exactHit.BookID, 1.0)
					continue
				}

			}
		}

		if progress != nil && (i%50 == 0 || i == total-1) {
			progress(i+1, total)
		}
	}

	slog.Info("[dedup] acoustid scan complete books scanned, candidate pair(s) emitted", "total", total, "emitted_count", len(emitted))
	return nil
}

// bestSeg returns the first non-empty segment string from a BookFile,
// used as the representative fingerprint for Hamming similarity comparison.
func bestSeg(f *database.BookFile) string {
	for _, s := range []string{
		f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2,
		f.AcoustIDSeg3, f.AcoustIDSeg4, f.AcoustIDSeg5,
		f.AcoustIDSeg6,
	} {
		if s != "" {
			return s
		}
	}
	return ""
}

// derefStr is defined in audiobook_service.go

// BookSignatureScan walks all primary books and emits dedup_candidates based
// on book-level fingerprint similarity (layer: "book_signature"). Books are
// compared pairwise using BookSignatureSimilarityMasked (falls back to
// BookSignatureSimilarity when neither book has a mask); pairs exceeding
// FuzzyMinSimilarity (0.80) are emitted as candidates.
//
// Pairs with fewer than 512 overlapping words (due to partial sig masks) are
// skipped — not enough data for a reliable comparison.
//
// Skips books that don't have a synthesized book_sig_v1 yet (not backfilled).
// Progress callback receives (done, total) book counts.
func (de *Engine) BookSignatureScan(ctx context.Context, progress func(done, total int)) error {
	books, err := de.getAllBooks()
	if err != nil {
		return fmt.Errorf("book signature scan: get all books: %w", err)
	}

	// Filter to books that have a book signature
	var booksWithSig []database.Book
	for _, b := range books {
		if b.BookSigV1 != nil && *b.BookSigV1 != "" {
			booksWithSig = append(booksWithSig, b)
		}
	}

	emitted := make(map[string]struct{})
	pairKey := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + ":" + b
	}

	emit := func(bookAID, bookBID string, sim float64) {
		key := pairKey(bookAID, bookBID)
		if _, already := emitted[key]; already {
			return
		}
		emitted[key] = struct{}{}
		if err := de.embedStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "book",
			EntityAID:  bookAID,
			EntityBID:  bookBID,
			Layer:      "book_signature",
			Similarity: &sim,
			Status:     "pending",
		}); err != nil {
			slog.Info("[dedup] book signature scan upsert candidate (, )", "bookAID", bookAID, "bookBID", bookBID, "err", err)
		}
	}

	total := len(booksWithSig)
	for i, bookA := range booksWithSig {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		sigA := *bookA.BookSigV1
		maskA := ""
		if bookA.BookSigV1Mask != nil {
			maskA = *bookA.BookSigV1Mask
		}
		for j := i + 1; j < len(booksWithSig); j++ {
			bookB := booksWithSig[j]
			sigB := *bookB.BookSigV1
			maskB := ""
			if bookB.BookSigV1Mask != nil {
				maskB = *bookB.BookSigV1Mask
			}

			sim, overlap, err := fingerprint.BookSignatureSimilarityMasked(sigA, sigB, maskA, maskB)
			if err != nil {
				slog.Info("[dedup] book signature scan compare vs", "bookA", bookA.ID, "bookB", bookB.ID, "err", err)
				continue
			}
			// Skip pairs with insufficient overlap (partial sigs with non-overlapping missing sections).
			const minOverlapWords = 512
			if overlap < minOverlapWords {
				slog.Info("[dedup] book signature scan skip vs (overlap < )", "bookA", bookA.ID, "bookB", bookB.ID, "overlap", overlap, "minOverlapWords", minOverlapWords)
				continue
			}

			if sim >= fingerprint.FuzzyMinSimilarity {
				emit(bookA.ID, bookB.ID, sim)
			}
		}

		if progress != nil {
			progress(i+1, total)
		}
	}

	slog.Info("[dedup] book signature scan complete books scanned, candidate pair(s) emitted", "total", total, "emitted_count", len(emitted))
	return nil
}
