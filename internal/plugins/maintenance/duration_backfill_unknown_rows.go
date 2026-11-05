// file: internal/plugins/maintenance/duration_backfill_unknown_rows.go
// version: 1.4.0
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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
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
	SkippedBadPath          int    `json:"skipped_bad_path"`
	SkippedNotAudio         int    `json:"skipped_not_audio"`
}

// UnknownRowsResult is the op result of an unknownRows run.
//
// In an apply run, Scanned equals Borrowed + PromotedFromFingerprint +
// ProbedKnown + ProbeFailed + EnvironmentSkipped + SkippedChanged +
// SkippedUnreadable + SkippedNoPath + SkippedUserExcluded + SkippedBadPath +
// SkippedNotAudio + SkippedNotRegular + WriteErrors (a run that was cancelled
// has rows that reached none of them). In a dry run it equals Borrowed +
// PromotedFromFingerprint + WouldProbe + SkippedUnreadable + SkippedNoPath +
// SkippedUserExcluded + SkippedBadPath + SkippedNotAudio (the first two read
// "would be"; a dry run does not touch the disk, so it cannot see
// SkippedNotRegular and counts such a row as WouldProbe).
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
	// reached (ffprobe missing, file absent, permission, stale mount). Nothing
	// was recorded.
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
	// SkippedBadPath: the row's path is not absolute and clean
	// (pathvalidation.CleanAbsolutePath refuses it), so it is never opened.
	SkippedBadPath int `json:"skipped_bad_path,omitempty"`
	// SkippedNotAudio: the row needs a probe but its extension is not one the
	// duration probe reads (audioext.DefaultSet), so no subprocess is started.
	SkippedNotAudio int `json:"skipped_not_audio,omitempty"`
	// SkippedNotRegular: the path is on disk but is a directory or another
	// special file, not a file a probe can read (apply only: it takes a stat).
	SkippedNotRegular int `json:"skipped_not_regular,omitempty"`
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
	notRegular                                   atomic.Int64
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

// unknownRowSettled is what the unknownRows mode does with one book_file row,
// the single rule both the mode's own walk and every caller that offers to start
// it (the folder-books holds' "Probe these files") read, so an offer names a row
// only when the run would not decline it for the reasons the row and the file
// can show (a hold whose action loops forever is the failure this prevents).
// The checks run in this order and the first that applies wins.
//
// What it cannot see is an outcome that depends on the world beyond the file's
// stat: ffprobe missing, a stale mount, or a file the process cannot open. Those
// are environment skips of the run, nothing is recorded, and a caller that wants
// to fail closed on them has to look itself (the folder-books fixer opens the
// file).
type unknownRowSettled int

const (
	// unknownRowActs: the mode borrows, promotes or probes the row.
	unknownRowActs unknownRowSettled = iota
	// unknownRowKnown: the row has a duration; there is nothing to do.
	unknownRowKnown
	// unknownRowMissing: flagged Missing. A library scan, not this mode, settles
	// whether the file is there.
	unknownRowMissing
	// unknownRowUnreadable: at the attempt ceiling for a reason that is not a
	// timeout; it stays unreadable until an explicit ResetDuration.
	unknownRowUnreadable
	unknownRowNoPath
	// unknownRowUserExcluded: the user excluded the file from scanning (SkipScan);
	// it is not probed and not borrowed into.
	unknownRowUserExcluded
	// unknownRowBadPath: the path is not absolute and clean, which the probe pass
	// refuses (pathvalidation.CleanAbsolutePath) before it looks at the file.
	unknownRowBadPath
	// unknownRowNotAudio: the extension is not one mediainfo reads, so it returns
	// ErrUnsupportedFormat without starting a probe. The scanner admits the
	// configured SupportedExtensions, which can be wider.
	unknownRowNotAudio
	// unknownRowNotRegular: the path is on disk but is not a regular file. Decided
	// only when the caller has looked at the file.
	unknownRowNotRegular
	// unknownRowChanged: the file on disk is not the size the row records, so it
	// is not the file the row describes and a measurement would be about other
	// bytes. Decided only when the caller has looked at the file.
	unknownRowChanged
)

// unknownRowDisk is what a caller saw of the file at a row's path: its size and
// whether it is a regular file. A nil *unknownRowDisk means the caller has not
// looked (the walk decides nothing from the disk; the probe pass stats each file
// and asks again with this filled in).
type unknownRowDisk struct {
	Size    int64
	Regular bool
}

// unknownRowsAudioExts is the set of extensions the duration probe reads: the
// same set mediainfo.Extract checks (audioext.DefaultSet).
var unknownRowsAudioExts = audioext.DefaultSet()

// unknownRowPromotable reports whether pass 1 promotes r's fingerprint duration
// into its row, which it does without looking at the disk, the path or the
// extension.
func unknownRowPromotable(r database.BookFileCore) bool {
	return r.AcoustIDFingerprintDurationSec > 0 &&
		database.NormalizeDurationSec(r.FileSize, int(math.Round(r.AcoustIDFingerprintDurationSec))) > 0
}

// unknownRowVerdict classifies r. disk is what the caller saw of the file at r's
// path, nil when it has not looked.
//
// The path, extension and disk checks govern only a row that needs a PROBE: one
// the walk would borrow a sibling's duration into is acted on whatever its
// extension (the walk checks the borrow first), and one with a fingerprint
// duration is promoted without a disk look, so a size mismatch says nothing
// about it.
func unknownRowVerdict(r database.BookFileCore, disk *unknownRowDisk) unknownRowSettled {
	switch {
	case r.DurationState() == database.DurationKnown:
		return unknownRowKnown
	case r.Missing:
		return unknownRowMissing
	case r.DurationState() == database.DurationUnreadable && !database.IsDurationTimeoutError(r.Scan.DurationOK.LastError):
		return unknownRowUnreadable
	case r.FilePath == "":
		return unknownRowNoPath
	case r.SkipScan:
		return unknownRowUserExcluded
	case unknownRowPromotable(r):
		return unknownRowActs
	}
	path, err := pathvalidation.CleanAbsolutePath(r.FilePath)
	switch {
	case err != nil:
		return unknownRowBadPath
	case !unknownRowsAudioExts[strings.ToLower(filepath.Ext(path))]:
		return unknownRowNotAudio
	case disk != nil && !disk.Regular:
		return unknownRowNotRegular
	case disk != nil && r.FileSize > 0 && disk.Size > 0 && disk.Size != r.FileSize:
		return unknownRowChanged
	}
	return unknownRowActs
}

// unknownRowActionable reports whether the unknownRows mode acts on r, given
// what the caller saw of the file (nil when it has not looked), and when it does
// not, why and what would settle it, in words a hold can show.
func unknownRowActionable(r database.BookFileCore, disk *unknownRowDisk) (ok bool, why string) {
	switch unknownRowVerdict(r, disk) {
	case unknownRowActs:
		return true, ""
	case unknownRowKnown:
		return false, "its duration is already known"
	case unknownRowMissing:
		return false, "the row is flagged missing although the file is on disk: a library scan refreshes the row"
	case unknownRowUnreadable:
		return false, "its probes failed to the attempt ceiling for a reason that is not a timeout: the row's duration " +
			"state needs an explicit reset before it is probed again"
	case unknownRowNoPath:
		return false, "the row has no file path"
	case unknownRowUserExcluded:
		return false, "the file is excluded from scanning (skip scan)"
	case unknownRowBadPath:
		return false, "the row's path is not an absolute, clean path, so the backfill will not open it"
	case unknownRowNotAudio:
		return false, fmt.Sprintf("its extension %q is not one the duration probe reads (the scanner admits it through the "+
			"configured supported extensions, the probe does not)", strings.ToLower(filepath.Ext(r.FilePath)))
	case unknownRowNotRegular:
		return false, "the path is not a regular file (a directory or special file): a library scan refreshes the row"
	case unknownRowChanged:
		return false, fmt.Sprintf("the file is %d bytes on disk but the row records %d: a library scan refreshes the row",
			disk.Size, r.FileSize)
	default:
		return false, "the backfill leaves this row alone"
	}
}

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
		switch unknownRowVerdict(*r, nil) {
		case unknownRowKnown:
			continue
		case unknownRowMissing:
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
		// The disk is looked at in the probe pass (probeGroup), per file: nothing
		// of it is known here.
		verdict := unknownRowVerdict(r, nil)
		// A sibling's duration is borrowed whatever the row's path or extension:
		// those verdicts are for a row that would need a probe.
		borrowSec := 0
		if verdict == unknownRowBadPath || verdict == unknownRowNotAudio {
			if borrowSec = database.KnownDurationAtPath(donorRows(r.FilePath), r.FileSize, r.FileHash); borrowSec > 0 {
				verdict = unknownRowActs
			}
		}
		// Settled rows take no action, so they never spend the limit; only a row
		// that would be borrowed, promoted or probed does.
		if verdict == unknownRowActs && params.Limit > 0 && actionable >= params.Limit {
			continue
		}
		res.Scanned++
		bc := book(r.BookID)
		switch verdict {
		case unknownRowUnreadable:
			res.SkippedUnreadable++
			bc.SkippedUnreadable++
			continue
		case unknownRowNoPath:
			res.SkippedNoPath++
			bc.SkippedNoPath++
			continue
		case unknownRowUserExcluded:
			res.SkippedUserExcluded++
			bc.SkippedUserExcluded++
			continue
		case unknownRowBadPath:
			res.SkippedBadPath++
			bc.SkippedBadPath++
			continue
		case unknownRowNotAudio:
			res.SkippedNotAudio++
			bc.SkippedNotAudio++
			continue
		}
		actionable++
		if borrowSec == 0 {
			borrowSec = database.KnownDurationAtPath(donorRows(r.FilePath), r.FileSize, r.FileHash)
		}
		if borrowSec > 0 {
			pass1 = append(pass1, unknownRowWork{row: r, sec: borrowSec, src: srcBorrow})
			bc.Borrowed++
			continue
		}
		if unknownRowPromotable(r) {
			sec := database.NormalizeDurationSec(r.FileSize, int(math.Round(r.AcoustIDFingerprintDurationSec)))
			pass1 = append(pass1, unknownRowWork{row: r, sec: sec, src: srcFingerprint})
			bc.PromotedFromFingerprint++
			continue
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
	res.SkippedNotRegular = int(run.ctr.notRegular.Load())
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
// be about other bytes; a path that is a directory or special file is left
// alone and counted under its own reason (SkippedNotRegular). Both of those
// decisions are unknownRowVerdict's, so the folder-books holds read them too.
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
	case statErr != nil:
		r.skipGroupEnvironment(group)
		if r.warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): file cannot be examined: %s; rows left unprobed",
				logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1, logger.SanitizeLogValue(statErr.Error()))
		}
		return nil
	}
	// The shared verdict, now that the file has been looked at: a path that is
	// not a regular file, or one whose size is no longer the row's.
	switch unknownRowVerdict(first.row, &unknownRowDisk{Size: fi.Size(), Regular: fi.Mode().IsRegular()}) {
	case unknownRowNotRegular:
		r.ctr.notRegular.Add(int64(len(group)))
		if r.warns.allow() {
			durLog.Warn("duration-backfill unknownRows: book %s file %s (+%d row(s) at the same path): not a regular file; rows left unprobed",
				logger.SanitizeLogValue(first.row.BookID), logger.SanitizeLogValue(first.row.ID), len(group)-1)
		}
		return nil
	case unknownRowChanged:
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
		return fmt.Sprintf("DRY RUN unknownRows: scanned=%d would-borrow=%d would-promote-from-fingerprint=%d would-probe=%d skipped-unreadable=%d skipped-no-path=%d skipped-user-excluded=%d skipped-bad-path=%d skipped-not-audio=%d skipped-missing=%d not-found=%d (ffprobe is not run in a dry run)",
			r.Scanned, r.Borrowed, r.PromotedFromFingerprint, r.WouldProbe, r.SkippedUnreadable, r.SkippedNoPath, r.SkippedUserExcluded, r.SkippedBadPath, r.SkippedNotAudio, r.SkippedMissing, r.NotFound)
	}
	return fmt.Sprintf("unknownRows: scanned=%d borrowed=%d promoted_from_fingerprint=%d probed_known=%d probe_failed=%d environment_skipped=%d skipped_changed=%d skipped_unreadable=%d skipped_no_path=%d skipped_user_excluded=%d skipped_bad_path=%d skipped_not_audio=%d skipped_not_regular=%d skipped_missing=%d not_found=%d write_errors=%d books_recomputed=%d",
		r.Scanned, r.Borrowed, r.PromotedFromFingerprint, r.ProbedKnown, r.ProbeFailed, r.EnvironmentSkipped, r.SkippedChanged,
		r.SkippedUnreadable, r.SkippedNoPath, r.SkippedUserExcluded, r.SkippedBadPath, r.SkippedNotAudio, r.SkippedNotRegular, r.SkippedMissing, r.NotFound, r.WriteErrors, r.BooksRecomputed)
}
