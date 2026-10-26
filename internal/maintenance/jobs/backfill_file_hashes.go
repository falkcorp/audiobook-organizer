// file: internal/maintenance/jobs/backfill_file_hashes.go
// version: 2.1.0
// guid: a1000014-0000-0000-0000-000000000014
// last-edited: 2026-10-26

package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return "Compute and store file hashes for book_files missing them. A file that is not on disk is counted as " +
		"not_found (only the first 50 paths are logged) and listed in a per-row TSV report under the library's " +
		".reports directory, instead of being warned about once per file; nothing is written to the database for it. " +
		"When a library root is configured the op refuses to run, a dry run included, if the root is unreadable, " +
		"is not an absolute path, holds nothing but dot-prefixed entries (a share that is probably not mounted), " +
		"or has a .reports directory that cannot be written"
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
//	unverified_outside_root  not found, but nothing vouches for the path's mount: it
//	                         is outside the library root (an import folder, an iTunes
//	                         share that may simply be unmounted) or no root is configured
//	read_errors              hash failed for a reason other than not-found; retried next run
//	write_errors             the hash was computed but could not be saved
//	skipped_no_path          the row has an empty path; nothing to hash
//
// Plain integers: they are only touched by the goroutine that commits a chunk
// (see commitChunk), never by the worker pool.
type hashBackfillCounts struct {
	hashed                int64
	notFound              int64
	unverifiedOutsideRoot int64
	readErrors            int64
	writeErrors           int64
	skippedNoPath         int64
}

// add counts one row in its report bucket. It returns false for a bucket it
// does not know.
func (c *hashBackfillCounts) add(bucket string) bool {
	switch bucket {
	case hbBucketHashed:
		c.hashed++
	case hbBucketNotFound:
		c.notFound++
	case hbBucketOutside:
		c.unverifiedOutsideRoot++
	case hbBucketReadError:
		c.readErrors++
	case hbBucketWriteError:
		c.writeErrors++
	case hbBucketNoPath:
		c.skippedNoPath++
	default:
		return false
	}
	return true
}

// summary is the one-line result stored on the operation log. In a dry run the
// same counts are what an apply WOULD do: no hash is stored (the report and the
// resume checkpoint are still written). note, when set, says the counts do not
// cover the whole run.
func (c *hashBackfillCounts) summary(dryRun bool, reportPath, note string) string {
	prefix := "Backfilled hashes"
	if dryRun {
		prefix = "Dry run, no hashes stored (counts are what an apply would do)"
	}
	s := fmt.Sprintf("%s: hashed=%d not_found=%d unverified_outside_root=%d read_errors=%d write_errors=%d skipped_no_path=%d",
		prefix, c.hashed, c.notFound, c.unverifiedOutsideRoot, c.readErrors, c.writeErrors, c.skippedNoPath)
	if reportPath != "" {
		s += " report=" + reportPath
	}
	if note != "" {
		s += " (" + note + ")"
	}
	return s
}

// Report buckets: one per outcome, and the TSV's first column.
const (
	hbBucketHashed     = "hashed"
	hbBucketNotFound   = "not-found"
	hbBucketOutside    = "not-found-outside-root"
	hbBucketReadError  = "read-error"
	hbBucketWriteError = "write-error"
	hbBucketNoPath     = "no-path"
)

const hbReportHeader = "bucket\tfile_id\tbook_id\tpath\treason\n"

// hbFile is the part of *os.File the report uses. It is an interface only so a
// test can make Close fail.
type hbFile interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Truncate(size int64) error
}

// hbOpenFile opens the report file. A variable so a test can inject a Close
// failure; production always uses os.OpenFile.
var hbOpenFile = func(name string, flag int, perm os.FileMode) (hbFile, error) {
	return os.OpenFile(name, flag, perm)
}

// hashBackfillReport streams the run's per-row outcomes to a TSV (bucket,
// file_id, book_id, path, reason), the way mark-missing-files does, so a person
// can read what is missing before deciding what to do about it. The TSV is also
// the run's ledger: every counted row has exactly one line in it, so a resumed
// run rebuilds its counters from it.
//
// Rows reach the file only in commitChunk, immediately before the chunk's
// checkpoint is saved, so a run that is cancelled or killed mid-chunk leaves no
// row past its checkpoint on disk. Only the goroutine that commits chunks
// writes; workers only read done.
//
// A nil report discards, for a run with no library root to put it under.
type hashBackfillReport struct {
	f      hbFile
	w      *bufio.Writer
	err    error // first write error; later writes are dropped
	closed bool
	// done is the set of file IDs the report already listed when this run opened
	// it (a resume). It is never modified afterwards, so workers may read it.
	done map[string]struct{}
}

var hbTSVClean = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace

// has reports whether the report already lists the row from an earlier part of
// this run.
func (r *hashBackfillReport) has(fileID string) bool {
	if r == nil {
		return false
	}
	_, ok := r.done[fileID]
	return ok
}

// openHashBackfillReport opens path for the run. A fresh run truncates it and
// writes the header. A resumed run reads what the earlier part wrote: it
// returns the set of file IDs already listed (so they are not written or counted
// again) and the counters those lines add up to, drops a trailing line the
// earlier part was cut off in the middle of, and appends after the last
// complete line. prior is false when a resumed run found nothing to carry over.
func openHashBackfillReport(path string, resumed bool) (rep *hashBackfillReport, seeded hashBackfillCounts, prior bool, err error) {
	if !resumed {
		f, oerr := hbOpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
		if oerr != nil {
			return nil, seeded, false, oerr
		}
		rep = &hashBackfillReport{f: f, w: bufio.NewWriterSize(f, 64<<10)}
		_, rep.err = rep.w.WriteString(hbReportHeader)
		return rep, seeded, false, rep.err
	}
	f, oerr := hbOpenFile(path, os.O_RDWR|os.O_CREATE, 0o664)
	if oerr != nil {
		return nil, seeded, false, oerr
	}
	rep = &hashBackfillReport{f: f, w: bufio.NewWriterSize(f, 64<<10), done: map[string]struct{}{}}
	good, total, lerr := rep.loadPrior(&seeded)
	if lerr != nil {
		_ = f.Close()
		return nil, hashBackfillCounts{}, false, lerr
	}
	if good != total {
		// A hard kill can cut the last line short; the rows of that chunk are
		// reprocessed, so the fragment must not stay in the file.
		if terr := f.Truncate(good); terr != nil {
			_ = f.Close()
			return nil, hashBackfillCounts{}, false, terr
		}
	}
	if _, serr := f.Seek(good, io.SeekStart); serr != nil {
		_ = f.Close()
		return nil, hashBackfillCounts{}, false, serr
	}
	if good == 0 {
		_, rep.err = rep.w.WriteString(hbReportHeader)
		return rep, seeded, false, rep.err
	}
	return rep, seeded, true, nil
}

// loadPrior reads the existing report. good is the byte length of its complete
// lines and total the file's length. A complete line that is not a valid row is
// an error: the ledger cannot be trusted, and guessing would put wrong numbers
// in the summary.
func (r *hashBackfillReport) loadPrior(seeded *hashBackfillCounts) (good, total int64, err error) {
	br := bufio.NewReaderSize(r.f, 1<<20)
	for lineNo := 1; ; lineNo++ {
		line, rerr := br.ReadString('\n')
		total += int64(len(line))
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return good, total, nil // a trailing fragment without a newline is not counted in good
			}
			return 0, 0, rerr
		}
		if lineNo == 1 {
			if line != hbReportHeader {
				return 0, 0, fmt.Errorf("existing report has an unexpected header %q", strings.TrimRight(line, "\n"))
			}
			good += int64(len(line))
			continue
		}
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		if len(fields) != 5 {
			return 0, 0, fmt.Errorf("existing report line %d has %d fields, want 5", lineNo, len(fields))
		}
		if _, dup := r.done[fields[1]]; !dup {
			if !seeded.add(fields[0]) {
				return 0, 0, fmt.Errorf("existing report line %d has an unknown bucket %q", lineNo, fields[0])
			}
			r.done[fields[1]] = struct{}{}
		}
		good += int64(len(line))
	}
}

// add buffers one row. Called only from commitChunk.
func (r *hashBackfillReport) add(bucket string, bf database.BookFileCore, reason string) {
	if r == nil || r.err != nil {
		return
	}
	_, r.err = fmt.Fprintf(r.w, "%s\t%s\t%s\t%s\t%s\n", bucket, bf.ID, bf.BookID, hbTSVClean(bf.FilePath), hbTSVClean(reason))
}

// flush pushes buffered rows to disk and returns the first write error.
func (r *hashBackfillReport) flush() error {
	if r == nil {
		return nil
	}
	if r.err == nil {
		r.err = r.w.Flush()
	}
	return r.err
}

// close flushes and closes the file. It is safe to call twice: the second call
// does nothing and returns nil.
func (r *hashBackfillReport) close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	ferr := r.flush()
	cerr := r.f.Close()
	return errors.Join(ferr, cerr)
}

var hbOpIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// hashBackfillReportName is the report's file name under {root}/.reports. It is
// unique to the run: the operation ID when there is one (a resume of the same op
// continues it), otherwise a timestamp, so two runs that have no op ID never
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

// hbOutcome is what a worker decided for one row. It is only a decision: nothing
// is counted, stored or reported until the whole chunk has been processed and
// commitChunk runs, so a chunk that is cancelled half way changes nothing.
type hbOutcome struct {
	bucket string // "" = not counted: the row already had a hash, or an earlier part of this run reported it
	reason string
	hash   string // the computed hash, for a hashed row
	bf     database.BookFileCore
}

// hashBackfillRun is one Run's state, shared by the worker pool.
type hashBackfillRun struct {
	store    maintenance.JobStore
	reporter maintenance.ProgressReporter
	report   *hashBackfillReport
	counts   hashBackfillCounts // only touched by the chunk-committing goroutine
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

// unverifiable reports whether a not-found for path says nothing about the
// library root's mount: the path is outside the root (an import folder or an
// iTunes share may simply be unmounted), or no root is configured so no mount
// was checked at all. Those are counted apart from not_found.
func (r *hashBackfillRun) unverifiable(path string) bool {
	return r.root == "" || !pathutil.IsWithin(filepath.Clean(path), r.root)
}

// processRow decides one row on a worker. It hashes the file but stores
// nothing: see hbOutcome.
func (r *hashBackfillRun) processRow(bf database.BookFileCore) hbOutcome {
	r.mu.Lock()
	r.reporter.Increment()
	r.mu.Unlock()

	if bf.FileHash != "" || r.report.has(bf.ID) {
		return hbOutcome{}
	}
	if strings.TrimSpace(bf.FilePath) == "" {
		return hbOutcome{bucket: hbBucketNoPath, reason: "row has no file path", bf: bf}
	}
	// filehash.BookFileHash directly: scanner.ComputeFileHash is only a thin
	// wrapper over it, and filehash is the single entry point for the digest.
	hash, herr := filehash.BookFileHash(bf.FilePath)
	if herr != nil {
		msg := herr.Error()
		if fileAbsent(herr) {
			return r.onNotFound(bf, msg)
		}
		slog.Warn("backfill-file-hashes hash failed", "file", bf.FilePath, "details", msg)
		r.log("warn", "Hash computation failed for "+bf.FilePath, &msg)
		return hbOutcome{bucket: hbBucketReadError, reason: msg, bf: bf} // one unreadable file must not abort the run
	}
	reason := "hash computed"
	if r.dryRun {
		reason = "hash computed, not stored (dry run)"
	}
	return hbOutcome{bucket: hbBucketHashed, reason: reason, hash: hash, bf: bf}
}

// onNotFound classifies a hash attempt that found the file absent. Nothing is
// written to the database for it: the row stays as it is, and the TSV report
// lists it. Only the first hashBackfillNotFoundLogCap are logged.
func (r *hashBackfillRun) onNotFound(bf database.BookFileCore, msg string) hbOutcome {
	bucket, reason := hbBucketNotFound, "file not found"
	if r.unverifiable(bf.FilePath) {
		bucket = hbBucketOutside
		if r.root == "" {
			reason = "file not found, and no library root is configured, so its mount is unverified"
		} else {
			reason = "file not found outside the library root, so its mount is unverified"
		}
	}
	if bf.Missing {
		reason += " (row already flagged missing)"
	}
	if r.notFoundLogged.Add(1) <= hashBackfillNotFoundLogCap {
		r.log("warn", "File not found: "+bf.FilePath, &msg)
	}
	return hbOutcome{bucket: bucket, reason: reason, bf: bf}
}

// commitChunk makes a fully processed chunk real, in this order: store the
// hashes (unless dry-run), count every outcome, write the chunk's rows to the
// report and flush them. The caller saves the checkpoint right after, so a row
// is on disk only for a chunk whose rows are complete, and a chunk cancelled
// before this point has stored, counted and written nothing.
//
// The hash writes touch one distinct key per row, so they run on a bounded
// pool; a failed write turns that row's outcome into a write error.
func (r *hashBackfillRun) commitChunk(outs []hbOutcome) error {
	if !r.dryRun {
		var g errgroup.Group
		g.SetLimit(r.workers)
		for i := range outs {
			if outs[i].bucket != hbBucketHashed {
				continue
			}
			g.Go(func() error { // each goroutine owns outs[i]
				o := &outs[i]
				if serr := r.store.SetBookFileHash(o.bf.ID, o.hash); serr != nil {
					msg := serr.Error()
					o.bucket, o.reason = hbBucketWriteError, msg
					slog.Error("backfill-file-hashes SetBookFileHash failed", "details", msg)
					r.log("error", "Failed to save file hash for "+o.bf.FilePath, &msg)
				}
				return nil
			})
		}
		_ = g.Wait() // the closures never return an error
	}
	for _, o := range outs {
		if o.bucket == "" {
			continue
		}
		r.counts.add(o.bucket)
		r.report.add(o.bucket, o.bf, o.reason)
	}
	return r.report.flush()
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
	partialNote := ""
	if root := strings.TrimSpace(config.AppConfig.RootDir); root != "" {
		// Refusals, all before anything is hashed, a dry run included. The root
		// must be readable and hold something other than dot-prefixed entries (an
		// unmounted share leaves an empty directory, so every path under it
		// answers not-found and the whole library would be reported as gone);
		// that is a coarse check (libroot.Mounted), not proof of a healthy mount.
		// It must be an absolute path, and {root}/.reports must be writable,
		// because the report is this run's deliverable. The mount check runs
		// BEFORE the report is resolved because that creates {root}/.reports.
		if merr := libroot.Mounted(root); merr != nil {
			return fmt.Errorf("backfill-file-hashes: refusing to run: %w", merr)
		}
		run.root = filepath.Clean(root)
		path, rerr := libroot.ReportPath(root, "", hashBackfillReportName(opID, time.Now()))
		if rerr != nil {
			return fmt.Errorf("backfill-file-hashes: refusing to run: %w", rerr)
		}
		report, seeded, prior, oerr := openHashBackfillReport(path, resumeIndex > 0)
		if oerr != nil {
			return fmt.Errorf("backfill-file-hashes: open report %s: %w", path, oerr)
		}
		run.report = report
		run.counts = seeded
		reportPath = path
		run.log("info", "Per-row report: "+path, nil)
		switch {
		case resumeIndex > 0 && prior:
			run.log("info", fmt.Sprintf("Resumed at row %d; the report already accounts for %d rows", resumeIndex, len(report.done)), nil)
		case resumeIndex > 0:
			partialNote = fmt.Sprintf("resumed at row %d with no earlier report to carry over: counts cover only the rows from there on", resumeIndex)
		}
		defer func() {
			if cerr := report.close(); cerr != nil && retErr == nil {
				retErr = fmt.Errorf("backfill-file-hashes: report %s: %w", path, cerr)
			}
		}()
	} else if resumeIndex > 0 {
		partialNote = fmt.Sprintf("resumed at row %d and no library root is configured, so there is no report to carry the earlier counts: counts cover only the rows from there on", resumeIndex)
	}

	// Process in ORDERED chunks. Each chunk runs through a bounded worker pool
	// that only DECIDES each row (hash, classify); nothing is stored, counted or
	// reported until the whole chunk is done, then commitChunk does all three and
	// the checkpoint is written right after. That preserves the invariant the old
	// serial loop had for free -- every index below the checkpoint has been
	// processed -- which a naive fan-out would break: a run killed mid-flight
	// could otherwise checkpoint an index past rows still being hashed, and
	// resume would skip them forever. It also means a run cancelled mid-chunk
	// leaves no report row, no counter and no stored hash behind for that chunk,
	// so resume redoes the whole chunk exactly once.
	for start := resumeIndex; start < len(files); start += hashBackfillChunkSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		end := min(start+hashBackfillChunkSize, len(files))
		outs := make([]hbOutcome, end-start)

		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(run.workers)
		for i := start; i < end; i++ {
			g.Go(func() error {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				outs[i-start] = run.processRow(files[i])
				return nil
			})
		}
		// Per-file failures are folded into the outcomes, so a non-nil result here
		// means the context was cancelled. Return it WITHOUT committing: the
		// checkpoint from the last fully-completed chunk stands, and resume
		// restarts at that boundary.
		if gerr := g.Wait(); gerr != nil {
			return gerr
		}
		// The report is the run's deliverable: stop, with the checkpoint unmoved,
		// rather than hash on while rows are being dropped from it.
		if rerr := run.commitChunk(outs); rerr != nil {
			return fmt.Errorf("backfill-file-hashes: report %s: %w", reportPath, rerr)
		}

		// Periodic checkpoint so long runs can resume after restart.
		if opID != "" {
			_ = operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", "scanning", end, len(files))
		}
	}

	// Close the report BEFORE declaring the run done. Every chunk is already
	// flushed, so only the file close itself can fail here; if it does, the run
	// fails with its checkpoint kept and no "completed" summary is saved, so the
	// status and the summary cannot disagree.
	if cerr := run.report.close(); cerr != nil {
		return fmt.Errorf("backfill-file-hashes: report %s: %w", reportPath, cerr)
	}

	// Clear any saved state on clean completion.
	if opID != "" {
		_ = operations.ClearState(store, opID)
	}

	// Save a lightweight operation summary for the UI and activity feed.
	res := run.counts.summary(dryRun, reportPath, partialNote)
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
