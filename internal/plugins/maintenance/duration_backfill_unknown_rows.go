// file: internal/plugins/maintenance/duration_backfill_unknown_rows.go
// version: 1.0.0
// guid: 30470edd-3683-43f8-963e-07e058ba2a3e
// last-edited: 2026-11-05

package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Row-grain mode of maintenance.duration-backfill: mode "unknownRows".
//
// The book-grain modes miss the rows this one exists for. SplitOwnFolderFiles
// drops copies, ZeroRowsOnly is scoped to books ABS lists, and the duplicate
// rows of a megarow are exactly what both skip. A held folder-books group, or a
// "possible bad file" nobody ever probed, is a ROW problem, so this mode walks
// book_file rows in any book.
//
// Scope. A row is a candidate when its DurationState is Unprobed or ProbeFailed,
// or Unreadable with a TIMEOUT as the last error (a slow mount is not a bad
// file; the same ceiling rule the book-grain loop applies). A row that is
// Unreadable for any other reason is counted and left alone until an explicit
// ResetDuration, and a row marked Missing is not looked at. Copies and
// books/itunes/** rows are included: probing is a header READ, and the write is
// a DB-only row update; no file is opened for writing, moved or touched.
//
// Pass 1 is DB only: a sibling borrow (database.KnownDurationAtPath, the rule
// the scanner and folder-books share), then AcoustIDFingerprintDurationSec, the
// first source the book-grain loop uses. The donor index is built from the WHOLE
// library, never from the narrowed candidate set: the donor that settles a
// held folder-books group is the OTHER book's row at the same path, and a
// fileIds narrowing to the unknown rows would otherwise hide it and probe every
// file.
//
// Pass 2 runs ffprobe through registry.RunItems with Concurrency set EXPLICITLY
// (RunItems is sequential unless told otherwise). Outcomes are classified by
// bookfileaudio.ClassifyProbeError, the one classifier every duration writer
// shares: a file failure or timeout is an attempt on the row, an environment
// failure (ffprobe missing, file absent, permission, stale mount) records
// nothing.
//
// Writes. Every row goes through ModifyBookFileDeferAggregates rather than a new
// store method: it already reads the row under its stripe, hands a copy to the
// callback, writes through the compare-and-swap commit (re-running the callback
// on a fresh copy when a concurrent writer wins) and honours
// ErrSkipBookFileWrite, which is the whole RecordDurationProbe contract. It is
// also what recordSegmentProbeFailures uses, so the two cannot disagree. The
// deferral is the point: a book's aggregates are recomputed ONCE per book, after
// both passes, and only for books where a Duration was set. A failure record
// moves nothing an aggregate reads.
//
// Dry run (the default) runs pass 1's decisions and reports what pass 2 would
// probe (would_probe) but does NOT run ffprobe: a preview that shells out per
// file over a library-scale population is hours of wall clock that writes
// nothing. probed_known and probe_failed are therefore 0 in a dry run.

// durationModeUnknownRows is the Mode value that selects this row-grain run.
const durationModeUnknownRows = "unknownRows"

const (
	unknownRowsDefaultWorkers = 4
	unknownRowsMaxWorkers     = 16
	// unknownRowsBookReportCap bounds the per-book breakdown a dry run puts in
	// its result: the books with the most rows to act on come first.
	unknownRowsBookReportCap = 100
)

// unknownRowsProbe is the header read of pass 2: extractWithTimeout in
// production. A variable so a test can observe or interleave with a probe (prove
// two are in flight at once, change a row while its probe runs) while the shell
// stub still runs underneath. Only this mode uses it.
var unknownRowsProbe = extractWithTimeout

// unknownRowsConcurrency is the pass-2 pool size: workers clamped to [1, 16],
// 4 when unset. The same bound the book-grain loop applies to its ffprobe
// subprocesses.
func unknownRowsConcurrency(workers int) int {
	if workers < 1 {
		return unknownRowsDefaultWorkers
	}
	return min(workers, unknownRowsMaxWorkers)
}

// validateUnknownRowsParams rejects every param combination that would
// otherwise be silently misread: encoding/json drops a key it does not know, so
// fileIds without the mode would become a whole-library book-grain run that
// writes.
func validateUnknownRowsParams(p durationReextractParams) error {
	switch p.Mode {
	case "", durationModeUnknownRows:
	default:
		return fmt.Errorf("invalid params: mode %q is not recognised (the only mode is %q)", p.Mode, durationModeUnknownRows)
	}
	if p.Mode != durationModeUnknownRows {
		return fmt.Errorf("invalid params: fileIds / file_ids is only valid with mode=%q", durationModeUnknownRows)
	}
	if p.ZeroRowsOnly || p.ZeroRowsOnlySnake {
		return fmt.Errorf("invalid params: mode=%q and zeroRowsOnly are different runs; send one", durationModeUnknownRows)
	}
	return nil
}

// UnknownRowsBookCounts is one book's share of a dry run.
type UnknownRowsBookCounts struct {
	BookID                  string `json:"book_id"`
	Rows                    int    `json:"rows"`
	Borrowed                int    `json:"borrowed"`
	PromotedFromFingerprint int    `json:"promoted_from_fingerprint"`
	WouldProbe              int    `json:"would_probe"`
	SkippedUnreadable       int    `json:"skipped_unreadable"`
}

// UnknownRowsResult is the op result of an unknownRows run.
//
// In an apply run, Scanned equals Borrowed + PromotedFromFingerprint +
// ProbedKnown + ProbeFailed + EnvironmentSkipped + SkippedChanged +
// SkippedUnreadable + SkippedNoPath + WriteErrors. In a dry run it equals
// Borrowed + PromotedFromFingerprint + WouldProbe + SkippedUnreadable +
// SkippedNoPath (the first two read "would be").
type UnknownRowsResult struct {
	DryRun bool `json:"dry_run"`
	// Scanned is the candidate rows examined: unmeasured rows, present, inside
	// the fileIds / bookIds narrowing, up to limit. Known and Missing rows are
	// not in it.
	Scanned int `json:"scanned"`
	// Borrowed: took the known duration of a sibling row at the same path.
	Borrowed int `json:"borrowed"`
	// PromotedFromFingerprint: took AcoustIDFingerprintDurationSec.
	PromotedFromFingerprint int `json:"promoted_from_fingerprint"`
	// ProbedKnown: ffprobe measured it.
	ProbedKnown int `json:"probed_known"`
	// ProbeFailed: ffprobe ran on the present file and failed; one more attempt
	// was recorded on the row.
	ProbeFailed int `json:"probe_failed"`
	// EnvironmentSkipped: the probe could not run or the file could not be
	// reached (ffprobe missing, file absent, permission, stale mount, an
	// extension that is not audio). Nothing was recorded.
	EnvironmentSkipped int `json:"environment_skipped"`
	// SkippedChanged: the row changed between the walk and the write (moved,
	// rewritten, marked missing, retired, measured by someone else) or is gone.
	SkippedChanged int `json:"skipped_changed"`
	// SkippedUnreadable: Unreadable for a reason other than a timeout.
	SkippedUnreadable int `json:"skipped_unreadable"`

	// WouldProbe is the rows pass 2 would probe: a dry run only.
	WouldProbe int `json:"would_probe,omitempty"`
	// SkippedNoPath: the row has no file path.
	SkippedNoPath int `json:"skipped_no_path,omitempty"`
	// SkippedMissing: rows marked Missing that no mode looks at. Not in Scanned.
	SkippedMissing int `json:"skipped_missing,omitempty"`
	// NotFound: fileIds that match no book_file row (a row merged away since the
	// caller read it). Reported, never an error.
	NotFound int `json:"not_found,omitempty"`
	// WriteErrors: rows whose write failed.
	WriteErrors int `json:"write_errors,omitempty"`
	// BooksRecomputed: books whose aggregates were recomputed (those where a
	// Duration was set), once each.
	BooksRecomputed int `json:"books_recomputed,omitempty"`

	// Books is the per-book breakdown, in a dry run only, the busiest
	// unknownRowsBookReportCap books first; BooksOmitted is the rest.
	Books        []UnknownRowsBookCounts `json:"books,omitempty"`
	BooksOmitted int                     `json:"books_omitted,omitempty"`
}

// unknownRowsCounters are the live counters of an apply run. They are written
// by worker goroutines and read by RunItems' Label closure (also a worker
// goroutine), so each is atomic.
type unknownRowsCounters struct {
	borrowed, promoted, probedKnown, probeFailed atomic.Int64
	envSkipped, changed, writeErrs               atomic.Int64
	// done counts items finished in the current pass, for the progress label.
	done atomic.Int64
}

type unknownRowSource int

const (
	srcBorrow unknownRowSource = iota
	srcFingerprint
	srcProbe
)

// unknownRowWork is one row's decided action. sec is the duration to write for
// srcBorrow / srcFingerprint; a srcProbe row is measured by pass 2.
type unknownRowWork struct {
	row database.BookFileCore
	sec int
	src unknownRowSource
}

// stillTheProbedRow reports whether cur is still the row the walk saw, in every
// respect a duration or a failure record depends on: not measured since, not
// marked missing or retired, same path, and the same bytes (hash and size: a
// failure of the OLD bytes says nothing about the new, and a borrowed duration
// was matched on size).
func stillTheProbedRow(cur *database.BookFile, seen database.BookFileCore) bool {
	return cur.Duration == nil && !cur.Missing && !cur.Retired &&
		cur.FilePath == seen.FilePath && cur.FileHash == seen.FileHash && cur.FileSize == seen.FileSize
}

// writeUnknownRow applies mutate to the stored row under its write lock, but
// only while the stored row is still the one the walk saw. wrote is false for a
// row that changed or vanished (the caller counts skipped_changed). The callback
// may run more than once for one row (a retried compare-and-swap), so each run
// starts from "not written", and mutate runs on the FRESH copy, never on the
// walked one: a failure's attempt count must grow from the stored count.
func writeUnknownRow(store segmentFailureWriter, seen database.BookFileCore, mutate func(cur *database.BookFile) error) (wrote bool, err error) {
	got, err := store.ModifyBookFileDeferAggregates(seen.BookID, seen.ID, func(cur *database.BookFile) error {
		wrote = false
		if !stillTheProbedRow(cur, seen) {
			return database.ErrSkipBookFileWrite
		}
		if err := mutate(cur); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if got == nil {
		return false, nil // the row is gone, or no longer keyed under this book
	}
	return wrote, nil
}

// unknownRowsBooks collects the books whose aggregates need a recompute.
type unknownRowsBooks struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (b *unknownRowsBooks) add(bookID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ids == nil {
		b.ids = map[string]struct{}{}
	}
	b.ids[bookID] = struct{}{}
}

func (b *unknownRowsBooks) sorted() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.ids))
	for id := range b.ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// unknownRowsStore is what the row walk reads and the recompute writes.
type unknownRowsStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	RecomputeBookAggregates(bookID string) error
}

func idSet(lists ...[]string) map[string]struct{} {
	var set map[string]struct{}
	for _, l := range lists {
		for _, id := range l {
			if id == "" {
				continue
			}
			if set == nil {
				set = map[string]struct{}{}
			}
			set[id] = struct{}{}
		}
	}
	return set
}

func (p *Plugin) runUnknownRowsBackfill(ctx context.Context, store unknownRowsStore, params durationReextractParams,
	dryRun bool, reporter sdk.Reporter) error {
	workers := unknownRowsConcurrency(params.Workers)
	res := UnknownRowsResult{DryRun: dryRun}

	all, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("unknownRows: read book files: %w", err)
	}

	fileSet := idSet(params.FileIDs, params.FileIDsSnake)
	bookSet := idSet(params.BookIDs, params.BookIDsSnake)

	// donors indexes the WHOLE library by path (rows that could lend a
	// duration), before any narrowing.
	donors := make(map[string][]database.BookFileCore)
	var walked []database.BookFileCore
	seenFile := make(map[string]struct{}, len(fileSet))
	for _, r := range all {
		if _, known := r.DurationSec(); known && !r.Missing && r.FilePath != "" {
			donors[r.FilePath] = append(donors[r.FilePath], r)
		}
		if _, ok := fileSet[r.ID]; ok {
			seenFile[r.ID] = struct{}{}
		}
		if fileSet != nil {
			if _, ok := fileSet[r.ID]; !ok {
				continue
			}
		}
		if bookSet != nil {
			if _, ok := bookSet[r.BookID]; !ok {
				continue
			}
		}
		switch st := r.DurationState(); {
		case st == database.DurationKnown:
			continue
		case r.Missing:
			res.SkippedMissing++
			continue
		}
		walked = append(walked, r)
	}
	res.NotFound = len(fileSet) - len(seenFile)
	all = nil // the donor index and the walk hold what is needed

	sort.Slice(walked, func(i, j int) bool {
		a, b := walked[i], walked[j]
		if a.BookID != b.BookID {
			return a.BookID < b.BookID
		}
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		return a.ID < b.ID
	})
	if params.Limit > 0 && len(walked) > params.Limit {
		walked = walked[:params.Limit]
	}

	// Pass 1 decisions: pure DB, in memory.
	var (
		pass1  []unknownRowWork
		pass2  []unknownRowWork
		perBkt = map[string]*UnknownRowsBookCounts{}
	)
	book := func(id string) *UnknownRowsBookCounts {
		c := perBkt[id]
		if c == nil {
			c = &UnknownRowsBookCounts{BookID: id}
			perBkt[id] = c
		}
		c.Rows++
		return c
	}
	for _, r := range walked {
		res.Scanned++
		bc := book(r.BookID)
		switch {
		case r.DurationState() == database.DurationUnreadable && !database.IsDurationTimeoutError(r.Scan.DurationOK.LastError):
			// At the attempt ceiling for a reason that is not a timeout: it
			// stays unreadable until an explicit ResetDuration.
			res.SkippedUnreadable++
			bc.SkippedUnreadable++
			continue
		case r.FilePath == "":
			res.SkippedNoPath++
			continue
		}
		if sec := database.KnownDurationAtPath(donors[r.FilePath], r.FileSize, r.FileHash); sec > 0 {
			pass1 = append(pass1, unknownRowWork{row: r, sec: sec, src: srcBorrow})
			bc.Borrowed++
			continue
		}
		if r.AcoustIDFingerprintDurationSec > 0 {
			if sec := database.NormalizeDurationSec(r.FileSize, int(math.Round(r.AcoustIDFingerprintDurationSec))); sec > 0 {
				pass1 = append(pass1, unknownRowWork{row: r, sec: sec, src: srcFingerprint})
				bc.PromotedFromFingerprint++
				continue
			}
		}
		pass2 = append(pass2, unknownRowWork{row: r, src: srcProbe})
		bc.WouldProbe++
	}

	if dryRun {
		return finishUnknownRowsDryRun(reporter, res, pass1, pass2, perBkt)
	}

	var deferred segmentFailureWriter
	if len(pass1)+len(pass2) > 0 {
		var ok bool
		if deferred, ok = database.AsCapability[segmentFailureWriter](store); !ok {
			return fmt.Errorf("unknownRows: the store cannot defer aggregates, so nothing was written")
		}
	}

	var (
		ctr     unknownRowsCounters
		touched unknownRowsBooks
		warns   = &readWarnLimiter{}
	)
	pool := func(items []unknownRowWork, name string, fn func(context.Context, unknownRowWork) error) error {
		ctr.done.Store(0)
		total := len(items)
		return registry.RunItems(ctx, reporter, items, func(ctx context.Context, w unknownRowWork) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			defer func() {
				ctr.done.Add(1)
				registry.TouchLiveness(reporter)
			}()
			return fn(ctx, w)
		}, registry.RunItemsOptions{
			Concurrency: workers, // explicit: RunItems is sequential by default
			ErrMode:     registry.ErrModeCollect,
			// Label runs inside the worker goroutines: every counter it reads is atomic.
			Label: func(i, _ int) string {
				return fmt.Sprintf("%s %d/%d (measured=%d failed=%d env-skipped=%d changed=%d write-err=%d)",
					name, ctr.done.Load(), total,
					ctr.borrowed.Load()+ctr.promoted.Load()+ctr.probedKnown.Load(), ctr.probeFailed.Load(),
					ctr.envSkipped.Load(), ctr.changed.Load(), ctr.writeErrs.Load())
			},
		})
	}

	// recordWrite counts one write's outcome. counter is bumped only when the
	// row was written; measured says a Duration was set (its book is recomputed).
	recordWrite := func(w unknownRowWork, wrote bool, wErr error, counter *atomic.Int64, measured bool) {
		switch {
		case wErr != nil:
			ctr.writeErrs.Add(1)
			if warns.allow() {
				durLog.Warn("duration-backfill unknownRows: book %s file %s: write failed: %s",
					logger.SanitizeLogValue(w.row.BookID), logger.SanitizeLogValue(w.row.ID), logger.SanitizeLogValue(wErr.Error()))
			}
		case !wrote:
			ctr.changed.Add(1)
		default:
			counter.Add(1)
			if measured {
				touched.add(w.row.BookID)
			}
		}
	}

	// A cancelled run must still bring the aggregates of every row it wrote up
	// to date, so the recompute runs on every exit path.
	runErr := func() error {
		if err := pool(pass1, "borrowing/promoting", func(_ context.Context, w unknownRowWork) error {
			wrote, wErr := writeUnknownRow(deferred, w.row, func(cur *database.BookFile) error {
				return cur.SetMeasuredDuration(w.sec)
			})
			counter := &ctr.borrowed
			if w.src == srcFingerprint {
				counter = &ctr.promoted
			}
			recordWrite(w, wrote, wErr, counter, true)
			return nil
		}); err != nil {
			return err
		}
		return pool(pass2, "probing", func(ctx context.Context, w unknownRowWork) error {
			return probeUnknownRow(ctx, deferred, w, &ctr, warns, recordWrite)
		})
	}()

	recomputed, rcErr := recomputeUnknownRowBooks(ctx, reporter, store, touched.sorted(), workers)
	res.BooksRecomputed = recomputed

	res.Borrowed = int(ctr.borrowed.Load())
	res.PromotedFromFingerprint = int(ctr.promoted.Load())
	res.ProbedKnown = int(ctr.probedKnown.Load())
	res.ProbeFailed = int(ctr.probeFailed.Load())
	res.EnvironmentSkipped = int(ctr.envSkipped.Load())
	res.SkippedChanged = int(ctr.changed.Load())
	res.WriteErrors = int(ctr.writeErrs.Load())
	if n := warns.suppressed(); n > 0 {
		durLog.Warn("duration-backfill unknownRows: %d further warnings suppressed after the first %d; see the counts in the result", n, readWarnLimit)
	}

	summary := unknownRowsSummary(res)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(res.Scanned, max(res.Scanned, 1), summary)
	setErr := registry.ReporterSetResult(reporter, res)
	switch {
	case runErr != nil:
		return runErr
	case rcErr != nil:
		return rcErr
	case setErr != nil:
		return fmt.Errorf("unknownRows: persist result: %w", setErr)
	}
	return nil
}

// probeUnknownRow is pass 2 for one row: a header probe, then the row's outcome.
func probeUnknownRow(ctx context.Context, store segmentFailureWriter, w unknownRowWork, ctr *unknownRowsCounters,
	warns *readWarnLimiter, recordWrite func(unknownRowWork, bool, error, *atomic.Int64, bool)) error {
	info, mErr := unknownRowsProbe(ctx, w.row.FilePath)
	if err := ctx.Err(); err != nil {
		return err // a cancelled probe says nothing about the file
	}
	pErr := bookfileaudio.ProbeError(info, mErr)
	if pErr == nil {
		sec := database.NormalizeDurationSec(w.row.FileSize, info.Duration)
		wrote, wErr := writeUnknownRow(store, w.row, func(cur *database.BookFile) error {
			return cur.SetMeasuredDuration(sec)
		})
		recordWrite(w, wrote, wErr, &ctr.probedKnown, true)
		return nil
	}
	kind, msg := bookfileaudio.ClassifyProbeError(pErr)
	if !kind.CountsAsAttempt() {
		// Says nothing about the file: record nothing.
		ctr.envSkipped.Add(1)
		if warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s: probe could not run (%s): %s; row left unprobed",
				logger.SanitizeLogValue(w.row.BookID), logger.SanitizeLogValue(w.row.ID), kind, logger.SanitizeLogValue(msg))
		}
		return nil
	}
	wrote, wErr := writeUnknownRow(store, w.row, func(cur *database.BookFile) error {
		cur.RecordDurationProbeFailure(msg)
		return nil
	})
	recordWrite(w, wrote, wErr, &ctr.probeFailed, false)
	return nil
}

// recomputeUnknownRowBooks recomputes each book's aggregates once. The books
// are disjoint, so the recompute is spread over the worker pool. It runs under
// a context that survives the op's cancellation: a row written before a cancel
// is a complete write and its book must follow it.
func recomputeUnknownRowBooks(ctx context.Context, reporter sdk.Reporter, store unknownRowsStore, bookIDs []string, workers int) (int, error) {
	if len(bookIDs) == 0 {
		return 0, nil
	}
	var done, failed atomic.Int64
	var firstErr atomic.Pointer[error]
	err := registry.RunItems(context.WithoutCancel(ctx), reporter, bookIDs, func(_ context.Context, id string) error {
		defer registry.TouchLiveness(reporter)
		if err := store.RecomputeBookAggregates(id); err != nil {
			failed.Add(1)
			e := fmt.Errorf("book %s: RecomputeBookAggregates: %w", id, err)
			firstErr.CompareAndSwap(nil, &e)
			return nil
		}
		done.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: workers,
		ErrMode:     registry.ErrModeCollect,
		Label: func(_, total int) string {
			return fmt.Sprintf("recomputing aggregates %d/%d", done.Load()+failed.Load(), total)
		},
	})
	if err != nil {
		return int(done.Load()), err
	}
	if p := firstErr.Load(); p != nil {
		return int(done.Load()), fmt.Errorf("unknownRows: %d book(s) failed their aggregate recompute, first: %w", failed.Load(), *p)
	}
	return int(done.Load()), nil
}

func finishUnknownRowsDryRun(reporter sdk.Reporter, res UnknownRowsResult, pass1, pass2 []unknownRowWork,
	perBook map[string]*UnknownRowsBookCounts) error {
	for _, w := range pass1 {
		if w.src == srcBorrow {
			res.Borrowed++
		} else {
			res.PromotedFromFingerprint++
		}
	}
	res.WouldProbe = len(pass2)

	books := make([]UnknownRowsBookCounts, 0, len(perBook))
	for _, c := range perBook {
		books = append(books, *c)
	}
	sort.Slice(books, func(i, j int) bool {
		if books[i].Rows != books[j].Rows {
			return books[i].Rows > books[j].Rows
		}
		return books[i].BookID < books[j].BookID
	})
	if len(books) > unknownRowsBookReportCap {
		res.BooksOmitted = len(books) - unknownRowsBookReportCap
		books = books[:unknownRowsBookReportCap]
	}
	res.Books = books

	summary := unknownRowsSummary(res)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(res.Scanned, max(res.Scanned, 1), summary)
	if err := registry.ReporterSetResult(reporter, res); err != nil {
		return fmt.Errorf("unknownRows: persist result: %w", err)
	}
	return nil
}

func unknownRowsSummary(r UnknownRowsResult) string {
	if r.DryRun {
		return fmt.Sprintf("DRY RUN unknownRows: scanned=%d would-borrow=%d would-promote-from-fingerprint=%d would-probe=%d skipped-unreadable=%d skipped-no-path=%d skipped-missing=%d not-found=%d (ffprobe is not run in a dry run)",
			r.Scanned, r.Borrowed, r.PromotedFromFingerprint, r.WouldProbe, r.SkippedUnreadable, r.SkippedNoPath, r.SkippedMissing, r.NotFound)
	}
	return fmt.Sprintf("unknownRows: scanned=%d borrowed=%d promoted_from_fingerprint=%d probed_known=%d probe_failed=%d environment_skipped=%d skipped_changed=%d skipped_unreadable=%d skipped_no_path=%d skipped_missing=%d not_found=%d write_errors=%d books_recomputed=%d",
		r.Scanned, r.Borrowed, r.PromotedFromFingerprint, r.ProbedKnown, r.ProbeFailed, r.EnvironmentSkipped, r.SkippedChanged,
		r.SkippedUnreadable, r.SkippedNoPath, r.SkippedMissing, r.NotFound, r.WriteErrors, r.BooksRecomputed)
}
