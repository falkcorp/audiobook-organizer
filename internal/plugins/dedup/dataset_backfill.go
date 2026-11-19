// file: internal/plugins/dedup/dataset_backfill.go
// version: 1.7.0
// guid: 2d6f8a13-7c40-4e92-8b15-9a3e5c7d2f64
// last-edited: 2026-11-18

// Package dedup — op dedup.dataset-backfill (spec C4 backfill).
//
// Iterates all pending candidates, builds a LabeledExample for each, runs the
// deterministic catchers (Classify), and writes the labeled example to the
// dedup:label keyspace. With apply=true, any candidate a catcher labels
// "not_dup" is suppressed (status → "dismissed") so residual part-vs-whole /
// missing-file false positives leave the review queue.
//
// Dry-run by default: reports counts, writes nothing. The apply path is
// idempotent — UpsertLabeledExample overwrites and re-dismissing an already-
// dismissed candidate is a no-op, so re-running is safe (no done-flag needed).
//
// NOTE on suppression counts: in practice, the dominant residual class (stub /
// unscanned-file pairs with one side duration=0) is NOT caught by the
// duration-ratio or missing-file catchers when file records exist but the file
// is 0-second. The op labels and suppresses only what the catchers actually
// fire on — counters are always honest and may be lower than the total pending
// backlog. That is by design.
package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// datasetBackfillParams are the JSON parameters accepted by the op.
type datasetBackfillParams struct {
	// Apply, if true, writes labeled examples and suppresses not_dup candidates.
	// Default false (dry-run) — the op only reports counts, writes nothing.
	Apply bool `json:"apply"`
}

// datasetBackfillKind is rule R3's native kind for dedup.dataset-backfill.
const datasetBackfillKind = tasks.Kind("books.dedup.dataset-backfill")

// datasetBackfillHelp is the definition's description, HEAD's and the native
// kind's Help (rule R5: verbatim).
const datasetBackfillHelp = "Builds a labeled example per pending candidate, runs deterministic catchers, " +
	"and (apply=true) suppresses rule-labeled not_dup candidates (status → dismissed). " +
	"Dry-run by default."

// datasetBackfillDef returns the OperationDef for dedup.dataset-backfill.
func (p *Plugin) datasetBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.dataset-backfill",
		Preview:         sdk.PreviewSpec{Params: datasetBackfillParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Backfill dedup tuning dataset",
		Description:     datasetBackfillHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.dataset-backfill",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDatasetBackfill,
	}
}

// builderAdapter satisfies dataset.BuilderStore using the plugin's main store.
// dataset.BuilderStore requires GetBook(id string) and GetBookFiles(id string).
// pluginStore exposes GetBookByID (not GetBook), so the adapter bridges the
// name mismatch while keeping the interface names canonical.
type builderAdapter struct{ store pluginStore }

func (b builderAdapter) GetBook(id string) (*database.Book, error) {
	return b.store.GetBookByID(id)
}

func (b builderAdapter) GetBookFiles(id string) ([]database.BookFile, error) {
	return b.store.GetBookFiles(id)
}

// bookLookupResult is a memoized GetBook outcome (both the book and any error
// are cached, so a candidate row referencing a not-found/errored book isn't
// re-queried on every repeat occurrence either).
type bookLookupResult struct {
	book *database.Book
	err  error
}

// memoizedBuilderAdapter wraps a builderAdapter with a mutex-guarded
// book-lookup cache (CONC-8): dedup.mine-gold-labels and dedup.dataset-backfill
// both iterate pending candidates where the same book can appear in many rows,
// and previously re-read that book from the store every time. Mirrors the
// bookCache pattern in internal/dedup/drain_stale.go and
// internal/server/server_maintenance_deps.go, but mutex-guarded here because
// the cache is shared across the registry.RunItems worker pool both callers
// now use — a plain map would race under -race.
//
// Only GetBook is memoized, matching the referenced bookCache patterns;
// GetBookFiles passes straight through to the inner adapter.
type memoizedBuilderAdapter struct {
	inner dataset.BuilderStore
	mu    sync.Mutex
	cache map[string]bookLookupResult
}

// newMemoizedBuilderAdapter returns a memoizedBuilderAdapter ready for
// concurrent use across a registry.RunItems worker pool.
func newMemoizedBuilderAdapter(inner dataset.BuilderStore) *memoizedBuilderAdapter {
	return &memoizedBuilderAdapter{inner: inner, cache: make(map[string]bookLookupResult)}
}

func (m *memoizedBuilderAdapter) GetBook(id string) (*database.Book, error) {
	m.mu.Lock()
	if r, ok := m.cache[id]; ok {
		m.mu.Unlock()
		return r.book, r.err
	}
	m.mu.Unlock()

	book, err := m.inner.GetBook(id)

	m.mu.Lock()
	m.cache[id] = bookLookupResult{book: book, err: err}
	m.mu.Unlock()
	return book, err
}

func (m *memoizedBuilderAdapter) GetBookFiles(id string) ([]database.BookFile, error) {
	return m.inner.GetBookFiles(id)
}

// datasetBackfillEmbeddingStore is the narrow embedding-store surface
// runDatasetBackfill needs: list pending candidates, upsert a labeled
// example, and (apply=true) flip a candidate's status. Abstracted so the
// apply-path's upsert-failure handling can be unit-tested with a fake that
// simulates a write failure (the real *database.EmbeddingStore satisfies
// this interface unmodified). Mirrors labeledExampleStore in
// rescore_labeled_examples.go.
type datasetBackfillEmbeddingStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
	UpsertLabeledExample(ex database.LabeledExample) error
	// ReclassifyCandidate is the guarded automated status write: it re-checks
	// the row under the manual-pin lock (see its doc).
	ReclassifyCandidate(id int64, fromStatus, toStatus string) error
}

// datasetBackfillReads is the reads the core takes (rule R10): the pending
// backlog listing and the book and file lookups the example builder makes.
type datasetBackfillReads interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// datasetBackfillWriter is the writes the core makes (HEAD: the same two
// EmbeddingStore methods): the labeled-example upsert and the guarded status
// flip that suppresses a catcher-confirmed not_dup candidate.
type datasetBackfillWriter interface {
	UpsertLabeledExample(ex database.LabeledExample) error
	// ReclassifyCandidate is the guarded automated status write: it re-checks
	// the row under the manual-pin lock (see its doc).
	ReclassifyCandidate(id int64, fromStatus, toStatus string) error
}

// datasetBackfillEnv is what the core takes from its shell (rules R2 and R10).
// Live is HEAD's apply=true; a dry run never calls Writer, which may then be nil.
type datasetBackfillEnv struct {
	kindEnv
	Store  datasetBackfillReads
	Writer datasetBackfillWriter
}

// datasetBackfillResult is the native run's result: the counters HEAD folded
// into its summary line. HEAD stored no result, so its shell drops it.
type datasetBackfillResult struct {
	Examined   int `json:"examined"`
	NotDup     int `json:"not_dup"`
	TrueDup    int `json:"true_dup"`
	Labeled    int `json:"labeled"`
	Suppressed int `json:"suppressed"`
	BuildErrs  int `json:"build_errors"`
	UpsertErrs int `json:"upsert_errors"`
}

// datasetBackfillParallelism is the candidate pool (rule R4): HEAD's
// runtime.NumCPU() workers. Each candidate's reads and writes touch only its own
// rows (the only shared state is the mutex-guarded book memo and counters).
func datasetBackfillParallelism() tasks.Parallelism { return tasks.CPU() }

// headDatasetReads is HEAD's reads for the core: the embedding store's candidate
// listing and the main store's book lookups.
type headDatasetReads struct {
	emb   datasetBackfillCandidateLister
	books interface {
		GetBookByID(id string) (*database.Book, error)
		GetBookFiles(bookID string) ([]database.BookFile, error)
	}
}

// datasetBackfillCandidateLister is the listing half of
// datasetBackfillEmbeddingStore.
type datasetBackfillCandidateLister interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
}

func (h headDatasetReads) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	return h.emb.ListCandidates(f)
}
func (h headDatasetReads) GetBookByID(id string) (*database.Book, error) {
	return h.books.GetBookByID(id)
}
func (h headDatasetReads) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return h.books.GetBookFiles(bookID)
}

// datasetBookAdapter bridges the core's GetBookByID to dataset.BuilderStore's
// GetBook name.
type datasetBookAdapter struct{ store datasetBackfillReads }

func (b datasetBookAdapter) GetBook(id string) (*database.Book, error) {
	return b.store.GetBookByID(id)
}
func (b datasetBookAdapter) GetBookFiles(id string) ([]database.BookFile, error) {
	return b.store.GetBookFiles(id)
}

// runDatasetBackfill implements the dedup.dataset-backfill op.
func (p *Plugin) runDatasetBackfill(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}
	return runDatasetBackfillWith(ctx, p.embeddingStore, p.store, rawParams, reporter)
}

// runDatasetBackfillWith is the v2 shell around datasetBackfillCore: params over
// the given embStore and mainStore (the testable seam).
func runDatasetBackfillWith(
	ctx context.Context,
	embStore datasetBackfillEmbeddingStore,
	mainStore pluginStore,
	rawParams json.RawMessage,
	reporter sdk.Reporter,
) error {
	// --- Parse params ---
	var params datasetBackfillParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	_, err := datasetBackfillCore(ctx, datasetBackfillEnv{
		kindEnv: headEnv(reporter, params.Apply),
		Store:   headDatasetReads{emb: embStore, books: mainStore},
		Writer:  embStore,
	})
	return err
}

// datasetBackfillCore is the body: it lists pending candidates, classifies each
// with the deterministic catchers, and (Live) writes/dismisses through the
// env's writer. Shared by both shells.
func datasetBackfillCore(ctx context.Context, env datasetBackfillEnv) (datasetBackfillResult, error) {
	env.Logger.Info("dataset-backfill start", "apply", env.Live)

	// --- Load all pending candidates ---
	env.Progress(0, 2, "Loading pending candidates…")
	filter := database.CandidateFilter{
		Status: "pending",
		Limit:  1_000_000,
	}
	cands, _, err := env.Store.ListCandidates(filter)
	if err != nil {
		return datasetBackfillResult{}, fmt.Errorf("list candidates: %w", err)
	}

	env.Logger.Info("dataset-backfill: candidates loaded", "count", len(cands))

	// Book-lookup memoization (CONC-8): see memoizedBuilderAdapter doc comment.
	// The cache is mutex-guarded because it's shared across the RunItems
	// worker pool below.
	adapter := newMemoizedBuilderAdapter(datasetBookAdapter{store: env.Store})

	// statsMu guards the counters below, which every worker goroutine
	// increments. Each candidate's own store reads/writes are independent and
	// need no lock — only the shared summary counters do.
	var (
		statsMu    sync.Mutex
		examined   int
		labeled    int
		suppressed int
		notDup     int
		trueDup    int
		buildErrs  int
		upsertErrs int
	)

	env.Progress(1, 2, fmt.Sprintf("Processing %d candidates…", len(cands)))

	err = env.Items(ctx, datasetBackfillParallelism(), len(cands), kindItemsOptions{
		PureLabel: func(i, total int) string {
			return fmt.Sprintf("Processed %d/%d candidates…", i+1, total)
		},
	}, func(ctx context.Context, ci int) error {
		c := cands[ci]
		if env.Canceled() {
			return context.Canceled
		}

		statsMu.Lock()
		examined++
		statsMu.Unlock()

		// Build feature vector for the candidate pair.
		ex, buildErr := dataset.BuildExample(adapter, c)
		if buildErr != nil {
			statsMu.Lock()
			buildErrs++
			statsMu.Unlock()
			env.Logger.Warn("dataset-backfill: build error",
				"candidate_id", c.ID,
				"entity_a", c.EntityAID,
				"entity_b", c.EntityBID,
				"error", buildErr)
			return nil
		}

		// Run deterministic catchers.
		if label, reason, fires := dataset.Classify(ex); fires {
			ex.Label = label
			ex.LabelSource = "rule"
			ex.LabelReason = reason
			ex.DecidedAt = time.Now().UTC().Format(time.RFC3339)
			switch label {
			case "not_dup":
				statsMu.Lock()
				notDup++
				statsMu.Unlock()
			case "true_dup":
				statsMu.Lock()
				trueDup++
				statsMu.Unlock()
			}
		}
		// If no catcher fired, ex.Label remains "" — example is unlabeled, written
		// as-is so features are captured for future human/ML labeling.

		if env.Live {
			// Write the labeled (or unlabeled) example to the store.
			upsertOK := false
			if err := env.Writer.UpsertLabeledExample(ex); err != nil {
				env.Logger.Error("dataset-backfill: upsert error",
					"candidate_id", c.ID, "error", err)
				statsMu.Lock()
				upsertErrs++
				statsMu.Unlock()
				// Continue — partial progress is better than aborting.
			} else {
				upsertOK = true
				statsMu.Lock()
				labeled++
				statsMu.Unlock()
			}

			// Suppress only catchers-confirmed not_dup candidates whose label
			// write actually succeeded. On an upsert failure the candidate
			// stays "pending" for retry — dismissing it here would leave the
			// label unwritten AND the candidate unreachable for re-examination.
			// A manual candidate is never dismissed by a catcher: a human
			// enqueued it to decide it. Its example is still labeled above.
			if upsertOK && ex.Label == "not_dup" && !database.IsManualCandidate(c) {
				// ReclassifyCandidate re-checks at write time: a pair a human
				// pinned or decided since the list was read is left alone.
				err := env.Writer.ReclassifyCandidate(c.ID, "pending", "dismissed")
				switch {
				case errors.Is(err, database.ErrManualCandidateProtected), errors.Is(err, database.ErrCandidateStatusChanged):
					env.Logger.Info("dataset-backfill: left a row changed since the list",
						"candidate_id", c.ID, "reason", err)
				case err != nil:
					env.Logger.Error("dataset-backfill: suppress error",
						"candidate_id", c.ID, "error", err)
				default:
					statsMu.Lock()
					suppressed++
					statsMu.Unlock()
				}
			}
		}
		return nil
	})
	if err != nil {
		return datasetBackfillResult{}, err
	}

	summary := fmt.Sprintf(
		"examined=%d not_dup=%d true_dup=%d labeled=%d suppressed=%d build_errs=%d upsert_errs=%d (apply=%v)",
		examined, notDup, trueDup, labeled, suppressed, buildErrs, upsertErrs, env.Live,
	)
	env.Logger.Info("dataset-backfill complete", "summary", summary)

	if !env.Live {
		env.Progress(2, 2,
			fmt.Sprintf("Dry-run complete — %d not_dup, %d true_dup of %d examined. Pass apply=true to write.", notDup, trueDup, examined))
	} else {
		env.Progress(2, 2,
			fmt.Sprintf("Complete — %d/%d labeled, %d suppressed. %s", labeled, examined, suppressed, summary))
	}

	return datasetBackfillResult{
		Examined: examined, NotDup: notDup, TrueDup: trueDup, Labeled: labeled,
		Suppressed: suppressed, BuildErrs: buildErrs, UpsertErrs: upsertErrs,
	}, nil
}
