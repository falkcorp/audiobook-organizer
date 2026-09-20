// file: internal/plugins/maintenance/bookfile_batch_write.go
// version: 1.0.0
// guid: 3f0a9c61-8d24-4b17-9e35-c1a7f2b64d08
// last-edited: 2026-09-19

// Package maintenance — shared plumbing for ops that rewrite MANY book_file
// rows of the SAME book.
//
// WHY THIS FILE EXISTS. database.UpdateBookFile is a full-record replacement
// that recomputes its book's aggregates on EVERY call, and
// RecomputeBookAggregates re-reads every row of the book. An op that walks a
// flat list of rows and calls UpdateBookFile per row therefore pays one full
// re-read of the whole book per row: O(n^2) in the book's file count. On
// 2026-09-19 maintenance.duration-reextract spent ~25 minutes on a single
// 1,494-file book that way and was killed by the stuck-op watchdog (#3480).
//
// The per-row rehydration in those same ops is the SECOND O(n^2) leg: each row
// called GetBookFiles(bookID) to rebuild the full record, reading all n rows to
// use one of them.
//
// Both legs are fixed by the same move: partition the flat work list into
// per-book groups, hand the GROUPS to the worker pool, and per group do one
// GetBookFiles and one UpdateBookFiles. Partitioning by book is also what makes
// the pool safe here — two workers can never touch rows of the same book, so
// they can never race each other's aggregate recompute.

package maintenance

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// bookFileBatchProgressInterval throttles the in-book progress line. Liveness
// is stamped on EVERY row (see bookFileBatchOpts.Reporter); this only paces the
// human-readable message.
const bookFileBatchProgressInterval = 10 * time.Second

// bookFileBatchGroup is one book's share of a flat work list.
type bookFileBatchGroup[T any] struct {
	BookID string
	Items  []T
}

// groupItemsByBook partitions items into one group per BookID, preserving the
// caller's item order inside each group.
//
// The groups themselves are sorted by their first item's sort key so a run's
// execution and reporting order is deterministic: the callers sort their flat
// list by file ID precisely so a capped re-run takes a stable prefix, and map
// iteration order would have thrown that away.
func groupItemsByBook[T any](items []T, bookID func(T) string, sortKey func(T) string) []bookFileBatchGroup[T] {
	if len(items) == 0 {
		return nil
	}
	idx := make(map[string]int, len(items))
	groups := make([]bookFileBatchGroup[T], 0, len(items))
	for _, it := range items {
		id := bookID(it)
		if at, ok := idx[id]; ok {
			groups[at].Items = append(groups[at].Items, it)
			continue
		}
		idx[id] = len(groups)
		groups = append(groups, bookFileBatchGroup[T]{BookID: id, Items: []T{it}})
	}
	sort.SliceStable(groups, func(a, b int) bool {
		return sortKey(groups[a].Items[0]) < sortKey(groups[b].Items[0])
	})
	return groups
}

// bookFileBatchWriter is the one store method these ops need for the write.
type bookFileBatchWriter interface {
	UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error)
}

// bookFileWriteOutcome is one book's batch write, classified into the counters
// a maintenance op reports.
type bookFileWriteOutcome struct {
	// Applied is the number of rows that were committed. A row whose fsync
	// could not be confirmed (database.ErrBookFileDurabilityUnknown) IS
	// counted here — its bytes are in the store — and is ALSO counted in
	// RowErrs. The per-row loops this replaced counted such a row as an error
	// only, which under-reported a write that actually landed.
	Applied int
	// RowErrs is the number of rows whose write reported an error.
	RowErrs int
	// RecomputeErrs is the number of books whose aggregate recompute failed
	// AFTER their rows committed. Deliberately NOT folded into an op's
	// "update errors": the rows were written; only the book's derived totals
	// are stale, and a caller that reported it as a failed row write would be
	// telling a person to re-run rows that are already correct.
	RecomputeErrs int
	// Cancelled is true when the batch stopped early because its context was
	// done — either the operation was cancelled or the caller aborted this
	// book (bookFileBatchOpts.Abort). Rows never attempted are in neither
	// Applied nor RowErrs.
	Cancelled bool
}

// classifyBookFileWrites turns UpdateBookFiles' (written, joined error) pair
// into a bookFileWriteOutcome.
//
// UpdateBookFiles joins its errors, so the joined value exposes
// Unwrap() []error and each element is exactly one row failure, one book's
// recompute failure, or the context error.
func classifyBookFileWrites(written int, err error) bookFileWriteOutcome {
	out := bookFileWriteOutcome{Applied: written}
	for _, e := range flattenJoinedErrs(err) {
		switch {
		case errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
			out.Cancelled = true
		case errors.Is(e, database.ErrBookAggregatesRecompute):
			out.RecomputeErrs++
		default:
			out.RowErrs++
		}
	}
	return out
}

// flattenJoinedErrs returns the elements of an errors.Join value, or the error
// itself when it is not a join. One level is enough: UpdateBookFiles joins a
// flat slice.
func flattenJoinedErrs(err error) []error {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// bookFileBatchOpts configures writeBookFileBatch.
type bookFileBatchOpts struct {
	// Reporter receives a liveness stamp after EVERY row. Without it a book
	// with thousands of rows would look frozen to the stuck-op watchdog for
	// the whole batch, because registry.RunItems stamps once per ITEM and the
	// item is now a whole book.
	Reporter sdk.Reporter

	// Progress, when non-nil, renders the progress line for row done of total
	// in this book. It is called at most once per bookFileBatchProgressInterval.
	Progress func(done, total int) (current, progressTotal int, msg string)

	// Abort, when non-nil and returning true, stops the rest of THIS book's
	// batch. The rows already written stay written and the book's aggregates
	// are still recomputed, so the book agrees with its committed rows. It
	// exists for the scan stand-down lease: a lapsed lease must stop the write
	// mid-book, not only at the next book boundary.
	Abort func() bool

	// OnRow, when non-nil, is called after each row attempt with the row's
	// index in the slice handed to writeBookFileBatch and whether it was
	// applied, so a caller can bucket its own per-row counters.
	OnRow func(i int, applied bool)
}

// writeBookFileBatch writes rows — all belonging to ONE book — through a single
// UpdateBookFiles call, so the book's aggregates are recomputed once after the
// rows rather than once per row.
//
// The batch runs under its own cancellable child of ctx: opts.Abort cancels
// only this book, leaving the caller's ctx (and therefore the rest of the run)
// alone. A cancelled operation still cancels the batch through ctx, which is
// the correct behaviour here — none of these ops has post-commit work that must
// outlive a cancel, and UpdateBookFiles already recomputes the aggregates of
// the books it wrote before returning, so a cancelled batch never leaves a book
// disagreeing with its own rows.
func writeBookFileBatch(ctx context.Context, store bookFileBatchWriter, rows []*database.BookFile, opts bookFileBatchOpts) bookFileWriteOutcome {
	if len(rows) == 0 {
		return bookFileWriteOutcome{}
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := 0
	lastProgress := time.Now()
	written, err := store.UpdateBookFiles(batchCtx, rows, func(i int, applied bool) {
		done++
		if opts.Reporter != nil {
			registry.TouchLiveness(opts.Reporter)
		}
		if opts.OnRow != nil {
			opts.OnRow(i, applied)
		}
		if opts.Progress != nil && opts.Reporter != nil && time.Since(lastProgress) >= bookFileBatchProgressInterval {
			cur, tot, msg := opts.Progress(done, len(rows))
			_ = opts.Reporter.UpdateProgress(cur, tot, msg)
			lastProgress = time.Now()
		}
		// Checked AFTER the row, so the abort stops the NEXT row rather than
		// discarding work already committed.
		if opts.Abort != nil && opts.Abort() {
			cancel()
		}
	})
	return classifyBookFileWrites(written, err)
}
