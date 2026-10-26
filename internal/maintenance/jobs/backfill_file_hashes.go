// file: internal/maintenance/jobs/backfill_file_hashes.go
// version: 2.0.0
// guid: a1000014-0000-0000-0000-000000000014
// last-edited: 2026-10-25

package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
		DryRun  bool     `json:"dry_run"`
		BookIDs []string `json:"book_ids,omitempty"`
	}{DryRun: true} // preview by default (owner 2026-09-25); send dry_run=false to apply
}

// hashBackfillScope is the part of the params the job reads itself. dry_run
// is NOT here on purpose: the dispatcher resolves it (omitted = preview) and
// passes it as Run's argument, and the opmode guard refuses a plain-bool
// dry_run field on any named params struct. BookIDs, when set, limits the
// run to those books' rows: a repair that needs hashes for one set of books
// (the fragment fixer proves a duplicate copy by hash) must not have to hash
// the whole library first. Empty means every row, as before.
type hashBackfillScope struct {
	BookIDs []string `json:"book_ids,omitempty"`
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
	return "Compute and store file hashes for book_files missing them. A file that is not on disk is counted as not_found (one capped log line set and a per-row TSV report under the library's .reports directory), not warned about once per file; nothing is written to the database for it"
}

// hashBackfillNotFoundLogCap bounds how many not-found paths one run writes to
// the operation log. A library can hold hundreds of thousands of rows whose
// files are gone (a prod preview found about 515k rows with no hash, every
// sampled failure a file missing on the server); they are all COUNTED and
// written to the TSV report, but one log row each would bury everything else in
// the op log.
const hashBackfillNotFoundLogCap = 50

// fileAbsent reports whether err means the path is definitively not there:
// not found (ENOENT) or a path component that is not a directory (ENOTDIR).
// Anything else -- permission denied, an I/O error, a stale network handle --
// leaves the file's state unknown, so it stays a read error and is retried.
func fileAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// hashBackfillCounts is one run's tally. Every row that has no hash when the run
// reaches it lands in exactly one of these six, and a row that has one is not
// counted at all:
//
//	hashed                   hash computed (and, unless dry-run, stored)
//	not_found                the hash attempt found the file absent (ENOENT/ENOTDIR)
//	unverified_outside_root  not found, but the path is outside the library root, so
//	                         the mount check says nothing about it (an import folder,
//	                         an iTunes share that may simply be unmounted)
//	read_errors              hash failed for a reason other than not-found; retried next run
//	write_errors             the hash was computed but could not be saved
//	skipped_no_path          the row has an empty path; nothing to hash
//
// Atomics: the worker pool updates them concurrently.
type hashBackfillCounts struct {
	hashed                atomic.Int64
	notFound              atomic.Int64
	unverifiedOutsideRoot atomic.Int64
	readErrors            atomic.Int64
	writeErrors           atomic.Int64
	skippedNoPath         atomic.Int64
}

// summary is the one-line result stored on the operation log. In a dry run the
// same counts are what an apply WOULD do and nothing is written.
func (c *hashBackfillCounts) summary(dryRun bool, reportPath string) string {
	prefix := "Backfilled hashes"
	if dryRun {
		prefix = "Dry run, nothing written (counts are what an apply would do)"
	}
	s := fmt.Sprintf("%s: hashed=%d not_found=%d unverified_outside_root=%d read_errors=%d write_errors=%d skipped_no_path=%d",
		prefix, c.hashed.Load(), c.notFound.Load(), c.unverifiedOutsideRoot.Load(), c.readErrors.Load(),
		c.writeErrors.Load(), c.skippedNoPath.Load())
	if reportPath != "" {
		s += " report=" + reportPath
	}
	return s
}

// Report buckets: every row the run did not hash.
const (
	hbBucketNotFound   = "not-found"
	hbBucketOutside    = "not-found-outside-root"
	hbBucketReadError  = "read-error"
	hbBucketWriteError = "write-error"
	hbBucketNoPath     = "no-path"
)

// hashBackfillReport streams the run's per-row decisions to a TSV (bucket,
// file_id, book_id, path, reason), the way mark-missing-files does, so a person
// can read what is missing before deciding what to do about it. It streams (a
// run can list hundreds of thousands of rows) and is safe for the worker pool.
// A nil report discards, for a run with no library root to put it under.
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

// flush pushes buffered rows to disk and returns the first write error.
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

// hashBackfillReportName is the report's file name under {root}/.reports. It is
// unique to the run: the operation ID when there is one (a resume of the same op
// appends to it), otherwise a timestamp, so two runs that have no op ID never
// truncate each other's report.
func hashBackfillReportName(opID string, now time.Time) string {
	id := hbOpIDUnsafe.ReplaceAllString(opID, "_")
	if opID == "" {
		id = "adhoc-" + strconv.FormatInt(now.UnixNano(), 10)
	}
	return "backfill-file-hashes-" + id + ".tsv"
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
	store    maintenance.JobStore
	reporter maintenance.ProgressReporter
	report   *hashBackfillReport
	counts   hashBackfillCounts
	dryRun   bool
	root     string // cleaned library root; "" when none is configured
	workers  int

	// mu serializes reporter: maintenance.ProgressAdapter increments a plain int
	// and is NOT concurrency-safe. The guarded calls are microseconds; hashing
	// dominates, so this does not meaningfully serialize the pool.
	mu sync.Mutex

	notFoundLogged atomic.Int64
}

func (r *hashBackfillRun) log(level, msg string, detail *string) {
	r.mu.Lock()
	r.reporter.Log(level, msg, detail)
	r.mu.Unlock()
}

// outsideRoot reports whether path is outside the library root whose mount was
// checked at the start of the run. A not-found there says nothing about that
// mount (an import folder or an iTunes share may simply be unmounted), so it is
// counted apart.
func (r *hashBackfillRun) outsideRoot(path string) bool {
	return r.root != "" && !pathutil.IsWithin(filepath.Clean(path), r.root)
}

// processRow handles one row on a worker.
func (r *hashBackfillRun) processRow(bf database.BookFileCore) {
	r.mu.Lock()
	r.reporter.Increment()
	r.mu.Unlock()

	if bf.FileHash != "" {
		return
	}
	if strings.TrimSpace(bf.FilePath) == "" {
		r.counts.skippedNoPath.Add(1)
		r.report.row(hbBucketNoPath, bf, "row has no file path")
		return
	}
	// filehash.BookFileHash directly: scanner.ComputeFileHash is only a thin
	// wrapper over it, and filehash is the single entry point for the digest.
	hash, herr := filehash.BookFileHash(bf.FilePath)
	if herr != nil {
		msg := herr.Error()
		if fileAbsent(herr) {
			r.onNotFound(bf, msg)
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
			r.report.row(hbBucketWriteError, bf, msg)
			slog.Error("backfill-file-hashes SetBookFileHash failed", "details", msg)
			r.log("error", "Failed to save file hash for "+bf.FilePath, &msg)
			return
		}
	}
	r.counts.hashed.Add(1)
}

// onNotFound counts a hash attempt that found the file absent. Nothing is
// written to the database for it: the row stays as it is, and the TSV report
// lists it. Only the first hashBackfillNotFoundLogCap are logged.
func (r *hashBackfillRun) onNotFound(bf database.BookFileCore, msg string) {
	bucket, reason := hbBucketNotFound, "file not found"
	if r.outsideRoot(bf.FilePath) {
		r.counts.unverifiedOutsideRoot.Add(1)
		bucket, reason = hbBucketOutside, "file not found outside the library root, so its mount is unverified"
	} else {
		r.counts.notFound.Add(1)
	}
	if bf.Missing {
		reason += " (row already flagged missing)"
	}
	r.report.row(bucket, bf, reason)
	if r.notFoundLogged.Add(1) <= hashBackfillNotFoundLogCap {
		r.log("warn", "File not found: "+bf.FilePath, &msg)
	}
}

// Job supports checkpoint-based resume after restart.
func (j *backfillFileHashesJob) CanResume() bool { return true }
func (j *backfillFileHashesJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) (retErr error) {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	if raw := maintenance.RawParamsFromCtx(ctx); len(raw) > 0 {
		var p hashBackfillScope
		if jerr := json.Unmarshal(raw, &p); jerr != nil {
			return fmt.Errorf("backfill-file-hashes: params: %w", jerr)
		}
		files = scopeToBooks(files, p.BookIDs)
	}
	reporter.SetTotal(len(files))

	run := &hashBackfillRun{
		store:    store,
		reporter: reporter,
		dryRun:   dryRun,
		workers:  hashBackfillWorkers(),
	}

	// Resume support: load checkpoint if present.
	opID := maintenance.OperationIDFromCtx(ctx)
	resumeIndex := 0
	if opID != "" {
		if cp, _ := operations.LoadCheckpoint(store, opID); cp != nil {
			// resume phase 'scanning'
			if cp.Phase == "scanning" {
				resumeIndex = cp.PhaseIndex
			}
		}
	}

	reportPath := ""
	if root := strings.TrimSpace(config.AppConfig.RootDir); root != "" {
		// An unmounted share leaves an empty directory, so every path under it
		// answers not-found and the whole library would be reported as gone.
		// Nothing is written for a not-found, so this is only a guard against a
		// useless, misleading run; it is a coarse check (libroot.Mounted), not a
		// proof of a healthy mount. It runs BEFORE the report is resolved because
		// that creates {root}/.reports.
		if merr := libroot.Mounted(root); merr != nil {
			return fmt.Errorf("backfill-file-hashes: refusing to run: %w", merr)
		}
		run.root = filepath.Clean(root)
		path, rerr := libroot.ReportPath(root, "", hashBackfillReportName(opID, time.Now()))
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
	// and the checkpoint is written only AFTER the whole chunk completes. That
	// preserves the invariant the old serial loop had for free -- every index
	// below the checkpoint has been processed -- which a naive fan-out would
	// break: a run killed mid-flight could otherwise checkpoint an index past
	// rows still being hashed, and resume would skip them forever. Distinct
	// rows are hashed concurrently; SetBookFileHash round-trips the full record
	// (no field wipe) and writes one distinct key per row, so parallel writes
	// across different rows do not race.
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
		// The report is the run's deliverable: stop, with the checkpoint unmoved,
		// rather than hash on while rows are being dropped from it.
		if rerr := run.report.flush(); rerr != nil {
			return fmt.Errorf("backfill-file-hashes: report %s: %w", reportPath, rerr)
		}

		// Periodic checkpoint so long runs can resume after restart.
		if opID != "" {
			_ = operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", "scanning", end, len(files))
		}
	}

	// Clear any saved state on clean completion.
	if opID != "" {
		_ = operations.ClearState(store, opID)
	}

	// Save a lightweight operation summary for the UI and activity feed.
	res := run.counts.summary(dryRun, reportPath)
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
func (j *backfillFileHashesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
