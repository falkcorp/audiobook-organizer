// file: internal/plugins/dedup/cleanup_orphan_embeddings.go
// version: 1.3.0
// guid: 9be49561-c03e-484c-ae53-723bbe299ebe
// last-edited: 2026-11-18

// Package dedup — op dedup.cleanup-orphan-embeddings.
//
// PR #1802 fixed PebbleStore.DeleteBook to also delete a book's embedding row
// (emb:v:book:<id>) as part of the same batch that removes the book itself —
// but that fix is forward-only. It stops NEW orphans from being created; it
// does nothing about embeddings orphaned by book deletions that happened
// BEFORE the fix landed (merges/purges over the project's history). Those
// pre-existing orphans are the likely dominant cause of the
// `dedup.calibrate-embedding-thresholds` `skipped_dim=2841` figure (out of
// 5301 scored gold-label pairs) — the referenced book is gone, so nothing
// ever revisits or re-embeds the stale row to the current model/dimension.
//
// This op is the retroactive counterpart to #1802: it walks every
// `emb:v:book:*` row and, for each, checks whether GetBookByID still resolves
// the entity ID to a live book.
//
//   - Dry-run (default): reports orphaned vs. live counts and a bounded sample
//     of orphaned entity IDs + their stored .Model, for a reviewer to
//     spot-check before applying.
//   - Apply (apply=true): deletes ONLY the rows confirmed orphaned (GetBookByID
//     returned nil). A row whose book still exists is NEVER touched by this
//     op, regardless of that embedding's model/dimension — a live book's
//     stale-model embedding is in scope for dedup.embed-scan/reembed-embeddings,
//     not this cleanup op.
//
// Idempotent: apply only ever deletes rows it can currently prove orphaned, so
// a second apply run finds nothing left to delete.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// cleanupOrphanSampleLimit bounds how many orphaned entity IDs are captured
// for the report sample, mirroring rebuild-gold-labels' bounded diff sample.
const cleanupOrphanSampleLimit = 10

// cleanupOrphanEmbeddingsParams are the JSON parameters accepted by the op.
type cleanupOrphanEmbeddingsParams struct {
	// Apply, if true, deletes rows confirmed orphaned (book no longer exists).
	// Default false (dry-run/report only).
	Apply bool `json:"apply"`
}

// cleanupOrphanSample is one orphaned row surfaced in the report sample, so a
// reviewer can spot-check specific entity IDs before applying.
type cleanupOrphanSample struct {
	EntityID string
	Model    string
}

// cleanupOrphanReport is the full result of scanning emb:v:book:* rows:
// total/orphaned/live counts and a bounded sample of orphaned rows. OrphanIDs
// holds every orphaned entity ID found (not just the sample) so apply can
// delete them without a second scan pass.
type cleanupOrphanReport struct {
	Total     int
	Orphaned  int
	Live      int
	LookupErr int // GetBookByID returned an error; row skipped, never touched
	Sample    []cleanupOrphanSample
	OrphanIDs []string
}

func (r cleanupOrphanReport) summary() string {
	return fmt.Sprintf("total=%d orphaned=%d live=%d lookup_errors=%d", r.Total, r.Orphaned, r.Live, r.LookupErr)
}

// cleanupOrphanEmbeddingsHelp is the definition's description, HEAD's and the native
// kind's Help (rule R5: verbatim).
const cleanupOrphanEmbeddingsHelp = "Retroactive counterpart to PR #1802's DeleteBook fix: finds emb:v:book:* rows " +
	"whose referenced book no longer exists (deleted before #1802 stopped new orphans from " +
	"being created) and reports them. Dry-run by default; pass apply=true to delete only the " +
	"confirmed-orphaned rows. A row whose book still exists is never touched by this op, " +
	"regardless of that embedding's model/dimension. Idempotent: re-running apply after a " +
	"clean pass finds nothing left to delete."

// cleanupOrphanEmbeddingsDef returns the OperationDef for
// dedup.cleanup-orphan-embeddings.
func (p *Plugin) cleanupOrphanEmbeddingsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.cleanup-orphan-embeddings",
		Preview:         sdk.PreviewSpec{Params: cleanupOrphanEmbeddingsParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Clean up orphaned book embeddings",
		Description:     cleanupOrphanEmbeddingsHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.cleanup-orphan-embeddings",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runCleanupOrphanEmbeddings,
	}
}

// cleanupOrphanEmbeddingsKind is rule R3's native kind for
// dedup.cleanup-orphan-embeddings.
const cleanupOrphanEmbeddingsKind = tasks.Kind("books.dedup.cleanup-orphan-embeddings")

// cleanupOrphanEmbeddingsStore is the reads the core takes (rule R10): the book
// embeddings and the book lookup that decides whether one is orphaned. Neither
// writes.
type cleanupOrphanEmbeddingsStore interface {
	ListByType(entityType string) ([]database.Embedding, error)
	GetBookByID(id string) (*database.Book, error)
}

// cleanupOrphanEmbeddingsWriter is the one write the core makes: a point-delete
// of an orphaned embedding row (HEAD: EmbeddingStore.Delete).
type cleanupOrphanEmbeddingsWriter interface {
	Delete(entityType, entityID string) error
}

// cleanupOrphanEmbeddingsEnv is what the core takes from its shell (rules R2 and
// R10). Live is HEAD's apply=true (native: a Live run); a dry run never calls
// Writer, which may then be nil.
type cleanupOrphanEmbeddingsEnv struct {
	kindEnv
	Store  cleanupOrphanEmbeddingsStore
	Writer cleanupOrphanEmbeddingsWriter
}

// cleanupOrphanEmbeddingsResult is the native run's result: the scan's counts
// and what the delete pass did. HEAD stored no result, so its shell drops it.
type cleanupOrphanEmbeddingsResult struct {
	Total      int `json:"total"`
	Orphaned   int `json:"orphaned"`
	Live       int `json:"live"`
	LookupErr  int `json:"lookup_errors"`
	Deleted    int `json:"deleted"`
	DeleteErrs int `json:"delete_errors"`
}

// cleanupOrphanEmbeddingsCore is the body of runCleanupOrphanEmbeddings, shared
// by both shells.
func cleanupOrphanEmbeddingsCore(ctx context.Context, env cleanupOrphanEmbeddingsEnv) (cleanupOrphanEmbeddingsResult, error) {
	env.Logger.Info("cleanup-orphan-embeddings start", "apply", env.Live)

	env.Progress(0, 2, "Listing book embeddings…")
	embeddings, err := env.Store.ListByType("book")
	if err != nil {
		return cleanupOrphanEmbeddingsResult{}, fmt.Errorf("list book embeddings: %w", err)
	}
	env.Logger.Info("cleanup-orphan-embeddings: embeddings loaded", "count", len(embeddings))

	env.Progress(1, 2, fmt.Sprintf("Checking %d embeddings against live books…", len(embeddings)))
	report, err := scanOrphanEmbeddingsVia(ctx, env.kindEnv, env.Store, embeddings)
	if err != nil {
		return cleanupOrphanEmbeddingsResult{}, err
	}
	res := cleanupOrphanEmbeddingsResult{Total: report.Total, Orphaned: report.Orphaned, Live: report.Live, LookupErr: report.LookupErr}

	env.Logger.Info("cleanup-orphan-embeddings: scan complete", "stats", report.summary())
	for _, s := range report.Sample {
		env.Logger.Info("cleanup-orphan-embeddings: sample orphan", "entity_id", s.EntityID, "model", s.Model)
	}

	if !env.Live {
		env.Progress(2, 2, fmt.Sprintf(
			"Dry-run — %d orphaned book embeddings found (%d live, %d lookup errors, %d total). Pass apply=true to delete.",
			report.Orphaned, report.Live, report.LookupErr, report.Total))
		env.Logger.Info("cleanup-orphan-embeddings: dry-run only; nothing deleted")
		return res, nil
	}

	env.Progress(1, 2, fmt.Sprintf("Applying — deleting %d orphaned embeddings…", len(report.OrphanIDs)))
	// The delete loop stays sequential on purpose. Each iteration is one Pebble
	// point-delete on a row this op already proved orphaned — no lookup, no
	// network — so it is bounded by write throughput, not by CPU or read
	// latency, and fanning it out would only interleave writes into the same
	// keyspace for no wall-clock gain. Keeping the writes ordered also keeps
	// the deleted/deleteErrs tallies and the per-row error log trivially
	// consistent with the order OrphanIDs was reported in.
	var deleted, deleteErrs int
	for _, id := range report.OrphanIDs {
		if env.Canceled() {
			break
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}
		if err := env.Writer.Delete("book", id); err != nil {
			deleteErrs++
			env.Logger.Error("cleanup-orphan-embeddings: delete error", "entity_id", id, "error", err)
			continue
		}
		deleted++
	}
	res.Deleted, res.DeleteErrs = deleted, deleteErrs

	env.Progress(2, 2, fmt.Sprintf(
		"Complete — deleted %d/%d orphaned embeddings (%d errors). %s",
		deleted, len(report.OrphanIDs), deleteErrs, report.summary()))
	env.Logger.Info("cleanup-orphan-embeddings complete",
		"deleted", deleted, "delete_errs", deleteErrs, "summary", report.summary())
	return res, nil
}

// runCleanupOrphanEmbeddings is the v2 shell: HEAD's Run (params, nil checks and
// the result write, none) over cleanupOrphanEmbeddingsCore.
func (p *Plugin) runCleanupOrphanEmbeddings(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	var params cleanupOrphanEmbeddingsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	_, err := cleanupOrphanEmbeddingsCore(ctx, cleanupOrphanEmbeddingsEnv{
		kindEnv: headEnv(reporter, params.Apply),
		Store:   headEmbeddingReads{emb: p.embeddingStore, books: p.store},
		Writer:  headEmbeddingWriter{emb: p.embeddingStore},
	})
	return err
}

// orphanVerdict is the per-embedding outcome of one GetBookByID lookup.
type orphanVerdict uint8

const (
	verdictLive      orphanVerdict = iota // GetBookByID resolved a book
	verdictOrphaned                       // GetBookByID returned nil, nil — the book is gone
	verdictLookupErr                      // GetBookByID errored; row skipped, never touched
)

// scanOrphanEmbeddings is the pure(ish) core of the op: for every given
// embedding it checks whether GetBookByID still resolves the entity ID to a
// live book, and returns the full report — counts, a bounded sample, and the
// complete list of orphaned entity IDs ready for apply to delete. Split out
// from runCleanupOrphanEmbeddings so the scan itself (the deliverable of
// dry-run) is directly unit-testable. The only side effect is reads
// (GetBookByID) — no writes happen here.
//
// The lookups fan out over a runtime.NumCPU()-wide worker pool (DA-03): this
// is one synchronous point-lookup per emb:v:book:* row over the whole
// library, exactly the single-core shape CLAUDE.md's concurrency mandate
// exists to catch. Workers never share mutable state: each writes only its
// own slot in verdicts (a disjoint partition by index), and the report —
// counters, OrphanIDs, Sample — is assembled from those slots afterwards in
// input order, so the result is identical to what the sequential loop
// produced regardless of which worker finished first. The Label closure
// reads nothing shared either; RunItems invokes it inside the workers.
func (p *Plugin) scanOrphanEmbeddings(ctx context.Context, reporter sdk.Reporter, embeddings []database.Embedding) (cleanupOrphanReport, error) {
	return scanOrphanEmbeddingsVia(ctx, headEnv(reporter, false), headEmbeddingReads{emb: p.embeddingStore, books: p.store}, embeddings)
}

// scanOrphanEmbeddingsVia is scanOrphanEmbeddings over the env's hooks and a
// store handed in, so the native shell shares it (rule R10: no p.store).
func scanOrphanEmbeddingsVia(ctx context.Context, env kindEnv, store cleanupOrphanEmbeddingsStore, embeddings []database.Embedding) (cleanupOrphanReport, error) {
	verdicts := make([]orphanVerdict, len(embeddings))

	if err := env.Items(ctx, tasks.CPU(), len(embeddings), kindItemsOptions{
		PureLabel: func(i, total int) string {
			return fmt.Sprintf("Checking embedding %d/%d…", i+1, total)
		},
	}, func(_ context.Context, i int) error {
		if env.Canceled() {
			return context.Canceled
		}
		e := &embeddings[i]
		book, err := store.GetBookByID(e.EntityID)
		switch {
		case err != nil:
			verdicts[i] = verdictLookupErr
			env.Logger.Warn("cleanup-orphan-embeddings: GetBookByID error; skipping (leaving row untouched)",
				"entity_id", e.EntityID, "error", err)
		case book != nil:
			verdicts[i] = verdictLive
		default:
			verdicts[i] = verdictOrphaned
		}
		return nil
	}); err != nil {
		// The callback only fails for cancellation (lookup errors are counted,
		// never returned), and the pool wraps callback errors in errors.Join —
		// hand back the same bare sentinel the sequential loop returned.
		if ctx.Err() != nil {
			return cleanupOrphanReport{}, ctx.Err()
		}
		if env.Canceled() {
			return cleanupOrphanReport{}, context.Canceled
		}
		return cleanupOrphanReport{}, err
	}

	report := cleanupOrphanReport{Total: len(embeddings)}
	for i, v := range verdicts {
		switch v {
		case verdictLookupErr:
			report.LookupErr++
		case verdictLive:
			report.Live++
		default:
			e := &embeddings[i]
			report.Orphaned++
			report.OrphanIDs = append(report.OrphanIDs, e.EntityID)
			if len(report.Sample) < cleanupOrphanSampleLimit {
				report.Sample = append(report.Sample, cleanupOrphanSample{EntityID: e.EntityID, Model: e.Model})
			}
		}
	}
	return report, nil
}
