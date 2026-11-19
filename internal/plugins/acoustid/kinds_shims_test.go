// file: internal/plugins/acoustid/kinds_shims_test.go
// version: 1.0.0
// guid: d98865a0-3440-40fc-8a64-56749cb0dfdb
// last-edited: 2026-11-19

// Test-only shims. The HEAD-named methods below used to be the production path;
// F5-05 moved the work into the *Via/*Core functions both the HEAD shell and the
// native kind call, which left these as test-only entry points. They live here so
// production keeps no dead code, and the older tests keep exercising the live path.

package acoustid

import (
	"context"
	"log/slog"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// backfillBook fingerprints the eligible files of one book, each in its own
// goroutine, and then refreshes the book signature once.
//
// The BOOK stays the unit RunItems schedules, because synthesizeBookSignatureForBook
// is a read-modify-write of the Book row and must run once, after every file of
// that book is done. The FILES fan out underneath it, bounded by fpSem — one
// semaphore shared by every book in the run, so total in-flight fpcalc stays at
// cap(fpSem) whether that is sixteen books with one file each or one book with
// a hundred chapters. Before 2026-09-12 a book's files ran sequentially inside
// one worker, so a many-chapter book pinned the run to a single fpcalc and
// parallelism collapsed near the end of a run (fingerprint timing note §6).
// Files of one book never share a row, so their UpdateBookFile writes are
// disjoint.
//
// Returns ctx.Err() when canceled so the page is not checkpointed; otherwise
// nil — one unreadable book must not abort a whole-library pass.
func (p *Plugin) backfillBook(ctx context.Context, b database.Book, tally *backfillTally, fpSem chan struct{}, logger *slog.Logger) error {
	return backfillBookVia(ctx, p.store, b, tally, fpSem, logger)
}

// backfillRunOptions builds the RunItems options for one page.
//
// A named function rather than a literal at the call site so that a test runs
// the real options instead of a hand-written copy of them: dropping Concurrency
// here is exactly the regression that made this op sequential for months.
func backfillRunOptions(offset, total int, tally *backfillTally) registry.RunItemsOptions {
	return registryItemsOptions(backfillParallelism(backfillWorkers()), backfillItemsOptions(offset, total, tally))
}

// backfillPages walks the book table one page at a time from the resume
// cursor, running each page through a RunItems pool and checkpointing the
// cursor once the page has fully drained. Split out of runBackfill so tests can
// drive it without a dedup engine or an fpcalc binary.
func (p *Plugin) backfillPages(ctx context.Context, reporter sdk.Reporter, state BackfillParams, tally *backfillTally) error {
	return backfillPagesCore(ctx, state, tally, p.headBackfillEnv(reporter))
}

// windowRunOptions builds the RunItems options for one tier. A named function
// so a test runs the real options: an omitted Concurrency is exactly the
// regression that left acoustid.backfill on one core for months.
//
// capMark lowers the watermark below any item a remote worker still owes
// (WorkerHub.checkpointMark): the server lane passes a leased item by skipping
// it, so the raw watermark can run ahead of work that is not done.
func windowRunOptions(workers, done, total, tier int, items []windowItem, params WindowBackfillParams,
	reporter sdk.Reporter, tally *windowTally, capMark func(int) int) registry.RunItemsOptions {
	return registryItemsOptions(windowParallelism(workers), windowItemsOptions(done, total, tier, items, params,
		func(state any) error { return reporter.Checkpoint(state) }, tally, capMark))
}

func (p *Plugin) planWindowBackfill(ctx context.Context, reporter sdk.Reporter, versions *fingerprint.ToolVersionInfo) (*windowPlan, error) {
	return planWindowBackfillVia(ctx, p.store, headItems(reporter), p.windowHooks(WindowKindDeps{}), versions)
}

// countWriteErr separates a row deleted mid-run from a real write failure,
// which fails the run at the end rather than passing as success. The store
// resolves the row by FILE ID and says so with ErrFingerprintWindowRowGone, so
// a row that moved to another book is never mistaken for a gone one.
func (p *Plugin) countWriteErr(it windowItem, err error, t *windowTally) {
	countWriteErrVia(it, err, t)
}

// attach makes run the live run the endpoints serve and starts the sweeper.
// The op's ConcurrencyKey guarantees one live run at a time.
//
// In a remote-only run wt carries only Versions, the reference pair: the
// equivalence class and the lease allowlist are built from it, and the
// server's own pair (never resolved) is in neither.
func (h *WorkerHub) attach(ctx context.Context, p *Plugin, wt fingerprint.WindowTools, tally *windowTally, calib []windowItem, mode hubMode) {
	h.attachVia(ctx, hubRunDeps{store: p.store, writer: headKindWriter{store: p.store}, probe: p.currentScanProbe()}, wt, tally, calib, mode)
}

// lshBackfillConcurrency is the size of the worker pool the core hands its
// item loop. One row per item and the work is a DB read plus a DB write
// (CPU for the index hook), so the pool is sized to the cores. It is a function,
// not an inline expression, so the cancel test can derive its guaranteed bound
// (cancel point + in-flight workers) from the same value the op uses: it reads
// the declared parallelism, runtime.NumCPU() for tasks.CPU().
func lshBackfillConcurrency() int { return lshBackfillParallelism().Workers() }
