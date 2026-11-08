// file: internal/dedup/ann_reconcile.go
// version: 1.0.0
// guid: 0c209429-4bf6-4dfd-80cf-3f71a93939fa
// last-edited: 2026-11-08

// Bringing a stale vector ANN snapshot up to date at boot (review B2).
//
// NewServer imports the HNSW snapshot before PostInit. When the Pebble
// embedding rows moved since that snapshot was exported (an unclean shutdown
// loses every write since the last periodic export), the store installs it
// pending a reconcile instead of discarding it. PostInit then runs
// runANNReconcile in the background: every embedding row is classified with
// the hydrate rules (classifyBookRow, classifyAuthorRow), and the store diffs
// the verdicts against the graph (database.HNSWEmbeddingStore.Reconcile). A
// full rebuild happens only when the store asks for one, and the log names the
// keys that forced it.

package dedup

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// annReconcileKeySample is how many of the keys that forced a rebuild the log
// line names; the rest are counted.
const annReconcileKeySample = 10

// annClassifyStats counts what classifying the rows for a reconcile found.
type annClassifyStats struct {
	bookRows          int
	authorRows        int
	bookLookupErrors  int
	firstLookupError  error
	bookUndecodable   int
	authorUndecodable int
}

// classifyForReconcile reads every "book" and "author" embedding row and
// classifies it with the hydrate rules.
//
// The book rows are classified on a bounded worker pool (runtime.NumCPU()):
// each one is a Pebble book lookup, and the production library has about 40K
// rows. Each worker writes only its own slot of the result slice, so the slice
// needs no lock; the lookup-error counter does. Author rows need no lookup and
// are classified in line.
func (de *Engine) classifyForReconcile(ctx context.Context) (map[string][]database.ReconcileRow, annClassifyStats, error) {
	var st annClassifyStats
	books, undecodableBooks, err := de.embedStore.ListByTypeCounted("book")
	if err != nil {
		return nil, st, fmt.Errorf("list book embeddings: %w", err)
	}
	st.bookRows, st.bookUndecodable = len(books), undecodableBooks

	bookRows := make([]database.ReconcileRow, len(books))
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i := range books {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			e := books[i]
			row := database.ReconcileRow{ID: e.EntityID, Vector: e.Vector}
			verdict, book, lerr := de.classifyBookRow(e)
			switch verdict {
			case annIndex:
				row.Verdict, row.Meta = database.ReconcileIndex, bookANNMeta(book)
			case annLookupError:
				row.Verdict = database.ReconcileUnknown
				mu.Lock()
				st.bookLookupErrors++
				if st.firstLookupError == nil {
					st.firstLookupError = lerr
				}
				mu.Unlock()
			default:
				row.Verdict = database.ReconcileSkip
			}
			bookRows[i] = row
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, st, err
	}

	authors, undecodableAuthors, err := de.embedStore.ListByTypeCounted("author")
	if err != nil {
		return nil, st, fmt.Errorf("list author embeddings: %w", err)
	}
	st.authorRows, st.authorUndecodable = len(authors), undecodableAuthors
	authorRows := make([]database.ReconcileRow, len(authors))
	for i, e := range authors {
		row := database.ReconcileRow{ID: e.EntityID, Vector: e.Vector, Verdict: database.ReconcileSkip}
		if de.classifyAuthorRow(e) == annIndex {
			row.Verdict = database.ReconcileIndex
		}
		authorRows[i] = row
	}
	return map[string][]database.ReconcileRow{"book": bookRows, "author": authorRows}, st, nil
}

// runANNReconcile is the PostInit goroutine body for a stale snapshot. It
// reconciles the snapshot in place, and falls back to the full rebuild
// (DiscardForRebuild, then runHydrate) only when the store cannot be
// reconciled: a changed vector, a damaged graph, too many tombstones, an
// entity type the rows do not cover, or a failure to read the rows. A run cut
// short by shutdown leaves the store incomplete; the snapshot on disk is the
// older one, and the next boot reconciles it again.
func (de *Engine) runANNReconcile(bgCtx context.Context, r database.SnapshotReconciler, completeness database.SnapshotCompleteness) {
	ctx, cancel := context.WithTimeout(bgCtx, 30*time.Minute)
	defer cancel()
	start := time.Now()

	var (
		report database.ReconcileReport
		reason string
	)
	if de.embedStore == nil {
		reason = "no embedding store to reconcile against"
	} else {
		truth, st, err := de.classifyForReconcile(ctx)
		if err == nil {
			report, err = r.Reconcile(ctx, truth)
		}
		switch {
		case err != nil && ctx.Err() != nil:
			lifecycleLog.Warn("vector ANN snapshot reconcile was cut short; the store stays incomplete and no snapshot is exported: %v", err)
			return
		case err != nil:
			reason = err.Error()
		case report.RebuildReason != "":
			reason = report.RebuildReason
		default:
			lifecycleLog.Info("vector ANN snapshot reconciled against Pebble in %s: %s (book_rows=%d book_rows_undecodable=%d author_rows=%d author_rows_undecodable=%d)",
				time.Since(start).Round(time.Millisecond), formatReconcileCounts(report),
				st.bookRows, st.bookUndecodable, st.authorRows, st.authorUndecodable)
			if !report.Complete {
				lifecycleLog.Warn("vector ANN store stays marked incomplete; no snapshot will be exported until it is rebuilt: %s",
					reconcileIncompleteReason(report, st))
			}
			return
		}
	}

	lifecycleLog.Warn("vector ANN snapshot cannot be reconciled in place (%s); rebuilding it from Pebble%s",
		reason, formatKeySample(report.Changed))
	r.DiscardForRebuild()
	de.runHydrate(bgCtx, completeness)
}

// formatReconcileCounts renders every entity type's counts on one line.
func formatReconcileCounts(report database.ReconcileReport) string {
	parts := make([]string, 0, len(report.ByType))
	for _, entityType := range []string{"book", "author"} {
		c, ok := report.ByType[entityType]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s rows=%d kept=%d added=%d restored=%d meta_refreshed=%d tombstoned=%d skipped=%d live_writes=%d unknown=%d",
			entityType, c.Rows, c.Kept, c.Added, c.Restored, c.MetaRefreshed, c.Tombstoned, c.Skipped, c.LiveWrites, c.Unknown))
	}
	return strings.Join(parts, "; ")
}

// formatKeySample names the first annReconcileKeySample keys and counts the
// rest, or returns "" for none.
func formatKeySample(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	shown := keys[:min(len(keys), annReconcileKeySample)]
	out := fmt.Sprintf("; keys: %s", strings.Join(shown, ", "))
	if rest := len(keys) - len(shown); rest > 0 {
		out += fmt.Sprintf(" and %d more", rest)
	}
	return out
}

// reconcileIncompleteReason says why a finished reconcile left the store
// incomplete, mirroring hydrateIncompleteReason: rows whose book lookup failed
// were neither added nor dropped, and rows that should have been added were
// not.
func reconcileIncompleteReason(report database.ReconcileReport, st annClassifyStats) string {
	var reasons []string
	if st.bookLookupErrors > 0 {
		reasons = append(reasons, fmt.Sprintf("%d book lookup error(s), first: %v", st.bookLookupErrors, st.firstLookupError))
	}
	if report.AddErrors > 0 {
		reasons = append(reasons, fmt.Sprintf("%d row(s) failed to add, first: %v", report.AddErrors, report.FirstAddError))
	}
	if len(reasons) == 0 {
		return "the reconcile did not finish cleanly"
	}
	return strings.Join(reasons, "; ")
}
