// file: internal/maintenance/jobs/backfill_file_hashes.go
// version: 4.4.0
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
	"sort"
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
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func init() { maintenance.Register(&backfillFileHashesJob{}) }

// hbLog is the job's logger. The operation-state diagnostics go through it, not
// log/slog, because the slog guard (internal/logger) ratchets direct slog calls
// down and does not allow new ones.
var hbLog = logger.New("backfill-file-hashes")

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
	return "Compute and store file hashes for book_files missing them. A file that is not on disk is counted " +
		"(not_found, or unverified_outside_root when it is outside the library root or no root is configured; " +
		"only the first 50 paths are logged) and listed in a per-row TSV report under the library's .reports " +
		"directory, instead of being warned about once per file; nothing is written to the database for it. " +
		"A hash is stored only if the row still has none and still points at the file that was hashed; a row " +
		"that changed meanwhile is counted as busy and retried next run. " +
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
// reaches it lands in exactly one of these seven, and a row that has one is not
// counted at all:
//
//	hashed                   hash computed (and, unless dry-run, stored)
//	not_found                the hash attempt found the file absent (ENOENT/ENOTDIR)
//	unverified_outside_root  not found, but nothing vouches for the path's mount: it
//	                         is outside the library root (an import folder, an iTunes
//	                         share that may simply be unmounted) or no root is configured
//	read_errors              hash failed for a reason other than not-found; retried next run
//	write_errors             the hash was computed but could not be saved
//	busy                     the hash was computed but the row changed meanwhile (another
//	                         writer, or its path/size/mtime moved); nothing stored, retried next run
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
	busy                  int64
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
	case hbBucketBusy:
		c.busy++
	case hbBucketNoPath:
		c.skippedNoPath++
	default:
		return false
	}
	return true
}

// valid reports whether bucket is one add accepts.
func (c *hashBackfillCounts) valid(bucket string) bool {
	var scratch hashBackfillCounts
	return scratch.add(bucket)
}

// summary is the one-line result stored on the operation log. In a dry run the
// same counts are what an apply WOULD do: no hash is stored (the report is still
// written). note, when set, says the counts do not cover the whole run.
func (c *hashBackfillCounts) summary(dryRun bool, reportPath, note string) string {
	prefix := "Backfilled hashes"
	if dryRun {
		prefix = "Dry run, no hashes stored (counts are what an apply would do)"
	}
	s := fmt.Sprintf("%s: hashed=%d not_found=%d unverified_outside_root=%d read_errors=%d write_errors=%d busy=%d skipped_no_path=%d",
		prefix, c.hashed, c.notFound, c.unverifiedOutsideRoot, c.readErrors, c.writeErrors, c.busy, c.skippedNoPath)
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
	hbBucketBusy       = "busy"
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
	Sync() error
	Stat() (os.FileInfo, error)
}

// hbReportPerm is the mode a new report file is created with: util.FileMode,
// the repo's standard (group write kept, because on a POSIX-ACL filesystem the
// group bits are the ACL mask and dropping group write would silently strip the
// write access the operator granted). Never world-writable. The report is
// group-writable by design (D43), so a user in the share's group, or on its ACL,
// can edit a live run's report. That can make a resume skip rows and inflate its
// counts, and can never cause a hash write (see the trust note on loadPrior). The
// checks on the open descriptor (hbCheckReportFile) turn away files other
// accounts made, not the share's group or ACL users.
const hbReportPerm = util.FileMode

// hbMaxReportBytes caps the size of a report that is read back on resume, and
// hbMaxReportLine the length of one of its lines. The expected report is about
// 110 MB for the whole library (about 515k rows of a few hundred bytes), so the
// size cap is roughly 4x that. A larger file, or a line that long, is not a
// report this job wrote: the resume fails closed instead of reading it. Variables
// so a test can lower them.
var (
	hbMaxReportBytes int64 = 512 << 20
	hbMaxReportLine        = 64 << 10
)

// hbCheckReportFile vets the opened report file, using the open descriptor (so
// the answer cannot be swapped between check and use): it must be a regular
// file, owned by the user this process runs as, and no larger than
// hbMaxReportBytes. The report is inside the library share, so a file another
// account made (a different owner, a symlink, a device) is not trusted. Users in
// the share's group or on its ACL can write a report this process made; that is
// accepted (D43), because a report only ever decides what a resume skips.
func hbCheckReportFile(f hbFile) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	switch {
	case !info.Mode().IsRegular():
		return errors.New("report is not a regular file")
	case !libroot.OwnedByCurrentUser(info):
		return errors.New("report is not owned by the user this process runs as; refusing to trust it")
	case info.Size() > hbMaxReportBytes:
		return fmt.Errorf("report is %d bytes, over the %d byte limit", info.Size(), hbMaxReportBytes)
	}
	return nil
}

// hbOpenFile opens the report file. A variable so a test can inject a Close or
// Write failure; production always uses os.OpenFile. Callers pass
// syscall.O_NOFOLLOW, so a symlink planted at the report's path is refused
// instead of followed.
var hbOpenFile = func(name string, flag int, perm os.FileMode) (hbFile, error) {
	return os.OpenFile(name, flag, perm)
}

// hashBackfillReport streams the run's per-row outcomes to a TSV (bucket,
// file_id, book_id, path, reason), the way mark-missing-files does, so a person
// can read what is missing before deciding what to do about it. The TSV is also
// the run's ledger: every counted row has exactly one line in it, so a resumed
// run rebuilds its counters from it and knows which rows it need not redo.
//
// The path and reason columns are LOSSY: a tab, CR or LF inside a path becomes a
// space (hbTSVClean), so such a path cannot be found again from the report. File
// and book IDs are written as they are and are never ambiguous (they carry no
// such characters). The chunk journal is JSON and exact.
//
// A chunk's rows are written only when the whole chunk has been processed,
// together with its hash stores (commitChunk), so a cancelled chunk leaves no
// row at all. A process killed inside that step can leave some or all of the
// chunk's rows on disk; resume dedupes them by file ID and the chunk journal
// finishes whatever the kill cut short. Only the goroutine that commits chunks
// writes; workers only read done.
//
// A nil report discards, for a run with no library root to put it under.
type hashBackfillReport struct {
	f      hbFile
	w      *bufio.Writer
	err    error // first write error; later writes are dropped
	closed bool
	// done is the set of file IDs the report listed when this run opened it plus
	// those replayJournal recovered. It is only modified before the worker pool
	// starts, so workers may read it.
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

// openHashBackfillReport opens path for the run.
//
// A run with an operation ID (existing = true) NEVER truncates: a report named
// after an operation ID can only be an earlier attempt of the same operation
// (IDs are unique per operation), and its rows may be the only record of hashes
// that attempt already stored. It reads that report: the set of file IDs already
// listed (so they are not written or counted again) and the counters those lines
// add up to. A trailing line the earlier attempt was cut off in the middle of is
// dropped, unless it is a whole valid row that only lacks its newline, which is
// kept. New rows are appended. prior is false when there was nothing to carry
// over. A run with no operation ID has a timestamped name nobody else uses and
// starts the file fresh.
func openHashBackfillReport(path string, existing bool) (rep *hashBackfillReport, seeded hashBackfillCounts, prior bool, err error) {
	if !existing {
		// No O_TRUNC at open: the file is vetted first (a file somebody else made is
		// refused, not emptied), then truncated.
		f, oerr := hbOpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, hbReportPerm)
		if oerr != nil {
			return nil, seeded, false, oerr
		}
		if cerr := hbCheckReportFile(f); cerr != nil {
			_ = f.Close()
			return nil, seeded, false, cerr
		}
		if terr := f.Truncate(0); terr != nil {
			_ = f.Close()
			return nil, seeded, false, terr
		}
		rep = &hashBackfillReport{f: f, w: bufio.NewWriterSize(f, 64<<10)}
		_, rep.err = rep.w.WriteString(hbReportHeader)
		return rep, seeded, false, rep.err
	}
	f, oerr := hbOpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, hbReportPerm)
	if oerr != nil {
		return nil, seeded, false, oerr
	}
	if cerr := hbCheckReportFile(f); cerr != nil {
		_ = f.Close()
		return nil, seeded, false, cerr
	}
	rep = &hashBackfillReport{f: f, w: bufio.NewWriterSize(f, 64<<10), done: map[string]struct{}{}}
	good, total, unterminated, lerr := rep.loadPrior(&seeded)
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
	if unterminated {
		_, rep.err = rep.w.WriteString("\n") // terminate the last row so appended rows start on a new line
	}
	return rep, seeded, true, rep.err
}

// hbParseRow splits one report line (without its newline) into its five fields,
// tolerating a CRLF ending.
func hbParseRow(line string) []string {
	return strings.Split(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), "\t")
}

// loadPrior reads the existing report. good is the byte length of the lines
// kept and total the file's length. A complete line that is not a valid row is
// an error: the ledger cannot be trusted, and guessing would put wrong numbers
// in the summary. unterminated reports that the last kept line had no newline.
//
// TRUST. The report lives under {root}/.reports, inside the library share, and
// its files are group-writable by design (D43), so whoever is in the share's
// group or on its ACL can write it. Its content is therefore treated as
// untrusted input: it only ever decides which rows to SKIP and what to COUNT.
// Nothing read from it is ever stored, hashed or used as a path; the only things
// taken from a line are its bucket (an enum) and its file ID, and both are
// validated here (hbValidID). What a resume STORES comes from the file on disk
// or from the chunk journal in the database, never from this file.
func (r *hashBackfillReport) loadPrior(seeded *hashBackfillCounts) (good, total int64, unterminated bool, err error) {
	// A bounded reader: ReadSlice fails with ErrBufferFull on a line longer than
	// the buffer, so a line past hbMaxReportLine is refused instead of growing
	// without limit.
	br := bufio.NewReaderSize(r.f, hbMaxReportLine)
	for lineNo := 1; ; lineNo++ {
		raw, rerr := br.ReadSlice('\n')
		if errors.Is(rerr, bufio.ErrBufferFull) {
			return 0, 0, false, fmt.Errorf("existing report line %d is longer than %d bytes", lineNo, hbMaxReportLine)
		}
		line := string(raw)
		total += int64(len(line))
		atEOF := false
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return 0, 0, false, rerr
			}
			if line == "" {
				return good, total, unterminated, nil
			}
			atEOF = true // a final line without a newline
		}
		if lineNo == 1 {
			if atEOF {
				return 0, total, false, nil // a header cut short: start the file over
			}
			if strings.TrimRight(line, "\r\n") != strings.TrimRight(hbReportHeader, "\n") {
				return 0, 0, false, fmt.Errorf("existing report has an unexpected header %q", strings.TrimRight(line, "\r\n"))
			}
			good += int64(len(line))
			continue
		}
		fields := hbParseRow(line)
		if len(fields) != 5 || !hbValidID(fields[1]) || (fields[2] != "" && !hbValidID(fields[2])) {
			if atEOF {
				return good, total, unterminated, nil // a fragment: dropped, its rows are redone
			}
			if len(fields) != 5 {
				return 0, 0, false, fmt.Errorf("existing report line %d has %d fields, want 5", lineNo, len(fields))
			}
			return 0, 0, false, fmt.Errorf("existing report line %d has an invalid file or book ID", lineNo)
		}
		// The bucket is checked on every line, a repeated file ID included: a
		// duplicate with a garbage bucket must not be accepted and kept.
		if !seeded.valid(fields[0]) {
			if atEOF {
				return good, total, unterminated, nil
			}
			return 0, 0, false, fmt.Errorf("existing report line %d has an unknown bucket %q", lineNo, fields[0])
		}
		if _, dup := r.done[fields[1]]; !dup {
			seeded.add(fields[0])
			// Clone: fields[1] is a substring that would pin its whole line, so a
			// 515k-row ledger would hold every line's bytes, not just the IDs.
			r.done[strings.Clone(fields[1])] = struct{}{}
		}
		good += int64(len(line))
		unterminated = atEOF
	}
}

// hbIDPattern is what a file or book ID in a report or journal may look like:
// 1 to 64 characters of letters, digits, underscore and hyphen. IDs are ULIDs
// when the store mints them, but the store also accepts IDs a caller supplies, so
// the check is on the character set rather than on ULID shape: a stricter one
// would refuse to resume a report that lists a legitimate non-ULID row.
var hbIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func hbValidID(id string) bool { return hbIDPattern.MatchString(id) }

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
	// written is set by storeHash when its conditional store wrote the row (or
	// failed in a way that may have written it), so the row's book needs an
	// aggregate recompute. A row that already had a hash, or that changed
	// meanwhile, wrote nothing and is not marked.
	written bool
}

// hashBackfillRun is one Run's state, shared by the worker pool.
type hashBackfillRun struct {
	store    maintenance.JobStore
	reporter maintenance.ProgressReporter
	report   *hashBackfillReport
	opID     string
	counts   hashBackfillCounts // only touched by the chunk-committing goroutine
	dryRun   bool
	root     string // cleaned library root; "" when none is configured
	workers  int
	// journalID is the operation-state key the chunk journal lives under, "" when
	// the run keeps none (no operation ID to key it by, or no report to finish).
	journalID string

	// mu serializes reporter: maintenance.ProgressAdapter increments a plain int
	// and is NOT concurrency-safe. The guarded calls are microseconds; hashing
	// dominates, so this does not meaningfully serialize the pool.
	mu sync.Mutex

	notFoundLogged atomic.Int64

	// failedBooks maps a book whose aggregate recompute failed to the failure
	// text. Its hashes are stored; the run does not stop for it (D45) and the
	// summary names these books. Only touched by the chunk-committing goroutine.
	failedBooks map[string]string
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

// hbHasNotFound reports whether any outcome is an under-root not-found, the
// only outcome a dropped mount can forge.
func hbHasNotFound(outs []hbOutcome) bool {
	for _, o := range outs {
		if o.bucket == hbBucketNotFound {
			return true
		}
	}
	return false
}

// hbCommitHook, when set, is called at fixed points inside commitChunk
// ("after-journal", "after-store", "after-flush", "after-commit") with the stage name and the chunk's first row index; a non-nil error stops the commit
// there, leaving on disk exactly what a process kill at that point would leave.
// A test seam only; production never sets it.
var hbCommitHook func(stage string, chunkStart int) error

func hbHook(stage string, chunkStart int) error {
	if hbCommitHook == nil {
		return nil
	}
	return hbCommitHook(stage, chunkStart)
}

// hbJournalEntry is one outcome as it is kept in the chunk journal.
type hbJournalEntry struct {
	Bucket string `json:"bucket"`
	Reason string `json:"reason"`
	Hash   string `json:"hash,omitempty"`
	FileID string `json:"file_id"`
	BookID string `json:"book_id"`
	Path   string `json:"path"`
	// Size and Mtime are the row's file_size and last_scan_mtime as the run read
	// them, so a replay can tell whether the row changed since the file was hashed.
	Size  int64  `json:"size,omitempty"`
	Mtime *int64 `json:"mtime,omitempty"`
}

// hbJournal is the whole chunk journal as it is stored.
type hbJournal struct {
	Version int              `json:"version"`
	OpID    string           `json:"op_id"`
	Entries []hbJournalEntry `json:"entries"`
}

const hbJournalVersion = 1

// hbJournalStateID is the operation-state key a run's chunk journal is kept
// under: the blob lives at opstate:<opID>:params. That key shape is the one the
// store ties to the operation's life: DeleteOperationState(opID) (which
// operations.ClearState calls) removes it with the checkpoint, and the
// retention job's opstate sweep keeps it while the operation can still resume
// and drops it once the operation is terminal or gone, so a journal an
// operation never came back for cannot outlive it. (An id that did not end in
// ":params" would look like an unknown operation to that sweep and be deleted
// under a live run.) Nothing else reads or writes this key on the maintenance
// path.
func hbJournalStateID(opID string) string { return opID + ":params" }

// writeJournal durably records the chunk's hashed outcomes BEFORE any hash is
// stored, in the operation's own state in the database (SaveOperationState syncs
// the write), so a record either exists whole or not at all. It exists for one
// reason: storing the hashes and writing the report rows are two steps a kill can
// fall between, and a row whose hash is already stored is skipped on the next
// run, so without a record of what the chunk decided that row would be neither
// counted nor listed. The journal is NOT a file: the directory a report lives in
// is inside the library share and writable by others, and the journal's entries
// are hash writes, so it must not be somewhere a forged entry could be planted.
// It is removed once the rows are on disk in the report; if one is found on
// resume, replayJournal finishes the job.
func (r *hashBackfillRun) writeJournal(outs []hbOutcome) (bool, error) {
	if r.journalID == "" || r.dryRun {
		return false, nil // a dry run stores nothing, so it has nothing to protect
	}
	j := hbJournal{Version: hbJournalVersion, OpID: r.opID}
	for _, o := range outs {
		// Only a hash about to be stored needs a record. Every other outcome
		// (not-found, read error, no path) stores nothing, so after a kill it is
		// in neither the journal nor the report, and the resume simply decides it
		// again and counts it once. Journaling them would write about 515k
		// entries per run, synced, for nothing.
		if o.bucket != hbBucketHashed {
			continue
		}
		j.Entries = append(j.Entries, hbJournalEntry{Bucket: o.bucket, Reason: o.reason, Hash: o.hash,
			FileID: o.bf.ID, BookID: o.bf.BookID, Path: o.bf.FilePath, Size: o.bf.FileSize, Mtime: o.bf.LastScanMtime})
	}
	if len(j.Entries) == 0 {
		return false, nil
	}
	data, err := json.Marshal(j)
	if err != nil {
		return false, err
	}
	if err := r.store.SaveOperationState(r.journalID, data); err != nil {
		return false, err
	}
	return true, nil
}

// removeJournal deletes the journal once its rows are durably in the report.
func (r *hashBackfillRun) removeJournal() error {
	return r.store.DeleteOperationState(r.journalID)
}

// hbBusyReason and hbChangedReason are the report reasons of a busy row.
const (
	hbBusyReason    = "row was busy, retried next run"
	hbChangedReason = "row changed since the file was hashed, retried next run"
)

// hbSameRow reports whether the stored row still describes the file that was
// hashed: same path, size and scan mtime as when the run read it.
func hbSameRow(row *database.BookFile, bf database.BookFileCore) bool {
	if row.FilePath != bf.FilePath || row.FileSize != bf.FileSize {
		return false
	}
	if (row.LastScanMtime == nil) != (bf.LastScanMtime == nil) {
		return false
	}
	return row.LastScanMtime == nil || *row.LastScanMtime == *bf.LastScanMtime
}

// storeHash stores one outcome's hash CONDITIONALLY, under the row's lock
// (ModifyBookFileDeferAggregates): only if the row still has no hash and still
// points at the file that was hashed. The hash was computed from the file at
// bf.FilePath some time ago (up to a whole chunk of reads, or a process restart
// for a replayed journal); a row repointed to a twin or a re-encoded copy since
// then must not receive the old file's hash, because SetBookFileHash-style writes
// also make it the row's permanent original_file_hash. A row that changed is not
// an error: it is counted as busy and retried next run. A row that already has a
// hash keeps it and counts as hashed.
//
// When the run has a journal, the store call is the variant that does NOT
// recompute the book's aggregates (a run with no journal uses ModifyBookFile, see
// below): that would decode every file row of the book once per stored hash (a
// 400-chapter book: 400 recomputes, 160k row decodes). The hash does feed the
// stored aggregate (a missing row whose hash matches a present row is absorbed
// into it, book_runtime.go), so the book IS recomputed, once per chunk, by
// recomputeBooks. An outcome that may have written the row is marked written for
// that.
//
// TODO(#4169): once PR #4169 lands, the store runs this callback under
// compare-and-swap and returns database.ErrBookFileConflict when other writers
// keep changing the row; count that as busy here. (database.ErrBookFileChangedSince,
// the generic changed-since error main has, comes only from PatchBookFileFields:
// ModifyBookFile* never returns it, so it is not matched.)
func (r *hashBackfillRun) storeHash(o *hbOutcome) {
	var already, changed bool
	apply := func(row *database.BookFile) error {
		switch {
		case row.FileHash != "":
			already = true
			return database.ErrSkipBookFileWrite
		case !hbSameRow(row, o.bf):
			changed = true
			return database.ErrSkipBookFileWrite
		}
		row.FileHash = o.hash
		if row.OriginalFileHash == "" {
			// The digest file_hash always holds (filehash.BookFileHash), as
			// SetBookFileHash records it.
			row.OriginalFileHash = o.hash
			row.OriginalFileHashKind = database.FileHashKindSampled
		}
		return nil
	}
	// With a journal the aggregates are recomputed once per book per chunk
	// (recomputeBooks), after the stores. Without one (no operation ID, or no
	// library root) a kill between the stores and that recompute would leave the
	// stored hashes behind stale aggregates and nothing would ever revisit them
	// (a later run skips rows that have a hash), so each row is stored through
	// ModifyBookFile, which recomputes its book as part of the write.
	modify := r.store.ModifyBookFileDeferAggregates
	deferred := r.journalID != ""
	if !deferred {
		modify = r.store.ModifyBookFile
	}
	row, err := modify(o.bf.BookID, o.bf.ID, apply)
	switch {
	case err != nil:
		// A failed write may still have been applied (an fsync that failed after
		// the commit), so its book is recomputed too.
		o.written = deferred && !already && !changed
		msg := err.Error()
		o.bucket, o.reason = hbBucketWriteError, msg
		slog.Error("backfill-file-hashes store hash failed", "details", msg)
		r.log("error", "Failed to save file hash for "+o.bf.FilePath, &msg)
	case row == nil || changed:
		o.bucket, o.reason = hbBucketBusy, hbChangedReason
		msg := "row " + o.bf.ID + " no longer matches the file that was hashed"
		r.log("warn", "Row changed, will retry next run: "+o.bf.FilePath, &msg)
	case already:
		o.reason = "hash already stored"
	default:
		o.written = deferred
	}
}

// storeHashes stores the hash of every hashed outcome (see storeHash). The
// writes touch one distinct row each, so they run on a bounded pool. Does
// nothing in a dry run.
func (r *hashBackfillRun) storeHashes(outs []hbOutcome) {
	if r.dryRun {
		return
	}
	var g errgroup.Group
	g.SetLimit(r.workers)
	for i := range outs {
		if outs[i].bucket != hbBucketHashed {
			continue
		}
		g.Go(func() error { // each goroutine owns outs[i]
			r.storeHash(&outs[i])
			return nil
		})
	}
	_ = g.Wait() // the closures never return an error
}

// recomputeBooks recomputes the stored aggregates of every distinct book that
// had a hash written, ONCE per book however many of its rows were written (see
// storeHash). all selects every book with a stored hash instead, whether or not
// this call wrote it: a replayed journal cannot know whether the dead run got as
// far as this step for a row it finds already stored, so it recomputes them all.
// Books are independent, so they run on a bounded pool. A recompute that fails
// does not stop the run (D45): the book's hashes are stored and the aggregate is
// derived data, so the failures are returned (book ID to error text), sorted by
// the caller, and noteRecomputeFailures logs them once and keeps them for the
// summary. The chunk's journal is cleared as normal, so a failed book is not
// retried by a resume; the summary names it so it can be repaired. Does nothing in
// a dry run.
func (r *hashBackfillRun) recomputeBooks(outs []hbOutcome, all bool) map[string]string {
	if r.dryRun {
		return nil
	}
	seen := map[string]struct{}{}
	var books []string
	for _, o := range outs {
		if all {
			if o.bucket != hbBucketHashed && o.bucket != hbBucketWriteError {
				continue
			}
		} else if !o.written {
			continue
		}
		if _, dup := seen[o.bf.BookID]; dup {
			continue
		}
		seen[o.bf.BookID] = struct{}{}
		books = append(books, o.bf.BookID)
	}
	var g errgroup.Group
	g.SetLimit(r.workers)
	var mu sync.Mutex
	var failed map[string]string
	for _, id := range books {
		g.Go(func() error {
			if err := r.store.RecomputeBookAggregates(id); err != nil {
				mu.Lock()
				if failed == nil {
					failed = map[string]string{}
				}
				failed[id] = err.Error()
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait() // the closures never return an error
	return failed
}

// hbMaxFailedBooksListed caps how many failed book IDs the summary and the log
// line spell out; the rest are counted.
const hbMaxFailedBooksListed = 50

// noteRecomputeFailures keeps the books whose recompute failed for the summary
// and logs them as ONE error line, however many failed (a persistently failing
// book is hit again by every later chunk that writes one of its rows, so a line
// per failure would repeat it).
func (r *hashBackfillRun) noteRecomputeFailures(failed map[string]string) {
	if len(failed) == 0 {
		return
	}
	if r.failedBooks == nil {
		r.failedBooks = map[string]string{}
	}
	ids := make([]string, 0, len(failed))
	for id, msg := range failed {
		ids = append(ids, id)
		r.failedBooks[id] = msg
	}
	sort.Strings(ids)
	first := failed[ids[0]]
	hbLog.Error("could not recompute the aggregates of %d books after storing their hashes, the hashes are kept, books=%s first_error=%s",
		len(ids), logger.SanitizeLogValue(hbListIDs(ids)), logger.SanitizeLogValue(first))
	msg := first
	r.log("error", fmt.Sprintf("Could not recompute the aggregates of %d books after storing their hashes: %s", len(ids), hbListIDs(ids)), &msg)
}

// hbListIDs joins ids for a message, spelling out at most hbMaxFailedBooksListed.
func hbListIDs(ids []string) string {
	if len(ids) <= hbMaxFailedBooksListed {
		return strings.Join(ids, ",")
	}
	return strings.Join(ids[:hbMaxFailedBooksListed], ",") + fmt.Sprintf(" and %d more", len(ids)-hbMaxFailedBooksListed)
}

// failedBooksNote is the summary's warning for the books whose aggregate
// recompute failed, "" when none did.
func (r *hashBackfillRun) failedBooksNote() string {
	if len(r.failedBooks) == 0 {
		return ""
	}
	ids := make([]string, 0, len(r.failedBooks))
	for id := range r.failedBooks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Sprintf("WARNING: the aggregates of %d books could not be recomputed after their hashes were stored, so they may be stale until the next edit or recompute: books %s",
		len(ids), hbListIDs(ids))
}

// recordOutcomes counts every outcome and writes its row to the report, then
// flushes and fsyncs the report, so the rows are on disk when it returns.
func (r *hashBackfillRun) recordOutcomes(outs []hbOutcome) error {
	for _, o := range outs {
		if o.bucket == "" {
			continue
		}
		r.counts.add(o.bucket)
		r.report.add(o.bucket, o.bf, o.reason)
	}
	if err := r.report.flush(); err != nil {
		return err
	}
	if r.report != nil {
		if err := r.report.f.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// commitChunk makes a fully processed chunk real, in this order: journal the
// chunk's hashed outcomes (durably), store the hashes (unless dry-run), recompute
// each affected book's aggregates once, count every outcome and write the chunk's
// rows to the report, flush and fsync them, then remove the journal. The caller saves the checkpoint right after. So a chunk
// cancelled before this point has stored, counted and written nothing, and a
// process killed anywhere inside it leaves either nothing (before the journal
// exists) or a complete journal that the next run replays, so no hash is ever
// stored without its row being counted and listed exactly once.
func (r *hashBackfillRun) commitChunk(chunkStart int, outs []hbOutcome) error {
	counted := false
	for _, o := range outs {
		if o.bucket != "" {
			counted = true
			break
		}
	}
	if !counted {
		return nil // every row was skipped (already hashed or already reported): nothing to make durable
	}
	journaled, err := r.writeJournal(outs)
	if err != nil {
		return fmt.Errorf("chunk journal: %w", err)
	}
	if err := hbHook("after-journal", chunkStart); err != nil {
		return err
	}
	r.storeHashes(outs)
	if err := hbHook("after-store", chunkStart); err != nil {
		return err
	}
	// Once per book, after every hash of the chunk is stored and BEFORE the rows
	// are recorded. A failure does not stop the run (D45): the hashes are kept and
	// the failed books are named in the summary.
	r.noteRecomputeFailures(r.recomputeBooks(outs, false))
	if err := r.recordOutcomes(outs); err != nil {
		return err
	}
	if err := hbHook("after-flush", chunkStart); err != nil {
		return err
	}
	if journaled {
		if err := r.removeJournal(); err != nil {
			return err
		}
	}
	return hbHook("after-commit", chunkStart)
}

// replayJournal finishes a chunk whose commit was cut off: it reads the journal
// the dead run left in the operation's state, drops the rows the report already
// lists, stores the journaled hash of each remaining hashed row (conditionally,
// see storeHash: the file is not read again, and a row that already has a hash or
// no longer matches is left alone), recomputes the aggregates of their books,
// counts and lists them, and removes the journal. It returns how many rows it recovered. No journal is not an error. A
// journal that does not parse, belongs to another operation, or holds an invalid
// entry is an error: it is the only source of the hashes this stores.
func (r *hashBackfillRun) replayJournal() (int, error) {
	if r.journalID == "" {
		return 0, nil
	}
	data, err := r.store.GetOperationState(r.journalID)
	if err != nil {
		return 0, fmt.Errorf("chunk journal %s: %w", r.journalID, err)
	}
	if len(data) == 0 {
		return 0, nil
	}
	var j hbJournal
	if jerr := json.Unmarshal(data, &j); jerr != nil {
		return 0, fmt.Errorf("chunk journal %s: %w", r.journalID, jerr)
	}
	if j.Version != hbJournalVersion || j.OpID != r.opID {
		return 0, fmt.Errorf("chunk journal %s is version %d for operation %q, want version %d for %q",
			r.journalID, j.Version, j.OpID, hbJournalVersion, r.opID)
	}
	var outs []hbOutcome
	for n, e := range j.Entries {
		if !r.counts.valid(e.Bucket) {
			return 0, fmt.Errorf("chunk journal %s entry %d has an unknown bucket %q", r.journalID, n+1, e.Bucket)
		}
		if !hbValidID(e.FileID) || !hbValidID(e.BookID) {
			return 0, fmt.Errorf("chunk journal %s entry %d has an invalid file or book ID", r.journalID, n+1)
		}
		if r.report.has(e.FileID) {
			continue
		}
		outs = append(outs, hbOutcome{bucket: e.Bucket, reason: e.Reason, hash: e.Hash,
			bf: database.BookFileCore{ID: e.FileID, BookID: e.BookID, FilePath: e.Path, FileSize: e.Size, LastScanMtime: e.Mtime}})
	}
	r.storeHashes(outs)
	r.noteRecomputeFailures(r.recomputeBooks(outs, true))
	if err := r.recordOutcomes(outs); err != nil {
		return 0, err
	}
	if err := r.removeJournal(); err != nil {
		return 0, err
	}
	for _, o := range outs {
		r.report.done[o.bf.ID] = struct{}{}
	}
	return len(outs), nil
}

// Job supports resume after restart: the report is the progress record (every
// row the run reached is listed in it, and a stored hash marks a row done), so a
// resumed run walks the rows from the start and skips what is already done.
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

	// Has this operation been started before? A "started" marker is saved at the
	// beginning of every run and cleared on completion, so a marker that is
	// already there means this run is a resumption. Resume does not depend on
	// it (the report and the stored hashes carry the progress); it only lets the
	// summary say so when there is no report to carry the earlier counts.
	opID := maintenance.OperationIDFromCtx(ctx)
	run.opID = opID
	markerFound, markerUnknown := false, false
	if opID != "" {
		cp, cerr := operations.LoadCheckpoint(store, opID)
		switch {
		case cerr != nil:
			markerUnknown = true
			msg := cerr.Error()
			hbLog.Warn("could not read the operation's saved state op=%s err=%s", logger.SanitizeLogValue(opID), logger.SanitizeLogValue(msg))
			run.log("warn", "Could not read this operation's saved state; resuming from the report alone", &msg)
		case cp != nil:
			markerFound = true
		}
	}

	reportPath := ""
	prior := false
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
		path, rerr := libroot.ReportPathNotify(root, "", hashBackfillReportName(opID, time.Now()), func(dir string, was fs.FileMode) {
			hbLog.Warn("removed world write from the report directory, its mode was %o dir=%s", was, logger.SanitizeLogValue(dir))
		})
		if rerr != nil {
			return fmt.Errorf("backfill-file-hashes: refusing to run: %w", rerr)
		}
		report, seeded, hadPrior, oerr := openHashBackfillReport(path, opID != "")
		if oerr != nil {
			return fmt.Errorf("backfill-file-hashes: open report %s: %w", path, oerr)
		}
		defer func() {
			if cerr := report.close(); cerr != nil && retErr == nil {
				retErr = fmt.Errorf("backfill-file-hashes: report %s: %w", path, cerr)
			}
		}()
		run.report = report
		run.counts = seeded
		if opID != "" {
			run.journalID = hbJournalStateID(opID)
		}
		reportPath = path
		prior = hadPrior
		if report.done == nil {
			report.done = map[string]struct{}{}
		}
		run.log("info", "Per-row report: "+path, nil)
		if prior {
			run.log("info", fmt.Sprintf("Resuming: the report already accounts for %d rows", len(report.done)), nil)
		}
		n, jerr := run.replayJournal()
		if jerr != nil {
			return fmt.Errorf("backfill-file-hashes: %w", jerr)
		}
		if n > 0 {
			run.log("info", fmt.Sprintf("Recovered %d rows from a chunk the earlier run was cut off in the middle of", n), nil)
		}
	}

	// The summary says so when this run cannot account for the whole operation:
	// it was started before (or its saved state cannot be read) but no earlier
	// report was found to carry the counts.
	partialNote := ""
	switch {
	case prior:
	case markerFound:
		partialNote = "this operation was started before but no earlier report was found: counts cover only the work done in this run"
	case markerUnknown:
		partialNote = "this operation's saved state could not be read and no earlier report was found: counts may cover only part of the operation"
	}
	if opID != "" {
		if serr := operations.SaveCheckpoint(store, opID, "maintenance:backfill-file-hashes", "started", 0, len(files)); serr != nil {
			msg := serr.Error()
			hbLog.Warn("could not save the operation's started marker op=%s err=%s", logger.SanitizeLogValue(opID), logger.SanitizeLogValue(msg))
			run.log("warn", "Could not save this operation's started marker", &msg)
		}
	}

	// Process in ORDERED chunks, always from the first row. Each chunk runs
	// through a bounded worker pool that only DECIDES each row (hash, classify),
	// skipping rows that already have a hash or that the report already lists;
	// nothing is stored, counted or reported until the whole chunk is done, then
	// commitChunk does all three. A run cancelled mid-chunk therefore leaves no
	// report row, no counter and no stored hash behind for that chunk, and a
	// process KILLED inside commitChunk is covered by the chunk journal
	// (writeJournal/replayJournal). Walking from row 0 instead of a saved index
	// costs one map lookup per finished row, and it cannot skip rows when the row
	// list shifts between attempts (rows retired or restored in between).
	for start := 0; start < len(files); start += hashBackfillChunkSize {
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
		// means the context was cancelled. Return it WITHOUT committing.
		if gerr := g.Wait(); gerr != nil {
			return gerr
		}
		// A share that drops mid-run makes every later path under it answer
		// not-found, which would be reported (and, on a resume, skipped) as a
		// library of missing files. So a chunk that found files absent under the
		// root has the root's mount checked again before its rows are recorded; if
		// it no longer looks mounted the run stops, and a resume decides the chunk
		// again. A chunk with no under-root not-found has nothing to distrust, so
		// the common case costs no directory read.
		if run.root != "" && hbHasNotFound(outs) {
			if merr := libroot.Mounted(run.root); merr != nil {
				return fmt.Errorf("backfill-file-hashes: chunk at row %d: the library root no longer looks mounted, so its not-found rows are not recorded: %w", start, merr)
			}
		}
		// The report is the run's deliverable: stop rather than hash on while rows
		// are being dropped from it.
		if rerr := run.commitChunk(start, outs); rerr != nil {
			return fmt.Errorf("backfill-file-hashes: chunk at row %d (report %s): %w", start, reportPath, rerr)
		}
	}

	// Close the report BEFORE declaring the run done. Every chunk is already
	// flushed, so only the file close itself can fail here; if it does, the run
	// fails and no "completed" summary is saved, so the status and the summary
	// cannot disagree.
	if cerr := run.report.close(); cerr != nil {
		return fmt.Errorf("backfill-file-hashes: report %s: %w", reportPath, cerr)
	}

	// Clear the started marker on clean completion. A failure only leaves a stale
	// marker (a later re-run of the same operation would say it was resumed).
	if opID != "" {
		if cerr := operations.ClearState(store, opID); cerr != nil {
			msg := cerr.Error()
			hbLog.Warn("could not clear the operation's started marker op=%s err=%s", logger.SanitizeLogValue(opID), logger.SanitizeLogValue(msg))
			run.log("warn", "Could not clear this operation's started marker", &msg)
		}
	}

	// Save a lightweight operation summary for the UI and activity feed.
	if w := run.failedBooksNote(); w != "" {
		run.log("warn", w, nil)
		if partialNote != "" {
			partialNote += "; "
		}
		partialNote += w
	}
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

// Policy: ResumeRestart because a restarted run resumes exactly: the progress
// record is the per-run report (every row the run reached is listed in it) plus
// the hashes already stored, which a rerun of the same operation reads back and
// skips. The checkpoint (operations.SaveCheckpoint) is only a "started" marker
// that lets the summary say a run was resumed when no earlier report survives; it
// carries no row index. PR-2 moves the marker to reporter.Checkpoint; the policy
// is unchanged by that move.
func (j *backfillFileHashesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
