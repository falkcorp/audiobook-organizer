// file: internal/plugins/dedup/cleanup_orphan_author_embeddings.go
// version: 1.0.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-07-08

// Package dedup — op dedup.cleanup-orphan-author-embeddings.
//
// Author-side counterpart to dedup.cleanup-orphan-embeddings (books, PR #1802
// follow-up). PR #1866's HydrateChromem model-mismatch guard stopped these
// rows from spamming a dimension-mismatch warning on every restart, but it
// only skips them — the dead rows stay in the embedding table forever. This
// op is the retroactive cleanup: it finds emb:v:author:* rows whose author ID
// no longer exists as a live author (merged into another author, or hard
// deleted) and reports/deletes them.
//
// # Why this can't reuse the book op's GetBookByID pattern
//
// The book op treats "GetBookByID returns nil" as the orphan signal. For
// authors that check is unsound: PebbleStore.GetAuthorByID follows a
// tombstone redirect for merged authors — GetAuthorByID(oldID) returns the
// CANONICAL author's data (non-nil), not nil, so a merged-away ID would look
// "live" and never get flagged. That silent redirect is exactly what let 3
// orphaned rows (authorIDs 39755, 40861, 42076) survive indefinitely: they
// were excluded from GetAllAuthors() (which iterates literal author:N Pebble
// keys, no tombstone-following) yet still had an embedding row, discovered
// while chasing a residual `dedup chromem upsert author` warning through
// PRs #1862/#1865/#1866.
//
// This op instead builds its "is this ID live" set directly from
// GetAllAuthors() — the same literal-key enumeration that HydrateChromem's
// author loop and the embedding backfill's author loop both rely on — so an
// ID is orphaned here if and only if it's exactly the condition that makes it
// unreachable by every other author-embedding code path in the system.
//
//   - Dry-run (default): reports orphaned vs. live counts and a bounded
//     sample of orphaned entity IDs + their stored .Model.
//   - Apply (apply=true): deletes ONLY the rows confirmed orphaned. A row
//     whose author still exists (by literal ID) is never touched, regardless
//     of that embedding's model/dimension — a live author's stale-model
//     embedding is in scope for a future author-side re-embed, not this op.
//
// Idempotent: apply only ever deletes rows it can currently prove orphaned,
// so a second apply run finds nothing left to delete.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// cleanupOrphanAuthorSampleLimit bounds how many orphaned entity IDs are
// captured for the report sample, mirroring the book-side op's sample limit.
const cleanupOrphanAuthorSampleLimit = 10

// cleanupOrphanAuthorEmbeddingsParams are the JSON parameters accepted by the op.
type cleanupOrphanAuthorEmbeddingsParams struct {
	// Apply, if true, deletes rows confirmed orphaned (author no longer
	// exists). Default false (dry-run/report only).
	Apply bool `json:"apply"`
}

// cleanupOrphanAuthorSample is one orphaned row surfaced in the report
// sample, so a reviewer can spot-check specific entity IDs before applying.
type cleanupOrphanAuthorSample struct {
	EntityID string
	Model    string
}

// cleanupOrphanAuthorReport is the full result of scanning emb:v:author:*
// rows against the live-author-ID set: total/orphaned/live counts and a
// bounded sample of orphaned rows. OrphanIDs holds every orphaned entity ID
// found (not just the sample) so apply can delete them without a second scan.
type cleanupOrphanAuthorReport struct {
	Total     int
	Orphaned  int
	Live      int
	Sample    []cleanupOrphanAuthorSample
	OrphanIDs []string
}

func (r cleanupOrphanAuthorReport) summary() string {
	return fmt.Sprintf("total=%d orphaned=%d live=%d", r.Total, r.Orphaned, r.Live)
}

// cleanupOrphanAuthorEmbeddingsDef returns the OperationDef for
// dedup.cleanup-orphan-author-embeddings.
func (p *Plugin) cleanupOrphanAuthorEmbeddingsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.cleanup-orphan-author-embeddings",
		Plugin:      "dedup",
		DisplayName: "Clean up orphaned author embeddings",
		Description: "Author-side counterpart to dedup.cleanup-orphan-embeddings (books): finds " +
			"emb:v:author:* rows whose author ID no longer exists as a live author (merged into " +
			"another author or hard-deleted) and reports them. Existence is checked against " +
			"GetAllAuthors(), not GetAuthorByID, because GetAuthorByID follows tombstone redirects " +
			"for merged authors and would misreport a merged-away ID as live. Dry-run by default; " +
			"pass apply=true to delete only the confirmed-orphaned rows. Idempotent: re-running " +
			"apply after a clean pass finds nothing left to delete.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.cleanup-orphan-author-embeddings",
		Cancellable:     true,
		Timeout:         10 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runCleanupOrphanAuthorEmbeddings,
	}
}

// runCleanupOrphanAuthorEmbeddings implements the
// cleanup-orphan-author-embeddings op.
func (p *Plugin) runCleanupOrphanAuthorEmbeddings(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	var params cleanupOrphanAuthorEmbeddingsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	reporter.Logger().Info("cleanup-orphan-author-embeddings start", "apply", params.Apply)

	_ = reporter.UpdateProgress(0, 3, "Listing author embeddings…")
	embeddings, err := p.embeddingStore.ListByType("author")
	if err != nil {
		return fmt.Errorf("list author embeddings: %w", err)
	}

	_ = reporter.UpdateProgress(1, 3, "Loading live author IDs…")
	authors, err := p.store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("get all authors: %w", err)
	}
	liveIDs := make(map[string]struct{}, len(authors))
	for _, a := range authors {
		liveIDs[strconv.Itoa(a.ID)] = struct{}{}
	}

	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf(
		"Checking %d embeddings against %d live authors…", len(embeddings), len(liveIDs)))
	report := scanOrphanAuthorEmbeddings(embeddings, liveIDs)

	reporter.Logger().Info("cleanup-orphan-author-embeddings: scan complete", "stats", report.summary())
	for _, s := range report.Sample {
		reporter.Logger().Info("cleanup-orphan-author-embeddings: sample orphan", "entity_id", s.EntityID, "model", s.Model)
	}

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, fmt.Sprintf(
			"Dry-run — %d orphaned author embeddings found (%d live, %d total). Pass apply=true to delete.",
			report.Orphaned, report.Live, report.Total))
		reporter.Logger().Info("cleanup-orphan-author-embeddings: dry-run only; nothing deleted")
		return nil
	}

	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Applying — deleting %d orphaned embeddings…", len(report.OrphanIDs)))
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
		if err := p.embeddingStore.Delete("author", id); err != nil {
			deleteErrs++
			reporter.Logger().Error("cleanup-orphan-author-embeddings: delete error", "entity_id", id, "error", err)
			continue
		}
		deleted++
	}

	_ = reporter.UpdateProgress(3, 3, fmt.Sprintf(
		"Complete — deleted %d/%d orphaned embeddings (%d errors). %s",
		deleted, len(report.OrphanIDs), deleteErrs, report.summary()))
	reporter.Logger().Info("cleanup-orphan-author-embeddings complete",
		"deleted", deleted, "delete_errs", deleteErrs, "summary", report.summary())
	return nil
}

// scanOrphanAuthorEmbeddings is the pure core of the op: for every given
// embedding it checks whether the entity ID is present in liveIDs (built
// from GetAllAuthors(), the literal-key enumeration every other author-
// embedding code path relies on), and returns the full report — counts, a
// bounded sample, and the complete list of orphaned entity IDs ready for
// apply to delete. Pure/no I/O (liveIDs is precomputed), so no context or
// cancellation plumbing is needed here, unlike the book op's per-row
// GetBookByID lookups.
func scanOrphanAuthorEmbeddings(embeddings []database.Embedding, liveIDs map[string]struct{}) cleanupOrphanAuthorReport {
	var report cleanupOrphanAuthorReport
	for _, e := range embeddings {
		report.Total++
		if _, ok := liveIDs[e.EntityID]; ok {
			report.Live++
			continue
		}
		report.Orphaned++
		report.OrphanIDs = append(report.OrphanIDs, e.EntityID)
		if len(report.Sample) < cleanupOrphanAuthorSampleLimit {
			report.Sample = append(report.Sample, cleanupOrphanAuthorSample{EntityID: e.EntityID, Model: e.Model})
		}
	}
	return report
}
