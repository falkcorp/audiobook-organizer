// file: internal/maintenance/jobs/backfill_file_hashes.go
// version: 1.13.0
// guid: a1000014-0000-0000-0000-000000000014
// last-edited: 2026-10-25

package jobs

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/libroot"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

func init() { maintenance.Register(&backfillFileHashesJob{}) }

type backfillFileHashesJob struct{}

func (j *backfillFileHashesJob) ID() string       { return "backfill-file-hashes" }
func (j *backfillFileHashesJob) Name() string     { return "Backfill File Hashes" }
func (j *backfillFileHashesJob) Category() string { return "files" }
func (j *backfillFileHashesJob) DefaultParams() any {
	return struct {
		DryRun       bool     `json:"dry_run"`
		BookIDs      []string `json:"book_ids,omitempty"`
		MarkAbsent   bool     `json:"mark_absent"`
		ClearMarkers bool     `json:"clear_markers"`
	}{DryRun: true, MarkAbsent: true} // preview by default (owner 2026-09-25); send dry_run=false to apply
}

// hashBackfillScope is the part of the params the job reads itself. dry_run
// is NOT here on purpose: the dispatcher resolves it (omitted = preview) and
// passes it as Run's argument, and the opmode guard refuses a plain-bool
// dry_run field on any named params struct.
//
//   - BookIDs, when set, limits the run to those books' rows: a repair that needs
//     hashes for one set of books (the fragment fixer proves a duplicate copy by
//     hash) must not have to hash the whole library first. Empty means every row.
//   - MarkAbsent (default true) lets the run record, on a row, that hashing it
//     found its file absent (book_file.hash_absent_at). false makes it a pure
//     hash run: absences are still counted, and no marker is written, set or
//     cleared (a stale marker on a row that now hashes stays for a run that
//     records markers, or for the scanner's stat to clear). The
//     Fingerprint and metadata-hash buttons send false so that no click records
//     absences without the preview-then-apply flow having been through it.
//   - ClearMarkers runs the inverse instead of hashing: every in-scope row's
//     hash_absent_at is cleared (dry_run is honoured). It is the undo for a
//     recording pass, e.g. after a mount that was wrong in a way the mount check
//     could not see.
type hashBackfillScope struct {
	BookIDs      []string `json:"book_ids,omitempty"`
	MarkAbsent   *bool    `json:"mark_absent,omitempty"`
	ClearMarkers bool     `json:"clear_markers,omitempty"`
}

// scopeToBooks keeps the rows of the given books, in their original order.
func scopeToBooks(files []database.BookFileCore, bookIDs []string) []database.BookFileCore {
	if len(bookIDs) == 0 {
		return files
	}
	want := make(map[string]bool, len(bookIDs))
	for _, id := range bookIDs {
		want[id] = true
	}
	out := files[:0:0]
	for _, f := range files {
		if want[f.BookID] {
			out = append(out, f)
		}
	}
	return out
}
func (j *backfillFileHashesJob) Description() string {
	return "Compute and store file hashes for book_files missing them. A file whose hash attempt finds it absent is recorded on its row (hash_absent_at, read only by this job) so later runs stat it instead of retrying; a mounted-library check gates the recording and a per-row TSV report lists every row it records"
}

// hashBackfillMissingLogCap bounds how many absent paths, and how many failed
// marker batches, one run writes to the operation log. The first run after this
// change can find hundreds of thousands of absent files; they are all COUNTED
// and written to the TSV report, but one log row each would bury everything else
// in the op log.
const hashBackfillMissingLogCap = 50

// hashBackfillPhase is the checkpoint phase. The index a checkpoint stores is a
// position in the run's row order, and that order changed (file-ID order before,
// (book ID, file ID) now), so the phase was renamed from "scanning": a checkpoint
// written by the old order is ignored instead of resuming at an index that means
// a different set of rows.
const hashBackfillPhase = "scanning-by-book"

// fileAbsent reports whether err means the path is definitively not there:
// not found (ENOENT) or a path component that is not a directory (ENOTDIR).
// Anything else -- permission denied, an I/O error, a stale network handle --
// leaves the file's state unknown and is NOT an answer, so it is never used to
// record a row as absent.
func fileAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// hashBackfillCounts is one run's tally.
//
// Row outcomes. Every row that has no hash when the run reaches it lands in
// exactly one of these nine, and a row that has one is not counted at all:
//
//	hashed                hash computed (and, unless dry-run, stored)
//	skipped_known_missing already known absent (hash_absent_at or Missing) and still not on disk
//	newly_missing         hash attempt found the file absent; recorded now (dry-run: would be;
//	                      mark_absent=false: found, not recorded)
//	read_errors           hash failed for a reason other than not-found; retried next run
//	write_errors          the hash was computed but could not be saved
//	skipped_no_path       the row has an empty path; nothing to stat or hash
//	unverified_absent     not found, but outside the library root, so the mount check says nothing
//	                      about it; counted, never recorded
//	skipped_changed       the row was repointed, hashed, deleted or already marked by someone else
//	                      between this run reading it and writing the marker
//	marker_errors         the absence marker for the row could not be written
//
// Operation counters. These count writes, not rows, and a row can appear in one
// of them as well as in an outcome above: a hashed row whose stale marker was
// then cleared is one hashed and one markers_cleared; a clear that failed is one
// hashed and one clear_errors. (A mark_absent=false run clears nothing.) recompute_errors counts books whose aggregates
// did not follow a marker write that DID land (those rows are already in
// newly_missing or markers_cleared).
//
// Atomics: the worker pool updates them concurrently.
type hashBackfillCounts struct {
	hashed              atomic.Int64
	skippedKnownMissing atomic.Int64
	newlyMissing        atomic.Int64
	readErrors          atomic.Int64
	writeErrors         atomic.Int64
	skippedNoPath       atomic.Int64
	unverifiedAbsent    atomic.Int64
	skippedChanged      atomic.Int64
	markerErrors        atomic.Int64

	markersCleared  atomic.Int64
	clearErrors     atomic.Int64
	recomputeErrors atomic.Int64
}

// summary is the one-line result stored on the operation log. In a dry run the
// same counts are what an apply WOULD do and nothing is written.
func (c *hashBackfillCounts) summary(dryRun, markAbsent, clearMode bool, reportPath string) string {
	prefix := "Backfilled hashes"
	switch {
	case clearMode && dryRun:
		prefix = "Dry run, nothing written (markers_cleared is what an apply would clear)"
	case clearMode:
		prefix = "Cleared hash-absent markers"
	case dryRun:
		prefix = "Dry run, nothing written (counts are what an apply would do)"
	}
	s := fmt.Sprintf("%s: hashed=%d skipped_known_missing=%d newly_missing=%d read_errors=%d write_errors=%d "+
		"skipped_no_path=%d unverified_absent=%d skipped_changed=%d marker_errors=%d "+
		"markers_cleared=%d clear_errors=%d recompute_errors=%d",
		prefix, c.hashed.Load(), c.skippedKnownMissing.Load(), c.newlyMissing.Load(), c.readErrors.Load(),
		c.writeErrors.Load(), c.skippedNoPath.Load(), c.unverifiedAbsent.Load(), c.skippedChanged.Load(),
		c.markerErrors.Load(), c.markersCleared.Load(), c.clearErrors.Load(), c.recomputeErrors.Load())
	if !markAbsent && !clearMode {
		s += " (mark_absent=false: absences are counted in newly_missing; no marker was written or cleared)"
	}
	if reportPath != "" {
		s += " report=" + reportPath
	}
	return s
}

// Report buckets. A dry run uses the would-* spelling for the rows an apply
// would write, so a report is unambiguous about whether anything was written.
const (
	hbBucketMarked        = "marked-absent"
	hbBucketWouldMark     = "would-mark-absent"
	hbBucketCleared       = "cleared-marker"
	hbBucketWouldClear    = "would-clear-marker"
	hbBucketReadError     = "read-error"
	hbBucketNoPath        = "no-path"
	hbBucketUnverified    = "unverified-absent"
	hbBucketSkippedChange = "skipped-changed"
	hbBucketMarkerError   = "marker-error"
	hbBucketClearError    = "clear-error"
)

// hashBackfillReport streams the run's per-row decisions to a TSV (bucket,
// file_id, book_id, path, reason), the way mark-missing-files does, so a person
// can read the preview before applying and audit or reverse an apply after it.
// It streams (a first apply can list hundreds of thousands of rows) and is safe
// for the worker pool. A nil report discards, for runs that record nothing.
type hashBackfillReport struct {
	mu  sync.Mutex
	f   *os.File
	w   *bufio.Writer
	err error // first write error; later writes are dropped
}

var hbTSVClean = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace

// openHashBackfillReport opens path for writing. A resumed run appends to the
// report its earlier part wrote; a fresh one truncates and writes the header.
func openHashBackfillReport(path string, resumed bool) (*hashBackfillReport, error) {
	flags := os.O_WRONLY | os.O_CREATE
	if resumed {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o664)
	if err != nil {
		return nil, err
	}
	r := &hashBackfillReport{f: f, w: bufio.NewWriterSize(f, 64<<10)}
	if st, serr := f.Stat(); !resumed || serr != nil || st.Size() == 0 {
		_, r.err = r.w.WriteString("bucket\tfile_id\tbook_id\tpath\treason\n")
	}
	return r, r.err
}

func (r *hashBackfillReport) row(bucket string, bf database.BookFileCore, reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	_, r.err = fmt.Fprintf(r.w, "%s\t%s\t%s\t%s\t%s\n", bucket, bf.ID, bf.BookID, hbTSVClean(bf.FilePath), hbTSVClean(reason))
}

// flush pushes buffered rows to disk and returns the first write error, so the
// run can stop before it writes more markers it cannot account for.
func (r *hashBackfillReport) flush() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = r.w.Flush()
	}
	return r.err
}

func (r *hashBackfillReport) close() error {
	if r == nil {
		return nil
	}
	ferr := r.flush()
	cerr := r.f.Close()
	return errors.Join(ferr, cerr)
}

var hbOpIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// hashBackfillReportName is the report's file name under {root}/.reports.
func hashBackfillReportName(opID string) string {
	if opID == "" {
		opID = "unknown-op"
	}
	return "backfill-file-hashes-" + hbOpIDUnsafe.ReplaceAllString(opID, "_") + ".tsv"
}

// batchErrors splits what ModifyBookFiles returned (an errors.Join of per-row
// errors and at most one recompute error) into what the caller reports apart:
// a recompute that did not follow rows which were written, a write whose fsync
// is unknown (the row IS written), and everything else (a row that failed).
// Cancellation is dropped: the caller returns ctx.Err() itself.
func batchErrors(err error) (recompute, durabilityUnknown []error, other int) {
	if err == nil {
		return nil, nil, 0
	}
	parts := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		parts = j.Unwrap()
	}
	for _, e := range parts {
		switch {
		case errors.Is(e, database.ErrBookAggregatesRecompute):
			recompute = append(recompute, e)
		case errors.Is(e, database.ErrBookFileDurabilityUnknown):
			durabilityUnknown = append(durabilityUnknown, e)
		case errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
		default:
			other++
		}
	}
	return recompute, durabilityUnknown, other
}

// hashBackfillWorkers bounds the parallel hashing pool. Hashing reads whole
// files (<=100MB) or ~20MB of head+tail chunks each, off what in production is
// a network-attached volume, so this work is IO-bound: a handful of workers
// hides mount latency without thrashing the filesystem or exhausting file
// descriptors. Override with ABK_HASH_BACKFILL_WORKERS.
func hashBackfillWorkers() int {
	const def = 8
	if v := os.Getenv("ABK_HASH_BACKFILL_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// hashBackfillChunkSize is how many files are dispatched to the worker pool
// before a checkpoint is written. See the checkpoint invariant in Run.
const hashBackfillChunkSize = 512

// hashBackfillRun is one Run's state, shared by the worker pool.
type hashBackfillRun struct {
	store      maintenance.JobStore
	mod        database.BookFileBatchModifier // nil only in a dry run
	reporter   maintenance.ProgressReporter
	report     *hashBackfillReport
	counts     hashBackfillCounts
	dryRun     bool
	markAbsent bool   // record absences (mark_absent)
	clearMode  bool   // clear markers instead of hashing
	root       string // cleaned library root; "" when no root check applies
	workers    int

	// mu serializes reporter (maintenance.ProgressAdapter increments a plain
	// int and is NOT concurrency-safe) and guards absent and clears. The guarded
	// calls are microseconds; hashing dominates.
	mu     sync.Mutex
	absent []database.BookFileCore // this chunk: hash attempt found the file gone, not yet recorded
	clears []database.BookFileCore // this chunk: hashed (or clear mode) rows whose marker is to be cleared

	missingLogged atomic.Int64
	batchLogged   atomic.Int64
}

func (r *hashBackfillRun) log(level, msg string, detail *string) {
	r.mu.Lock()
	r.reporter.Log(level, msg, detail)
	r.mu.Unlock()
}

// logBatchFailure logs a failed marker batch, capped like the absent paths.
func (r *hashBackfillRun) logBatchFailure(msg string, err error) {
	if r.batchLogged.Add(1) > hashBackfillMissingLogCap {
		return
	}
	d := err.Error()
	r.log("error", msg, &d)
}

// verifiable reports whether an absence at path can be trusted: the path is
// under the library root whose mount was just verified. A path elsewhere (an
// import folder, an iTunes share) has no such evidence, so its not-found could
// be an unmounted share and is never recorded.
func (r *hashBackfillRun) verifiable(path string) bool {
	return r.root == "" || pathutil.IsWithin(filepath.Clean(path), r.root)
}

// processRow handles one row on a worker: classify it, hash it, and queue the
// marker writes for the chunk's flush.
func (r *hashBackfillRun) processRow(bf database.BookFileCore) {
	r.mu.Lock()
	r.reporter.Increment()
	r.mu.Unlock()

	if r.clearMode {
		if bf.HashAbsentAt != nil {
			r.mu.Lock()
			r.clears = append(r.clears, bf)
			r.mu.Unlock()
		}
		return
	}
	if bf.FileHash != "" {
		return
	}
	if strings.TrimSpace(bf.FilePath) == "" {
		r.counts.skippedNoPath.Add(1)
		r.report.row(hbBucketNoPath, bf, "row has no file path")
		return
	}
	known := bf.HashAbsentAt != nil || bf.Missing
	if known {
		// Known absent: a cheap stat decides whether to hash at all, so the
		// ~515k rows whose files are gone cost one stat each instead of an
		// open + failed hash + warning every run. Only a definitive not-found
		// skips; a successful stat (the file reappeared) or an inconclusive one
		// falls through to the hash attempt, which either succeeds or reports the
		// real error. The skip is always decided by a fresh stat, never by the
		// marker alone, so a stale marker costs one stat, not a wrong answer.
		if _, serr := os.Stat(bf.FilePath); serr != nil && fileAbsent(serr) {
			r.counts.skippedKnownMissing.Add(1)
			return
		}
	}
	// filehash.BookFileHash directly: scanner.ComputeFileHash is only a thin
	// wrapper over it, and filehash is the single entry point for the digest.
	hash, herr := filehash.BookFileHash(bf.FilePath)
	if herr != nil {
		msg := herr.Error()
		if fileAbsent(herr) {
			r.onAbsent(bf, known, msg)
			return
		}
		r.counts.readErrors.Add(1)
		r.report.row(hbBucketReadError, bf, msg)
		slog.Warn("backfill-file-hashes hash failed", "file", bf.FilePath, "details", msg)
		r.log("warn", "Hash computation failed for "+bf.FilePath, &msg)
		return // one unreadable file must not abort the run
	}
	if !r.dryRun {
		if serr := r.store.SetBookFileHash(bf.ID, hash); serr != nil {
			r.counts.writeErrors.Add(1)
			msg := serr.Error()
			slog.Error("backfill-file-hashes SetBookFileHash failed", "details", msg)
			r.log("error", "Failed to save file hash for "+bf.FilePath, &msg)
			return
		}
	}
	r.counts.hashed.Add(1)
	if bf.HashAbsentAt != nil && r.markAbsent {
		// The file exists after all: queue the stale marker's clear. A pure hash
		// run (mark_absent=false) writes no marker of any kind, so it leaves the
		// stale marker for a run that records markers, or for the scanner's stat.
		r.mu.Lock()
		r.clears = append(r.clears, bf)
		r.mu.Unlock()
	}
}

// onAbsent routes a hash attempt that found the file absent.
func (r *hashBackfillRun) onAbsent(bf database.BookFileCore, known bool, msg string) {
	if known {
		// The stat was inconclusive but the row was already known absent.
		r.counts.skippedKnownMissing.Add(1)
		return
	}
	if !r.verifiable(bf.FilePath) {
		r.counts.unverifiedAbsent.Add(1)
		r.report.row(hbBucketUnverified, bf, "outside the library root, so its mount is unverified; not recorded")
		return
	}
	if !r.markAbsent {
		r.counts.newlyMissing.Add(1)
		return
	}
	r.mu.Lock()
	r.absent = append(r.absent, bf)
	if r.missingLogged.Add(1) <= hashBackfillMissingLogCap {
		r.reporter.Log("warn", "File not found: "+bf.FilePath, &msg)
	}
	r.mu.Unlock()
}

// groupByBook partitions rows by book, each group in file-ID order, and returns
// the groups in book-ID order. Two workers can then never touch rows of one
// book, which is what lets a book's aggregates be recomputed once.
func groupByBook(rows []database.BookFileCore) [][]database.BookFileCore {
	by := map[string][]database.BookFileCore{}
	for _, r := range rows {
		by[r.BookID] = append(by[r.BookID], r)
	}
	ids := make([]string, 0, len(by))
	for id := range by {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([][]database.BookFileCore, 0, len(ids))
	for _, id := range ids {
		g := by[id]
		slices.SortFunc(g, func(a, b database.BookFileCore) int { return cmp.Compare(a.ID, b.ID) })
		out = append(out, g)
	}
	return out
}

// batchResult is how one book's ModifyBookFiles call resolved each row.
type batchResult struct {
	changed map[string]bool
	skipped map[string]bool // the precondition refused the row
	ran     map[string]bool // the callback ran (so a missing change is a failed write)
	err     error
}

// modifyGroup runs fn over one book's rows through the batch modifier, with the
// per-row bookkeeping the report and the counters both need. precondition is
// evaluated UNDER the row's lock against the row as it is stored now, never
// against the snapshot this run took hours ago.
func (r *hashBackfillRun) modifyGroup(ctx context.Context, group []database.BookFileCore, apply func(row *database.BookFile) bool) batchResult {
	expect := make(map[string]database.BookFileCore, len(group))
	ids := make([]string, len(group))
	for i, row := range group {
		expect[row.ID] = row
		ids[i] = row.ID
	}
	res := batchResult{changed: map[string]bool{}, skipped: map[string]bool{}, ran: map[string]bool{}}
	changed, err := r.mod.ModifyBookFiles(ctx, group[0].BookID, ids, func(row *database.BookFile) error {
		res.ran[row.ID] = true
		want, ok := expect[row.ID]
		if !ok || row.FilePath != want.FilePath || !apply(row) {
			res.skipped[row.ID] = true
			return database.ErrSkipBookFileWrite
		}
		return nil
	})
	for _, id := range changed {
		res.changed[id] = true
	}
	res.err = err
	return res
}

// noteBatchErrors counts and logs the parts of a batch error that are not row
// failures: a recompute that did not follow rows which were written, and a
// write whose fsync is unknown. Row failures are attributed per row by the
// caller, from which rows the callback ran for and which came back changed.
func (r *hashBackfillRun) noteBatchErrors(bookID, what string, err error) {
	recompute, durability, _ := batchErrors(err)
	for _, e := range recompute {
		r.counts.recomputeErrors.Add(1)
		r.logBatchFailure("Recorded "+what+" for book "+bookID+" but its aggregates did not follow", e)
	}
	for _, e := range durability {
		r.logBatchFailure("Wrote "+what+" for book "+bookID+" but fsync is unknown", e)
	}
}

// flushAbsent records this chunk's newly absent rows, one batch per book.
func (r *hashBackfillRun) flushAbsent(ctx context.Context, rows []database.BookFileCore) error {
	if len(rows) == 0 {
		return nil
	}
	if r.dryRun {
		for _, bf := range rows {
			r.counts.newlyMissing.Add(1)
			r.report.row(hbBucketWouldMark, bf, "hash attempt found the file absent")
		}
		return nil
	}
	stamp := time.Now().UTC()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.workers)
	for _, group := range groupByBook(rows) {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			res := r.modifyGroup(gctx, group, func(row *database.BookFile) bool {
				// Still un-hashed and not yet marked; Missing rows are owned by the
				// scanner and are never marked here.
				if row.FileHash != "" || row.HashAbsentAt != nil || row.Missing {
					return false
				}
				t := stamp
				row.HashAbsentAt = &t
				return true
			})
			r.noteBatchErrors(group[0].BookID, "absent markers", res.err)
			for _, bf := range group {
				switch {
				case res.changed[bf.ID]:
					r.counts.newlyMissing.Add(1)
					r.report.row(hbBucketMarked, bf, "hash attempt found the file absent")
				case res.skipped[bf.ID]:
					r.counts.skippedChanged.Add(1)
					r.report.row(hbBucketSkippedChange, bf, "row changed since it was read; not marked")
				case res.ran[bf.ID]:
					r.counts.markerErrors.Add(1)
					r.report.row(hbBucketMarkerError, bf, "marker write failed")
				case gctx.Err() == nil:
					r.counts.skippedChanged.Add(1)
					r.report.row(hbBucketSkippedChange, bf, "row no longer present or unreadable; not marked")
				}
				// A cancelled flush leaves rows that never ran; they are neither
				// changed nor changed-under-us, and the checkpoint was not written,
				// so a resume reconsiders them.
			}
			if _, _, other := batchErrors(res.err); other > 0 && gctx.Err() == nil {
				r.logBatchFailure("Failed to record absent markers of book "+group[0].BookID, res.err)
			}
			return gctx.Err()
		})
	}
	return g.Wait()
}

// flushClears clears this chunk's stale markers, one batch per book, so a book
// whose 800 chapters all came back recomputes once, not 800 times.
func (r *hashBackfillRun) flushClears(ctx context.Context, rows []database.BookFileCore) error {
	if len(rows) == 0 {
		return nil
	}
	if r.dryRun {
		for _, bf := range rows {
			r.counts.markersCleared.Add(1)
			r.report.row(hbBucketWouldClear, bf, "marker would be cleared")
		}
		return nil
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.workers)
	for _, group := range groupByBook(rows) {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			res := r.modifyGroup(gctx, group, func(row *database.BookFile) bool {
				if row.HashAbsentAt == nil {
					return false // already cleared (a scanner stat does that)
				}
				row.HashAbsentAt = nil
				return true
			})
			r.noteBatchErrors(group[0].BookID, "cleared markers", res.err)
			for _, bf := range group {
				switch {
				case res.changed[bf.ID]:
					r.counts.markersCleared.Add(1)
					r.report.row(hbBucketCleared, bf, "marker cleared")
				case res.ran[bf.ID] && !res.skipped[bf.ID]:
					r.counts.clearErrors.Add(1)
					r.report.row(hbBucketClearError, bf, "marker clear failed")
				}
			}
			if _, _, other := batchErrors(res.err); other > 0 && gctx.Err() == nil {
				r.logBatchFailure("Failed to clear absent markers of book "+group[0].BookID, res.err)
			}
			return gctx.Err()
		})
	}
	return g.Wait()
}

// Job supports checkpoint-based resume after restart.
func (j *backfillFileHashesJob) CanResume() bool { return true }
func (j *backfillFileHashesJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) (retErr error) {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	var scope hashBackfillScope
	if raw := maintenance.RawParamsFromCtx(ctx); len(raw) > 0 {
		if jerr := json.Unmarshal(raw, &scope); jerr != nil {
			return fmt.Errorf("backfill-file-hashes: params: %w", jerr)
		}
		files = scopeToBooks(files, scope.BookIDs)
	}
	// One fixed order, grouped by book. Two reasons, both load-bearing: the
	// checkpoint is an INDEX into this slice, so resume needs the same order on
	// every run; and the marker flush recomputes a book once per chunk it
	// appears in, so a book's rows must sit next to each other or a book could
	// be recomputed once per file. (Before 2026-10-25 the order was the store's
	// own: file-ID order with the in-memory index on, key order otherwise; the
	// checkpoint phase was renamed so a checkpoint from that order is ignored.)
	// The slice is the store's own fresh copy, so sorting in place is free.
	slices.SortFunc(files, func(a, b database.BookFileCore) int {
		if c := cmp.Compare(a.BookID, b.BookID); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	reporter.SetTotal(len(files))

	run := &hashBackfillRun{
		store:      store,
		reporter:   reporter,
		dryRun:     dryRun,
		markAbsent: scope.MarkAbsent == nil || *scope.MarkAbsent,
		clearMode:  scope.ClearMarkers,
		workers:    hashBackfillWorkers(),
	}
	if run.clearMode {
		run.markAbsent = false
	}
	recordsMarkers := run.markAbsent || run.clearMode // a mode that writes, or previews writing, markers

	// Resolved up front and refused loudly: a store that cannot batch the marker
	// writes would otherwise fall back to a per-file recompute (the cost this
	// capability exists to avoid) or silently never record anything. A dry run
	// writes nothing and needs no writer.
	if !dryRun {
		mod, ok := database.AsCapability[database.BookFileBatchModifier](store)
		if !ok {
			return fmt.Errorf("backfill-file-hashes: store %T does not implement database.BookFileBatchModifier", store)
		}
		run.mod = mod
	}

	root := config.AppConfig.RootDir
	if run.markAbsent {
		// An unmounted share leaves an empty directory, so every path under it
		// answers not-found and a hash attempt would read the whole library as
		// gone. Refuse before anything is recorded.
		if merr := libroot.Mounted(root); merr != nil {
			return fmt.Errorf("backfill-file-hashes: refusing to record absent files: %w", merr)
		}
		run.root = filepath.Clean(root)
	} else if run.clearMode {
		// Clearing trusts no not-found, but the report goes under the same root:
		// resolving it on an unmounted mountpoint would create {root}/.reports
		// there. libroot.Mounted ignores that directory, so it would not be
		// fooled by it later, but nothing should be written to a bare mountpoint.
		if merr := libroot.Mounted(root); merr != nil {
			return fmt.Errorf("backfill-file-hashes: refusing to write the clear report: %w", merr)
		}
	}
	if !dryRun && recordsMarkers {
		// The scanner stat-clears the same marker, and a marker write races a scan
		// that rewrites the same row. What stands between them, exactly:
		//   - this check refuses to START a marker write while a scan runs (the op
		//     registry cannot give this job library.scan's ConcurrencyKey: a shared
		//     key would break per-job self-exclusion, see repoint_version_primary.go,
		//     and the legacy Job.Run has no ScanController, so holding the scan
		//     stand-down around the writes would mean plumbing one through
		//     maintenance.JobStore for every legacy job);
		//   - every row write re-checks, under the row's lock, that the row still
		//     has the same path, no hash, and no marker.
		// That does NOT cover a scan that starts after this check and commits
		// between the read of a row and its commit on a path that takes no file
		// stripe (a batch upsert, a repoint): the job writes back its full row
		// copy, so such a write can be overwritten with the row as it was read. The
		// damage is bounded by design: the marker is its own field (never Missing),
		// the skip it licenses is always re-decided by a fresh stat, and a stale or
		// lost marker costs one extra stat or one extra hash attempt on the next
		// run, never a wrong answer.
		if serr := refuseDuringLibraryScan(store, "backfill-file-hashes"); serr != nil {
			return serr
		}
	}

	// Resume support: load checkpoint if present.
	opID := maintenance.OperationIDFromCtx(ctx)
	resumeIndex := 0
	if opID != "" {
		if cp, _ := operations.LoadCheckpoint(store, opID); cp != nil && cp.Phase == hashBackfillPhase {
			resumeIndex = cp.PhaseIndex
		}
	}

	reportPath := ""
	if recordsMarkers {
		path, rerr := libroot.ReportPath(root, "", hashBackfillReportName(opID))
		if rerr != nil {
			return fmt.Errorf("backfill-file-hashes: %w", rerr)
		}
		report, oerr := openHashBackfillReport(path, resumeIndex > 0)
		if oerr != nil {
			return fmt.Errorf("backfill-file-hashes: open report %s: %w", path, oerr)
		}
		run.report = report
		reportPath = path
		run.log("info", "Per-row report: "+path, nil)
		defer func() {
			if cerr := report.close(); cerr != nil && retErr == nil {
				retErr = fmt.Errorf("backfill-file-hashes: report %s: %w", path, cerr)
			}
		}()
	}

	// Process in ORDERED chunks. Each chunk runs through a bounded worker pool,
	// and the checkpoint is written only AFTER the whole chunk, markers and
	// report included, completes. That preserves the invariant the old serial
	// loop had for free -- every index below the checkpoint has been processed --
	// which a naive fan-out would break: a run killed mid-flight could otherwise
	// checkpoint an index past rows still being hashed, and resume would skip
	// them forever. Distinct rows are hashed concurrently; SetBookFileHash
	// round-trips the full record (no field wipe) and writes one distinct key per
	// row, so parallel writes across different rows do not race.
	for start := resumeIndex; start < len(files); start += hashBackfillChunkSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		end := min(start+hashBackfillChunkSize, len(files))

		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(run.workers)
		for i := start; i < end; i++ {
			bf := files[i]
			g.Go(func() error {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				run.processRow(bf)
				return nil
			})
		}
		// Per-file failures are swallowed in processRow, so a non-nil result here
		// means the context was cancelled. Return it: the checkpoint from the last
		// fully-completed chunk stands, and resume restarts at that boundary.
		if gerr := g.Wait(); gerr != nil {
			return gerr
		}

		run.mu.Lock()
		absent, clears := run.absent, run.clears
		run.absent, run.clears = nil, nil
		run.mu.Unlock()

		if len(absent) > 0 {
			// Re-check the mount right before the write: it can drop mid-run, and
			// the whole chunk's not-founds would then be wrong together.
			if merr := libroot.Mounted(root); merr != nil {
				return fmt.Errorf("backfill-file-hashes: refusing to record %d absent files in this chunk: %w", len(absent), merr)
			}
			if ferr := run.flushAbsent(ctx, absent); ferr != nil {
				return ferr
			}
		}
		if ferr := run.flushClears(ctx, clears); ferr != nil {
			return ferr
		}
		if rerr := run.report.flush(); rerr != nil {
			return fmt.Errorf("backfill-file-hashes: report %s: %w", reportPath, rerr)
		}

		// Periodic checkpoint so long runs can resume after restart.
		if opID != "" {
			_ = operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", hashBackfillPhase, end, len(files))
		}
	}

	// Clear any saved state on clean completion.
	if opID != "" {
		_ = operations.ClearState(store, opID)
	}

	// Save a lightweight operation summary for the UI and activity feed.
	res := run.counts.summary(dryRun, run.markAbsent, run.clearMode, reportPath)
	run.log("info", res, nil)
	now := time.Now()
	opLog := &database.OperationSummaryLog{
		ID:          opID,
		Type:        "backfill-file-hashes",
		Status:      "completed",
		Progress:    1.0,
		Result:      &res,
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	_ = store.SaveOperationSummaryLog(opLog)

	slog.Info("backfill-file-hashes complete")
	return nil
}

// Policy: ResumeRestart because this job checkpoints via
// operations.SaveCheckpoint, so a resumed run has real progress to reload.
// PR-2 moves it to reporter.Checkpoint; the policy is unchanged by that move.
// ConcurrencyKey stays empty (derived per job) for the reason Run gives for the
// scan interlock.
func (j *backfillFileHashesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
