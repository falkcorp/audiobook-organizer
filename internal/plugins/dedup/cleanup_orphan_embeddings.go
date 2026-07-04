// file: internal/plugins/dedup/cleanup_orphan_embeddings.go
// version: 1.0.0
// guid: 9be49561-c03e-484c-ae53-723bbe299ebe
// last-edited: 2026-07-04

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

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// cleanupOrphanEmbeddingsDef returns the OperationDef for
// dedup.cleanup-orphan-embeddings.
func (p *Plugin) cleanupOrphanEmbeddingsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.cleanup-orphan-embeddings",
		Plugin:      "dedup",
		DisplayName: "Clean up orphaned book embeddings",
		Description: "Retroactive counterpart to PR #1802's DeleteBook fix: finds emb:v:book:* rows " +
			"whose referenced book no longer exists (deleted before #1802 stopped new orphans from " +
			"being created) and reports them. Dry-run by default; pass apply=true to delete only the " +
			"confirmed-orphaned rows. A row whose book still exists is never touched by this op, " +
			"regardless of that embedding's model/dimension. Idempotent: re-running apply after a " +
			"clean pass finds nothing left to delete.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.cleanup-orphan-embeddings",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runCleanupOrphanEmbeddings,
	}
}

// runCleanupOrphanEmbeddings implements the cleanup-orphan-embeddings op.
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
	reporter.Logger().Info("cleanup-orphan-embeddings start", "apply", params.Apply)

	_ = reporter.UpdateProgress(0, 2, "Listing book embeddings…")
	embeddings, err := p.embeddingStore.ListByType("book")
	if err != nil {
		return fmt.Errorf("list book embeddings: %w", err)
	}
	reporter.Logger().Info("cleanup-orphan-embeddings: embeddings loaded", "count", len(embeddings))

	_ = reporter.UpdateProgress(1, 2, fmt.Sprintf("Checking %d embeddings against live books…", len(embeddings)))
	report, err := p.scanOrphanEmbeddings(ctx, reporter, embeddings)
	if err != nil {
		return err
	}

	reporter.Logger().Info("cleanup-orphan-embeddings: scan complete", "stats", report.summary())
	for _, s := range report.Sample {
		reporter.Logger().Info("cleanup-orphan-embeddings: sample orphan", "entity_id", s.EntityID, "model", s.Model)
	}

	if !params.Apply {
		_ = reporter.UpdateProgress(2, 2, fmt.Sprintf(
			"Dry-run — %d orphaned book embeddings found (%d live, %d lookup errors, %d total). Pass apply=true to delete.",
			report.Orphaned, report.Live, report.LookupErr, report.Total))
		reporter.Logger().Info("cleanup-orphan-embeddings: dry-run only; nothing deleted")
		return nil
	}

	_ = reporter.UpdateProgress(1, 2, fmt.Sprintf("Applying — deleting %d orphaned embeddings…", len(report.OrphanIDs)))
	var deleted, deleteErrs int
	for _, id := range report.OrphanIDs {
		if reporter.IsCanceled() {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := p.embeddingStore.Delete("book", id); err != nil {
			deleteErrs++
			reporter.Logger().Error("cleanup-orphan-embeddings: delete error", "entity_id", id, "error", err)
			continue
		}
		deleted++
	}

	_ = reporter.UpdateProgress(2, 2, fmt.Sprintf(
		"Complete — deleted %d/%d orphaned embeddings (%d errors). %s",
		deleted, len(report.OrphanIDs), deleteErrs, report.summary()))
	reporter.Logger().Info("cleanup-orphan-embeddings complete",
		"deleted", deleted, "delete_errs", deleteErrs, "summary", report.summary())
	return nil
}

// scanOrphanEmbeddings is the pure(ish) core of the op: for every given
// embedding it checks whether GetBookByID still resolves the entity ID to a
// live book, and returns the full report — counts, a bounded sample, and the
// complete list of orphaned entity IDs ready for apply to delete. Split out
// from runCleanupOrphanEmbeddings so the scan itself (the deliverable of
// dry-run) is directly unit-testable. The only side effect is reads
// (GetBookByID) — no writes happen here.
func (p *Plugin) scanOrphanEmbeddings(ctx context.Context, reporter sdk.Reporter, embeddings []database.Embedding) (cleanupOrphanReport, error) {
	var report cleanupOrphanReport

	for i := range embeddings {
		if reporter.IsCanceled() {
			return cleanupOrphanReport{}, context.Canceled
		}
		select {
		case <-ctx.Done():
			return cleanupOrphanReport{}, ctx.Err()
		default:
		}

		e := embeddings[i]
		report.Total++
		if (i+1)%1000 == 0 {
			_ = reporter.UpdateProgress(1, 2, fmt.Sprintf("Checked %d/%d…", i+1, len(embeddings)))
		}

		book, err := p.store.GetBookByID(e.EntityID)
		if err != nil {
			report.LookupErr++
			reporter.Logger().Warn("cleanup-orphan-embeddings: GetBookByID error; skipping (leaving row untouched)",
				"entity_id", e.EntityID, "error", err)
			continue
		}
		if book != nil {
			report.Live++
			continue
		}

		report.Orphaned++
		report.OrphanIDs = append(report.OrphanIDs, e.EntityID)
		if len(report.Sample) < cleanupOrphanSampleLimit {
			report.Sample = append(report.Sample, cleanupOrphanSample{EntityID: e.EntityID, Model: e.Model})
		}
	}

	return report, nil
}
