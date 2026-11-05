// file: internal/plugins/maintenance/duration_backfill_unknown_rows.go
// version: 1.2.0
// guid: 30470edd-3683-43f8-963e-07e058ba2a3e
// last-edited: 2026-11-05

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
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
// file; the same ceiling rule the book-grain loop applies). Rows that are
// settled without any action are counted and left alone: Unreadable for any
// other reason (until an explicit ResetDuration), no file path, and rows the
// user excluded from scanning (SkipScan: neither probed nor borrowed into).
// A row marked Missing is not looked at. Copies and books/itunes/** rows are
// included: probing is a header READ, and the write is a DB-only row update; no
// file is opened for writing, moved or touched.
//
// Limit counts rows that would be acted on (borrow, promote or probe). Settled
// rows are classified BEFORE the cap and never consume it: otherwise a library
// whose first N rows are unreadable would make a limited run examine the same N
// rows forever and never progress.
//
// Pass 1 is DB only: a sibling borrow (database.KnownDurationAtPath, the rule
// the scanner and folder-books share), then AcoustIDFingerprintDurationSec, the
// first source the book-grain loop uses. The donor index is built from the WHOLE
// library, never from the narrowed candidate set: the donor that settles a
// held folder-books group is the OTHER book's row at the same path, and a
// fileIds narrowing to the unknown rows would otherwise hide it and probe every
// file. The index holds positions into the one row slice the walk already has,
// not a second copy of every known row.
//
// Pass 2 runs ffprobe through registry.RunItems with Concurrency set EXPLICITLY
// (RunItems is sequential unless told otherwise). Rows that are the same file
// (same path, size and hash; an unknown hash groups only with unknown hashes)
// share ONE probe: a megarow has many rows at one path, and probing each would
// run ffprobe N times on the same bytes. Counts stay per row. Before the probe a
// single stat per group settles what needs no ffprobe at all: a file that is
// absent (a stale row; the common case on a library with many of them), or one
// whose on-disk size no longer matches the row, which is not the file the row
// describes. Outcomes are classified by bookfileaudio.ClassifyProbeError, the
// one classifier every duration writer shares: a file failure or timeout is an
// attempt on the row, an environment failure (ffprobe missing, file absent,
// permission, stale mount) records nothing.
//
// Writes. Every row goes through ModifyBookFileDeferAggregates rather than a new
// store method: it already reads the row under its stripe, hands a copy to the
// callback, writes through the compare-and-swap commit (re-running the callback
// on a fresh copy when a concurrent writer wins) and honours
// ErrSkipBookFileWrite, which is the whole RecordDurationProbe contract. It is
// also what recordSegmentProbeFailures uses, so the two cannot disagree. The
// deferral is the point: a book's aggregates are recomputed ONCE per book, and
// only for books where a Duration was set. A failure record moves nothing an
// aggregate reads.
//
// The recompute is prompt, not deferred to the end of the run. A book whose rows
// are all settled is recomputed by the worker that settled its last row; books
// that only borrowed or promoted are recomputed when pass 1 ends. Only the
// leftovers (a cancelled run) are recomputed at the end, under a context that
// survives the cancellation. So a long probe pass does not leave measured books
// reading stale totals until the last file finishes.
//
// Dry run (the default) runs pass 1's decisions and reports what pass 2 would
// probe (would_probe) but does NOT run ffprobe or stat anything: a preview that
// shells out per file over a library-scale population is hours of wall clock
// that writes nothing. probed_known and probe_failed are therefore 0 in a dry
// run, and a stale row shows up as would_probe, not as an environment skip.

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
// writes. raw is the params as sent: skipAgeDays has a non-zero default, so only
// the key's presence says the caller set it.
func validateUnknownRowsParams(p durationReextractParams, raw json.RawMessage) error {
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
	// force, onlyMissingDuration and skipAgeDays steer the book-grain loop. Here
	// they would be accepted and do nothing, which reads as a safeguard that is
	// not there (an operator sending force or skipAgeDays expects rows to be
	// re-examined or skipped).
	if p.Force {
		return fmt.Errorf("invalid params: force has no effect with mode=%q (it is a book-grain flag); remove it", durationModeUnknownRows)
	}
	if p.OnlyMissingDuration {
		return fmt.Errorf("invalid params: onlyMissingDuration has no effect with mode=%q (every candidate row is unmeasured already); remove it", durationModeUnknownRows)
	}
	var keys map[string]json.RawMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &keys) // already decoded once by the caller
	}
	if _, set := keys["skipAgeDays"]; set {
		return fmt.Errorf("invalid params: skipAgeDays has no effect with mode=%q (rows are not age-skipped); remove it", durationModeUnknownRows)
	}
	return nil
}

// UnknownRowsBookCounts is one book's share of a dry run. Its columns sum to
// Rows.
type UnknownRowsBookCounts struct {
	BookID                  string `json:"book_id"`
	Rows                    int    `json:"rows"`
	Borrowed                int    `json:"borrowed"`
	PromotedFromFingerprint int    `json:"promoted_from_fingerprint"`
	WouldProbe              int    `json:"would_probe"`
	SkippedUnreadable       int    `json:"skipped_unreadable"`
	SkippedNoPath           int    `json:"skipped_no_path"`
	SkippedUserExcluded     int    `json:"skipped_user_excluded"`
}

// UnknownRowsResult is the op result of an unknownRows run.
//
// In an apply run, Scanned equals Borrowed + PromotedFromFingerprint +
// ProbedKnown + ProbeFailed + EnvironmentSkipped + SkippedChanged +
// SkippedUnreadable + SkippedNoPath + SkippedUserExcluded + WriteErrors (a run
// that was cancelled has rows that reached none of them). In a dry run it equals
// Borrowed + PromotedFromFingerprint + WouldProbe + SkippedUnreadable +
// SkippedNoPath + SkippedUserExcluded (the first two read "would be").
type UnknownRowsResult struct {
	DryRun bool `json:"dry_run"`
	// Scanned is the candidate rows examined: unmeasured rows, present, inside
	// the fileIds / bookIds narrowing, and (rows that would be acted on) up to
	// limit. Known and Missing rows are not in it.
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
	// rewritten, marked missing, retired, excluded, measured by someone else),
	// is gone, or its file on disk no longer has the size the row records.
	SkippedChanged int `json:"skipped_changed"`
	// SkippedUnreadable: Unreadable for a reason other than a timeout.
	SkippedUnreadable int `json:"skipped_unreadable"`

	// ProbesRun is the ffprobe runs of pass 2 (apply only). It is at most
	// ProbedKnown + ProbeFailed + the rows skipped as changed after a probe: rows
	// that are the same file (same path, size and hash) share ONE probe, whose
	// outcome is applied to each of them, and a group whose file is absent or
	// changed on disk is never probed.
	ProbesRun int `json:"probes_run,omitempty"`
	// WouldProbe is the rows pass 2 would probe: a dry run only.
	WouldProbe int `json:"would_probe,omitempty"`
	// SkippedNoPath: the row has no file path.
	SkippedNoPath int `json:"skipped_no_path,omitempty"`
	// SkippedUserExcluded: the user excluded the row from scanning (SkipScan). It
	// is neither probed nor borrowed into.
	SkippedUserExcluded int `json:"skipped_user_excluded,omitempty"`
	// SkippedMissing: rows marked Missing that no mode looks at. Not in Scanned.
	SkippedMissing int `json:"skipped_missing,omitempty"`
	// NotFound: fileIds that match no book_file row (a row merged away since the
	// caller read it). Reported, never an error.
	NotFound int `json:"not_found,omitempty"`
	// WriteErrors: rows whose write failed. A write whose fsync failed is applied
	// but in doubt (database.ErrBookFileDurabilityUnknown): it is counted here,
	// not as a success, and its book is still recomputed.
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
	// probes counts ffprobe runs: one per (path, size, hash) group.
	probes atomic.Int64
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
// marked missing, retired or excluded from scanning, same path, and the same
// bytes (hash and size: a failure of the OLD bytes says nothing about the new,
// and a borrowed duration was matched on size).
func stillTheProbedRow(cur *database.BookFile, seen database.BookFileCore) bool {
	return cur.Duration == nil && !cur.Missing && !cur.Retired && !cur.SkipScan &&
		cur.FilePath == seen.FilePath && cur.FileHash == seen.FileHash && cur.FileSize == seen.FileSize
}

// writeUnknownRow applies mutate to the stored row under its write lock, but
// only while the stored row is still the one the walk saw. wrote is false for a
// row that changed or vanished (the caller counts skipped_changed). The callback
// may run more than once for one row (a retried compare-and-swap), so each run
// starts from "not written", and mutate runs on the FRESH copy, never on the
// walked one: a failure's attempt count must grow from the stored count.
//
// A write whose fsync failed returns database.ErrBookFileDurabilityUnknown WITH
// wrote true: the row WAS written (bookFileApplied's contract: only its fsync is
// in doubt), so the caller counts the error and still recomputes the book. Any
// other error means nothing was written and wrote is false, whatever the last
// callback run saw.
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
		if errors.Is(err, database.ErrBookFileDurabilityUnknown) {
			return wrote, err
		}
		return false, err
	}
	if got == nil {
		return false, nil // the row is gone, or no longer keyed under this book
	}
	return wrote, nil
}

// unknownRowsRecompute owns the "recompute each measured book's aggregates once"
// rule. A book is touched when a Duration was set on one of its rows and claimed
// when its recompute starts; claiming is what makes "exactly once" hold however
// many paths (the worker that settled its last row, the end of pass 1, the final
// leftover sweep) reach it.
type unknownRowsRecompute struct {
	store unknownRowsStore

	mu      sync.Mutex
	touched map[string]struct{}
	claimed map[string]struct{}

	done, failed atomic.Int64
	firstErr     atomic.Pointer[error]
}

func (r *unknownRowsRecompute) touch(bookID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.touched == nil {
		r.touched = map[string]struct{}{}
	}
	r.touched[bookID] = struct{}{}
}

func (r *unknownRowsRecompute) isTouched(bookID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.touched[bookID]
	return ok
}

// pending returns, sorted, the touched books not yet claimed that keep accepts.
func (r *unknownRowsRecompute) pending(keep func(bookID string) bool) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for id := range r.touched {
		if _, done := r.claimed[id]; done {
			continue
		}
		if keep == nil || keep(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// run recomputes one book's aggregates, once: a second call for the same book is
// a no-op.
func (r *unknownRowsRecompute) run(bookID string) {
	r.mu.Lock()
	if _, done := r.claimed[bookID]; done {
		r.mu.Unlock()
		return
	}
	if r.claimed == nil {
		r.claimed = map[string]struct{}{}
	}
	r.claimed[bookID] = struct{}{}
	r.mu.Unlock()

	if err := r.store.RecomputeBookAggregates(bookID); err != nil {
		r.failed.Add(1)
		e := fmt.Errorf("book %s: RecomputeBookAggregates: %w", bookID, err)
		r.firstErr.CompareAndSwap(nil, &e)
		return
	}
	r.done.Add(1)
}

// runAll recomputes ids over a bounded pool. It deliberately does NOT go through
// registry.RunItems: RunItems honours the op's pause gate, so a recompute that
// must outlive a cancelled run (a row written before the cancel is a complete
// write and its book must follow it) would block on a paused op, and
// context.WithoutCancel would only hide that. An errgroup with SetLimit has no
// gate and no context to cancel.
func (r *unknownRowsRecompute) runAll(reporter sdk.Reporter, ids []string, workers int) {
	if len(ids) == 0 {
		return
	}
	var g errgroup.Group
	g.SetLimit(max(1, workers))
	for _, id := range ids {
		g.Go(func() error {
			defer registry.TouchLiveness(reporter)
			r.run(id)
			return nil
		})
	}
	_ = g.Wait()
}

func (r *unknownRowsRecompute) err() error {
	if p := r.firstErr.Load(); p != nil {
		return fmt.Errorf("unknownRows: %d book(s) failed their aggregate recompute, first: %w", r.failed.Load(), *p)
	}
	return nil
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

// unknownRowsRun is the state of one apply run's two passes.
type unknownRowsRun struct {
	writer segmentFailureWriter
	ctr    unknownRowsCounters
	warns  *readWarnLimiter
	books  *unknownRowsRecompute
	// pending counts, per book, the pass-2 rows not yet settled. The map is built
	// before pass 2 and never written again (only the counters inside change), so
	// workers read it without a lock. A book is absent when it has no pass-2 row.
	pending map[string]*atomic.Int32
}

func newUnknownRowsRun(writer segmentFailureWriter, store unknownRowsStore, pass2 []unknownRowWork) *unknownRowsRun {
	r := &unknownRowsRun{
		writer:  writer,
		warns:   &readWarnLimiter{},
		books:   &unknownRowsRecompute{store: store},
		pending: map[string]*atomic.Int32{},
	}
	for _, w := range pass2 {
		c := r.pending[w.row.BookID]
		if c == nil {
			c = new(atomic.Int32)
			r.pending[w.row.BookID] = c
		}
		c.Add(1)
	}
	return r
}

// recordWrite counts one write's outcome. counter is bumped only when the row
// was written cleanly; measured says a Duration was set (its book is recomputed).
func (r *unknownRowsRun) recordWrite(w unknownRowWork, wrote bool, wErr error, counter *atomic.Int64, measured bool) {
	switch {
	case wErr != nil:
		r.ctr.writeErrs.Add(1)
		if r.warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s: write failed: %s",
				logger.SanitizeLogValue(w.row.BookID), logger.SanitizeLogValue(w.row.ID), logger.SanitizeLogValue(wErr.Error()))
		}
		// A write whose fsync failed was still applied: the row carries the
		// duration, so the aggregates must follow it even though the row is
		// counted as an error.
		if wrote && measured {
			r.books.touch(w.row.BookID)
		}
	case !wrote:
		r.ctr.changed.Add(1)
	default:
		counter.Add(1)
		if measured {
			r.books.touch(w.row.BookID)
		}
	}
}

// rowSettled is called once per pass-2 row when its outcome is final. The worker
// that settles a book's last row recomputes it right then, so the book's
// aggregates are current while the rest of the pass is still probing.
func (r *unknownRowsRun) rowSettled(bookID string) {
	c := r.pending[bookID]
	if c == nil {
		return
	}
	if c.Add(-1) == 0 && r.books.isTouched(bookID) {
		r.books.run(bookID)
	}
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

	// donorIdx indexes the WHOLE library by path (rows that could lend a
	// duration), before any narrowing. It holds positions into all, so the index
	// costs an int per known row rather than a second copy of the row.
	donorIdx := make(map[string][]int)
	var cand []int
	seenFile := make(map[string]struct{}, len(fileSet))
	for i := range all {
		r := &all[i]
		if _, known := r.DurationSec(); known && !r.Missing && r.FilePath != "" {
			donorIdx[r.FilePath] = append(donorIdx[r.FilePath], i)
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
		switch {
		case r.DurationState() == database.DurationKnown:
			continue
		case r.Missing:
			res.SkippedMissing++
			continue
		}
		cand = append(cand, i)
	}
	res.NotFound = len(fileSet) - len(seenFile)

	sort.Slice(cand, func(x, y int) bool {
		a, b := &all[cand[x]], &all[cand[y]]
		if a.BookID != b.BookID {
			return a.BookID < b.BookID
		}
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		return a.ID < b.ID
	})

	// donorRows adapts the index to database.KnownDurationAtPath, the one shared
	// borrow rule. A path's rows are copied once, on first use, so only the paths
	// a candidate actually sits at are ever materialised.
	donorCache := map[string][]database.BookFileCore{}
	donorRows := func(path string) []database.BookFileCore {
		idx := donorIdx[path]
		if len(idx) == 0 {
			return nil
		}
		if rows, ok := donorCache[path]; ok {
			return rows
		}
		rows := make([]database.BookFileCore, 0, len(idx))
		for _, j := range idx {
			rows = append(rows, all[j])
		}
		donorCache[path] = rows
		return rows
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
	actionable := 0
	for _, i := range cand {
		r := all[i]
		const (
			none = iota
			unreadable
			noPath
			userExcluded
		)
		settled := none
		switch {
		case r.DurationState() == database.DurationUnreadable && !database.IsDurationTimeoutError(r.Scan.DurationOK.LastError):
			// At the attempt ceiling for a reason that is not a timeout: it
			// stays unreadable until an explicit ResetDuration.
			settled = unreadable
		case r.FilePath == "":
			settled = noPath
		case r.SkipScan:
			// The user excluded this file from scanning: not probed, not
			// borrowed into.
			settled = userExcluded
		}
		// Settled rows take no action, so they never spend the limit; only a row
		// that would be borrowed, promoted or probed does.
		if settled == none && params.Limit > 0 && actionable >= params.Limit {
			continue
		}
		res.Scanned++
		bc := book(r.BookID)
		switch settled {
		case unreadable:
			res.SkippedUnreadable++
			bc.SkippedUnreadable++
			continue
		case noPath:
			res.SkippedNoPath++
			bc.SkippedNoPath++
			continue
		case userExcluded:
			res.SkippedUserExcluded++
			bc.SkippedUserExcluded++
			continue
		}
		actionable++
		if sec := database.KnownDurationAtPath(donorRows(r.FilePath), r.FileSize, r.FileHash); sec > 0 {
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
	// The decisions hold copies of what they need.
	all, cand, donorIdx, donorCache = nil, nil, nil, nil

	if dryRun {
		return finishUnknownRowsDryRun(reporter, res, pass1, pass2, perBkt)
	}

	var writer segmentFailureWriter
	if len(pass1)+len(pass2) > 0 {
		var ok bool
		if writer, ok = database.AsCapability[segmentFailureWriter](store); !ok {
			return fmt.Errorf("unknownRows: the store cannot defer aggregates, so nothing was written")
		}
	}
	run := newUnknownRowsRun(writer, store, pass2)

	// A cancelled run must still bring the aggregates of every row it wrote up
	// to date, so the leftover sweep below runs on every exit path.
	runErr := func() error {
		if err := unknownRowsPool(ctx, reporter, workers, &run.ctr, pass1, "borrowing/promoting", func(_ context.Context, w unknownRowWork) error {
			wrote, wErr := writeUnknownRow(run.writer, w.row, func(cur *database.BookFile) error {
				return cur.SetMeasuredDuration(w.sec)
			})
			counter := &run.ctr.borrowed
			if w.src == srcFingerprint {
				counter = &run.ctr.promoted
			}
			run.recordWrite(w, wrote, wErr, counter, true)
			return nil
		}); err != nil {
			return err
		}
		// Pass 1 is over: every book that only borrowed or promoted is final.
		// Books that also have rows to probe are recomputed when their last
		// probe row settles.
		run.books.runAll(reporter, run.books.pending(func(id string) bool {
			_, probing := run.pending[id]
			return !probing
		}), workers)
		return unknownRowsPool(ctx, reporter, workers, &run.ctr, groupProbeRows(pass2), "probing", func(ctx context.Context, g []unknownRowWork) error {
			return run.probeAndSettle(ctx, g)
		})
	}()

	run.books.runAll(reporter, run.books.pending(nil), workers)
	rcErr := run.books.err()
	res.BooksRecomputed = int(run.books.done.Load())

	res.Borrowed = int(run.ctr.borrowed.Load())
	res.PromotedFromFingerprint = int(run.ctr.promoted.Load())
	res.ProbedKnown = int(run.ctr.probedKnown.Load())
	res.ProbeFailed = int(run.ctr.probeFailed.Load())
	res.EnvironmentSkipped = int(run.ctr.envSkipped.Load())
	res.SkippedChanged = int(run.ctr.changed.Load())
	res.WriteErrors = int(run.ctr.writeErrs.Load())
	res.ProbesRun = int(run.ctr.probes.Load())
	if n := run.warns.suppressed(); n > 0 {
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

// unknownRowsPool runs fn over items through registry.RunItems with the pool
// size set EXPLICITLY (RunItems is sequential by default). Each item checks ctx
// first and stamps liveness when done. Label runs inside the worker goroutines,
// so every counter it reads is atomic.
func unknownRowsPool[T any](ctx context.Context, reporter sdk.Reporter, workers int, ctr *unknownRowsCounters,
	items []T, name string, fn func(context.Context, T) error) error {
	ctr.done.Store(0)
	total := len(items)
	return registry.RunItems(ctx, reporter, items, func(ctx context.Context, it T) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		defer func() {
			ctr.done.Add(1)
			registry.TouchLiveness(reporter)
		}()
		return fn(ctx, it)
	}, registry.RunItemsOptions{
		Concurrency: workers,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, _ int) string {
			return fmt.Sprintf("%s %d/%d (measured=%d failed=%d env-skipped=%d changed=%d write-err=%d probes=%d)",
				name, ctr.done.Load(), total,
				ctr.borrowed.Load()+ctr.promoted.Load()+ctr.probedKnown.Load(), ctr.probeFailed.Load(),
				ctr.envSkipped.Load(), ctr.changed.Load(), ctr.writeErrs.Load(), ctr.probes.Load())
		},
	})
}

// probeGroupKey identifies one file for probing: rows at the same path with the
// same size and the same hash are the same bytes, so one probe answers for all.
// An unknown hash ("") is a key of its own: such a row groups only with other
// unknown-hash rows at that path and size, never with a row whose hash is known,
// and rows with differing known hashes never share a group.
type probeGroupKey struct {
	path string
	size int64
	hash string
}

// groupProbeRows partitions rows by probeGroupKey, in first-seen order.
func groupProbeRows(rows []unknownRowWork) [][]unknownRowWork {
	index := map[probeGroupKey]int{}
	var groups [][]unknownRowWork
	for _, w := range rows {
		k := probeGroupKey{w.row.FilePath, w.row.FileSize, w.row.FileHash}
		i, ok := index[k]
		if !ok {
			i = len(groups)
			index[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], w)
	}
	return groups
}

// probeAndSettle runs one group and, when it completed, settles each of its
// rows (see rowSettled). A group cut short by cancellation settles nothing: its
// books are picked up by the leftover sweep.
func (r *unknownRowsRun) probeAndSettle(ctx context.Context, group []unknownRowWork) error {
	if err := r.probeGroup(ctx, group); err != nil {
		return err
	}
	for _, w := range group {
		r.rowSettled(w.row.BookID)
	}
	return nil
}

// skipGroupEnvironment records that a whole group was left alone for a reason
// that says nothing about the file's bytes: nothing is written to any row.
func (r *unknownRowsRun) skipGroupEnvironment(group []unknownRowWork) {
	r.ctr.envSkipped.Add(int64(len(group)))
}

// probeGroup is pass 2 for one group of rows that are the same file
// (probeGroupKey): at most ONE header probe, then the outcome applied to every
// row. Each row's write has its own precondition (writeUnknownRow), so a row that
// changed underneath is skipped and its siblings still get the result. Counts
// stay per row.
//
// One stat per group runs first. A file that is absent is a stale row, not a bad
// file, and starting an ffprobe process to learn that is the cost this avoids; a
// file whose size no longer matches the row is not the file the row describes,
// so a duration measured from it (or a failure recorded against the row) would
// be about other bytes.
func (r *unknownRowsRun) probeGroup(ctx context.Context, group []unknownRowWork) error {
	first := group[0]
	path, pathErr := pathvalidation.CleanAbsolutePath(first.row.FilePath)
	if pathErr != nil {
		r.skipGroupEnvironment(group)
		if r.warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): path cannot be probed: %s; rows left unprobed",
				logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1, logger.SanitizeLogValue(pathErr.Error()))
		}
		return nil
	}
	fi, statErr := os.Stat(path)
	switch {
	case statErr != nil && errors.Is(statErr, fs.ErrNotExist):
		// A stale row: the common case on a library with many of them, so it is
		// Debug (the count is in the result), not a warning per file.
		r.skipGroupEnvironment(group)
		durLog.Debug("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): file is absent; not probed",
			logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1)
		return nil
	case statErr != nil || !fi.Mode().IsRegular():
		msg := "not a regular file"
		if statErr != nil {
			msg = statErr.Error()
		}
		r.skipGroupEnvironment(group)
		if r.warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): file cannot be examined: %s; rows left unprobed",
				logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1, logger.SanitizeLogValue(msg))
		}
		return nil
	case first.row.FileSize > 0 && fi.Size() > 0 && fi.Size() != first.row.FileSize:
		r.ctr.changed.Add(int64(len(group)))
		if r.warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): file is %d bytes on disk but the row records %d; not probed",
				logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1, fi.Size(), first.row.FileSize)
		}
		return nil
	}

	r.ctr.probes.Add(1)
	info, mErr := unknownRowsProbe(ctx, path)
	if err := ctx.Err(); err != nil {
		return err // a cancelled probe says nothing about the file
	}
	pErr := bookfileaudio.ProbeError(info, mErr)
	if pErr == nil {
		sec := database.NormalizeDurationSec(first.row.FileSize, info.Duration)
		for _, w := range group {
			if err := ctx.Err(); err != nil {
				return err
			}
			wrote, wErr := writeUnknownRow(r.writer, w.row, func(cur *database.BookFile) error {
				return cur.SetMeasuredDuration(sec)
			})
			r.recordWrite(w, wrote, wErr, &r.ctr.probedKnown, true)
		}
		return nil
	}
	kind, msg := bookfileaudio.ClassifyProbeError(pErr)
	if !kind.CountsAsAttempt() {
		// Says nothing about the file: record nothing.
		r.skipGroupEnvironment(group)
		if kind == bookfileaudio.FailureAbsent {
			durLog.Debug("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): file vanished before the probe; rows left unprobed",
				logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1)
		} else if r.warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): probe could not run (%s): %s; rows left unprobed",
				logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1, kind, logger.SanitizeLogValue(msg))
		}
		return nil
	}
	for _, w := range group {
		if err := ctx.Err(); err != nil {
			return err
		}
		wrote, wErr := writeUnknownRow(r.writer, w.row, func(cur *database.BookFile) error {
			cur.RecordDurationProbeFailure(msg)
			return nil
		})
		r.recordWrite(w, wrote, wErr, &r.ctr.probeFailed, false)
	}
	return nil
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
		return fmt.Sprintf("DRY RUN unknownRows: scanned=%d would-borrow=%d would-promote-from-fingerprint=%d would-probe=%d skipped-unreadable=%d skipped-no-path=%d skipped-user-excluded=%d skipped-missing=%d not-found=%d (ffprobe is not run in a dry run)",
			r.Scanned, r.Borrowed, r.PromotedFromFingerprint, r.WouldProbe, r.SkippedUnreadable, r.SkippedNoPath, r.SkippedUserExcluded, r.SkippedMissing, r.NotFound)
	}
	return fmt.Sprintf("unknownRows: scanned=%d borrowed=%d promoted_from_fingerprint=%d probed_known=%d probe_failed=%d environment_skipped=%d skipped_changed=%d skipped_unreadable=%d skipped_no_path=%d skipped_user_excluded=%d skipped_missing=%d not_found=%d write_errors=%d books_recomputed=%d",
		r.Scanned, r.Borrowed, r.PromotedFromFingerprint, r.ProbedKnown, r.ProbeFailed, r.EnvironmentSkipped, r.SkippedChanged,
		r.SkippedUnreadable, r.SkippedNoPath, r.SkippedUserExcluded, r.SkippedMissing, r.NotFound, r.WriteErrors, r.BooksRecomputed)
}
